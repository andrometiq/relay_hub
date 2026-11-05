package containers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const siteBotToken = "123456:site_bot_token_local_only_value"

func siteChannelsStore(t *testing.T) (*Store, func(string) Channel) {
	ctx, s := directoryStore(t)
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_containers(container_id,name,signup) VALUES('signup','signup',true),('manual','manual',false);
 INSERT INTO relay_sites(site_id,origin) VALUES('shop','https://shop.invalid'),('other','https://other.invalid')`); err != nil {
		t.Fatal(err)
	}
	bot := func(label string) Channel {
		return Channel{Channel: "Telegram", Label: label, Config: map[string]any{"username": "acme_textiles_bot"}}
	}
	return s, bot
}

func TestSiteChannelIsStoredLinkedAndUniqueByIdentity(t *testing.T) {
	s, bot := siteChannelsStore(t)
	ctx := t.Context()
	key, err := s.SaveSiteChannel(ctx, "shop", bot("Acme Textiles"), siteBotToken)
	if err != nil || !strings.HasPrefix(key, "tg-") {
		t.Fatal(key, err)
	}
	stored, err := s.GetChannel(ctx, ChannelRef{Channel: "Telegram", Key: key})
	if err != nil || stored.Site != "shop" || stored.Container != "signup" || stored.Address != "123456" || !stored.Enabled || stored.Config["site_managed"] != true {
		t.Fatalf("stored %+v %v", stored, err)
	}
	for _, site := range []string{"shop", "other"} {
		if _, err = s.SaveSiteChannel(ctx, site, bot("Again"), siteBotToken); !errors.Is(err, ErrChannelInUse) {
			t.Fatalf("%s connected a live bot again: %v", site, err)
		}
	}
	mail := Channel{Channel: "Email", Config: map[string]any{"from": "Sales@Example.com", "host": "smtp.example.com", "port": 587, "username": "sales", "imap_password": "imap-local-only"}}
	mailKey, err := s.SaveSiteChannel(ctx, "shop", mail, "smtp-local-only")
	if err != nil || !strings.HasPrefix(mailKey, "mail-") {
		t.Fatal(mailKey, err)
	}
	credential, err := s.Credential(ctx, "Email", mailKey)
	if err != nil || credential.Secret != "smtp-local-only" || credential.Config["imap_password"] != "imap-local-only" || credential.Address != "sales@example.com" {
		t.Fatalf("mailbox credential %+v %v", credential.Address, err)
	}
}

func TestSiteChannelWithoutSignupContainerIsRefused(t *testing.T) {
	ctx, s := directoryStore(t)
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES('shop','https://shop.invalid')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSiteChannel(ctx, "shop", Channel{Channel: "Telegram", Config: map[string]any{}}, siteBotToken); !errors.Is(err, ErrNoSignupContainer) {
		t.Fatal(err)
	}
}

func TestRemovedSiteChannelIsWipedAndKeepsItsKeyWhenConnectedAgain(t *testing.T) {
	s, bot := siteChannelsStore(t)
	ctx := t.Context()
	key, err := s.SaveSiteChannel(ctx, "shop", bot("Acme Textiles"), siteBotToken)
	if err != nil {
		t.Fatal(err)
	}
	ref := ChannelRef{Channel: "Telegram", Key: key}
	if err = s.RemoveSiteChannel(ctx, "other", ref); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another site removed the bot: %v", err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO relay_channel_outbox(site_id,account,operation,request_key,payload,payload_hash,state) VALUES('shop',$1,'telegram.message','k1','{}','h1','queued')`, key); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveSiteChannel(ctx, "shop", ref); !errors.Is(err, ErrPendingSends) {
		t.Fatalf("removed with open sends: %v", err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE relay_channel_outbox SET state='submitted'`); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveSiteChannel(ctx, "shop", ref); err != nil {
		t.Fatal(err)
	}
	removed, err := s.GetChannel(ctx, ref)
	if err != nil || removed.Site != "" || removed.Enabled || len(removed.ConfiguredSecrets) != 0 || removed.Config["webhook_status"] != "removed" {
		t.Fatalf("removed %+v %v", removed, err)
	}
	again, err := s.SaveSiteChannel(ctx, "other", bot("Acme Textiles"), siteBotToken)
	if err != nil || again != key {
		t.Fatalf("connected again as %s: %v", again, err)
	}
}

func TestUpdateSiteChannelOnlyChangesTheSitesOwnChannel(t *testing.T) {
	s, bot := siteChannelsStore(t)
	ctx := t.Context()
	key, err := s.SaveSiteChannel(ctx, "shop", bot("Acme Textiles"), siteBotToken)
	if err != nil {
		t.Fatal(err)
	}
	change := Channel{Key: key, Channel: "Telegram", Label: "Acme Sales", Enabled: false, Config: map[string]any{"username": "acme_textiles_bot", "site_managed": true}}
	if err = s.UpdateSiteChannel(ctx, "other", change, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another site changed the bot: %v", err)
	}
	if err = s.UpdateSiteChannel(ctx, "shop", change, ""); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetChannel(ctx, ChannelRef{Channel: "Telegram", Key: key})
	if err != nil || stored.Label != "Acme Sales" || stored.Enabled || len(stored.ConfiguredSecrets) != 1 {
		t.Fatalf("updated %+v %v", stored, err)
	}
	if err = s.UpdateSiteValues(ctx, "other", ChannelRef{Channel: "Telegram", Key: key}, nil, map[string]any{"webhook_status": "registered"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another site wrote a status: %v", err)
	}
	if err = s.UpdateSiteValues(ctx, "shop", ChannelRef{Channel: "Telegram", Key: key}, nil, map[string]any{"bot_token": "x"}); err == nil {
		t.Fatal("a private value was written as a status")
	}
}

func TestSiteAttemptsAreLimitedPerSiteAndAction(t *testing.T) {
	ctx, s := directoryStore(t)
	for i := range 3 {
		allowed, err := s.AllowSiteAttempt(ctx, "shop", "Telegram", "telegram.bot.connect", time.Hour, 2)
		if err != nil || allowed != (i < 2) {
			t.Fatalf("attempt %d: %v %v", i, allowed, err)
		}
	}
	if allowed, err := s.AllowSiteAttempt(ctx, "other", "Telegram", "telegram.bot.connect", time.Hour, 2); err != nil || !allowed {
		t.Fatal("another site has its own limit")
	}
	if allowed, err := s.AllowSiteAttempt(ctx, "shop", "Telegram", "telegram.bot.check", time.Hour, 2); err != nil || !allowed {
		t.Fatal("another tool has its own limit")
	}
}
