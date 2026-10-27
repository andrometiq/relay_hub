package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/containers"
)

// One container holds three bots and two mailboxes. Each bot and mailbox has
// its own webhook secret, and its events take the site of their channel.
func TestBotsAndMailboxesKeepTheirOwnWebhooksAndSites(t *testing.T) {
	ctx := context.Background()
	pool, _ := channelTestDB(t)
	store, err := containers.New(pool, "multi-channel-key")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveContainer(ctx, containers.Container{ID: "main", Enabled: true}, nil, true); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"bot-a", "bot-b", "bot-c"} {
		bot := containers.Channel{Key: key, Channel: "Telegram", Container: "main", Label: key, Enabled: true, Config: map[string]any{"username": key}}
		if err = store.SaveChannel(ctx, bot, fmt.Sprintf("60000%d:bot-secret-abcdefghijklmnopqrstu", i+1), true); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"sales", "help"} {
		mailbox := containers.Channel{Key: "mail-" + key, Channel: "Email", Container: "main", Label: key, Enabled: true,
			Config: map[string]any{"host": "smtp.invalid", "port": float64(587), "username": key, "from": key + "@essdee.invalid"}}
		if err = store.SaveChannel(ctx, mailbox, "smtp-password", true); err != nil {
			t.Fatal(err)
		}
	}
	for site, refs := range map[string][]containers.ChannelRef{
		"site-a": {{Channel: "Telegram", Key: "bot-a"}, {Channel: "Email", Key: "mail-sales"}},
		"site-b": {{Channel: "Telegram", Key: "bot-b"}, {Channel: "Email", Key: "mail-help"}},
	} {
		if err = store.SaveSite(ctx, containers.Site{ID: site, Origin: "https://" + site + ".invalid", Enabled: true}, nil, true); err != nil {
			t.Fatal(err)
		}
		if err = store.AssignChannels(ctx, site, refs); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.Runtime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := Load(rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(registry.SiteKeys("Telegram", "site-a"), ",") != "bot-a" || strings.Join(registry.SiteKeys("Email", "site-b"), ",") != "mail-help" {
		t.Fatal("site keys")
	}
	events := Store{DB: pool}
	master := strings.Repeat("m", 32)
	for i, bot := range []string{"bot-a", "bot-b", "bot-c"} {
		secret, _ := registry.TelegramWebhookSecret(bot, master)
		update := []byte(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":7,"date":1700000000,"chat":{"id":42},"text":"to %s"}}`, i+1, bot))
		if bot == "bot-b" {
			wrong, _ := registry.TelegramWebhookSecret("bot-a", master)
			if registry.TelegramWebhook(ctx, events, bot, master, []byte(wrong), update) == nil {
				t.Fatal("another bot's secret was accepted")
			}
		}
		if err = registry.TelegramWebhook(ctx, events, bot, master, []byte(secret), update); err != nil {
			t.Fatal(err)
		}
	}
	for _, mailbox := range []string{"mail-sales", "mail-help"} {
		secret, _ := registry.EmailWebhookSecret(mailbox, master)
		other, _ := registry.EmailWebhookSecret("mail-sales", master)
		body, _ := json.Marshal(map[string]any{"id": "msg-" + mailbox, "raw": "From: customer@example.invalid\r\nTo: " + mailbox + "@essdee.invalid\r\nSubject: Order\r\nMessage-ID: <" + mailbox + "@example.invalid>\r\n\r\nHello\r\n"})
		if mailbox == "mail-help" && registry.EmailWebhook(ctx, events, mailbox, master, []byte(other), body) == nil {
			t.Fatal("another mailbox's secret was accepted")
		}
		if err = registry.EmailWebhook(ctx, events, mailbox, master, []byte(secret), body); err != nil {
			t.Fatal(err)
		}
	}
	sites := map[string]string{}
	stored, err := pool.Query(ctx, `SELECT account,coalesce(site_id,'') FROM relay_channel_events`)
	if err != nil {
		t.Fatal(err)
	}
	for stored.Next() {
		var key, site string
		if err = stored.Scan(&key, &site); err != nil {
			t.Fatal(err)
		}
		sites[key] = site
	}
	stored.Close()
	want := map[string]string{"bot-a": "site-a", "bot-b": "site-b", "bot-c": "", "mail-sales": "site-a", "mail-help": "site-b"}
	if fmt.Sprint(sites) != fmt.Sprint(want) {
		t.Fatalf("event sites: %v", sites)
	}

	// The unassigned bot's event waits until the bot has a site.
	var id int64
	if err = pool.QueryRow(ctx, `SELECT id FROM relay_channel_events WHERE account='bot-c'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err = events.ReplayUnassigned(ctx, id); err != ErrNotAssigned {
		t.Fatalf("replay before assignment: %v", err)
	}
	if err = store.AssignChannels(ctx, "site-b", []containers.ChannelRef{{Channel: "Telegram", Key: "bot-b"}, {Channel: "Email", Key: "mail-help"}, {Channel: "Telegram", Key: "bot-c"}}); err != nil {
		t.Fatal(err)
	}
	if err = events.ReplayUnassigned(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = events.ReplayUnassigned(ctx, id); err != ErrNotAssigned {
		t.Fatal("event replayed twice")
	}
	var site string
	if err = pool.QueryRow(ctx, `SELECT site_id FROM relay_channel_events WHERE id=$1`, id).Scan(&site); err != nil || site != "site-b" {
		t.Fatalf("replayed site: %q %v", site, err)
	}
}
