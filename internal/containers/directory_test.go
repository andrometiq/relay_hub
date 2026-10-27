package containers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func directoryStore(t *testing.T) (context.Context, *Store) {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RELAY_TEST_DATABASE_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("relay_directory_test_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); base.Close() })
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	store, _ := New(pool, "directory-test-key")
	return ctx, store
}

// legacyChannel writes a channel the way 0038 leaves a legacy account row.
func legacyChannel(t *testing.T, ctx context.Context, s *Store, id, channel string, public map[string]any, private map[string]string) {
	t.Helper()
	cipher, nonce, err := s.boxMap(private)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(public)
	if _, err = s.DB.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES($1,'https://legacy.invalid') ON CONFLICT DO NOTHING;`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO relay_containers(container_id,name) VALUES($1,$1) ON CONFLICT DO NOTHING`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO relay_channel_credentials(channel,channel_key,container_id,site_id,enabled,config,config_ciphertext,config_nonce)
 VALUES($1,$2,$2,$2,true,$3,$4,$5)`, channel, id, raw, cipher, nonce); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeMovesLegacyValuesToContainerAndSite(t *testing.T) {
	ctx, s := directoryStore(t)
	core := map[string]string{"core_api_key": "key", "core_api_secret": "secret", "callback_secret": strings.Repeat("c", 32)}
	wa := map[string]string{"app_secret": "app-secret", "verify_token": "verify"}
	for k, v := range core {
		wa[k] = v
	}
	legacyChannel(t, ctx, s, "legacy", "WhatsApp", map[string]any{"app_id": "1", "graph_version": "v25.0", "phone_id": "11", "waba_id": "22"}, wa)
	legacyChannel(t, ctx, s, "legacy", "Telegram", map[string]any{}, core)
	legacyChannel(t, ctx, s, "legacy", "Email", map[string]any{"host": "smtp.invalid", "from": "a@b.invalid"}, map[string]string{"imap_password": "imap", "core_api_key": "key"})
	for range 2 {
		if err := s.UpgradeContainers(ctx); err != nil {
			t.Fatal(err)
		}
	}
	container, err := s.GetContainer(ctx, "legacy")
	if err != nil || container.WhatsApp.Config["app_id"] != "1" || len(container.WhatsApp.ConfiguredSecrets) != 2 {
		t.Fatalf("container app not moved: %#v %v", container.WhatsApp, err)
	}
	site, err := s.GetSite(ctx, "legacy")
	if err != nil || len(site.ConfiguredSecrets) != 3 || len(site.Channels) != 3 {
		t.Fatalf("site credentials not moved: %#v %v", site, err)
	}
	for _, channel := range site.Channels {
		if _, ok := channel.Config["app_id"]; ok || contains(channel.ConfiguredSecrets, "core_api_key") {
			t.Fatalf("legacy value left on channel %s", channel.Channel)
		}
	}
	rows, err := s.Runtime(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Core.CallbackSecret != core["callback_secret"] || row.Site != "legacy" {
			t.Fatalf("runtime lost site credentials for %s", row.Channel)
		}
		if row.Channel == "WhatsApp" && (row.Config["app_secret"] != "app-secret" || row.Config["phone_id"] != "11") {
			t.Fatal("runtime did not join the container app")
		}
		if row.Channel == "Email" && row.Config["imap_password"] != "imap" {
			t.Fatal("channel private value lost")
		}
	}
}

func TestUpgradeRefusesDifferentLegacyValues(t *testing.T) {
	ctx, s := directoryStore(t)
	legacyChannel(t, ctx, s, "mixed", "Telegram", map[string]any{}, map[string]string{"core_api_key": "one"})
	legacyChannel(t, ctx, s, "mixed", "Email", map[string]any{}, map[string]string{"core_api_key": "two"})
	legacyChannel(t, ctx, s, "mixed", "WhatsApp", map[string]any{}, map[string]string{"core_api_key": "three"})
	s.DB.Exec(ctx, `UPDATE relay_channel_credentials SET enabled=false WHERE channel='WhatsApp'`)
	if err := s.UpgradeContainers(ctx); err == nil || !strings.Contains(err.Error(), "different core_api_key") {
		t.Fatalf("mismatch accepted: %v", err)
	}
}

func TestChannelAddressIsUniqueAndSiteOwnsChannel(t *testing.T) {
	ctx, s := directoryStore(t)
	for _, id := range []string{"meta-a", "meta-b"} {
		if err := s.SaveContainer(ctx, Container{ID: id, Enabled: true}, &ProviderInput{Config: map[string]any{"app_id": "9", "app_secret": "x"}}, true); err != nil {
			t.Fatal(err)
		}
	}
	number := Channel{Key: "sales", Channel: "WhatsApp", Container: "meta-a", Enabled: true, Config: map[string]any{"phone_id": "101", "waba_id": "201"}}
	if err := s.SaveChannel(ctx, number, "token", true); err != nil {
		t.Fatal(err)
	}
	copied := number
	copied.Key, copied.Container = "copy", "meta-b"
	if err := s.SaveChannel(ctx, copied, "", true); err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("duplicate phone_id accepted: %v", err)
	}
	moved := number
	moved.Container = "meta-b"
	if err := s.SaveChannel(ctx, moved, "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("channel moved between containers: %v", err)
	}
	for _, id := range []string{"site-a", "site-b"} {
		if err := s.SaveSite(ctx, Site{ID: id, Origin: "https://" + id + ".invalid", Enabled: true}, nil, true); err != nil {
			t.Fatal(err)
		}
	}
	ref := []ChannelRef{{Channel: "WhatsApp", Key: "sales"}}
	if err := s.AssignChannels(ctx, "site-a", ref); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "site-b", ref); !errors.Is(err, ErrConflict) {
		t.Fatalf("one channel assigned to two sites: %v", err)
	}
	if err := s.AssignChannels(ctx, "site-a", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "site-b", ref); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "site-b", []ChannelRef{{Channel: "WhatsApp", Key: "missing"}}); err == nil {
		t.Fatal("unknown channel assigned")
	}
	var notices int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE account='' AND payload->>'object'='relay_channels'`).Scan(&notices); err != nil || notices != 3 {
		t.Fatalf("relay_channels events: %d %v", notices, err)
	}
	site, err := s.GetSite(ctx, "site-b")
	if err != nil || len(site.Channels) != 1 || site.Channels[0].Address != "101" {
		t.Fatalf("assigned channel missing: %#v %v", site, err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_outbox(account,request_key,payload,site_id) VALUES('sales','k1','{}','site-b')`); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "site-b", nil); !errors.Is(err, ErrPendingSends) {
		t.Fatalf("unassigned a channel with queued sends: %v", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE relay_outbox SET state='cancelled' WHERE request_key='k1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "site-b", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContainer(ctx, Container{ID: "bots", Enabled: true}, nil, true); err != nil {
		t.Fatal(err)
	}
	clash := Channel{Key: "sales", Channel: "Telegram", Container: "bots", Enabled: true, Config: map[string]any{}}
	if err := s.SaveChannel(ctx, clash, "", true); err == nil {
		t.Fatal("key shared across providers accepted")
	}
	if err := s.SaveSite(ctx, Site{ID: "site-b", Origin: "https://site-b.invalid", Enabled: true}, map[string]any{"core_api_key": "only-one"}, false); err == nil {
		t.Fatal("partial Core credentials accepted")
	}
}

func TestTelegramBotAddressComesFromToken(t *testing.T) {
	ctx, s := directoryStore(t)
	if err := s.SaveContainer(ctx, Container{ID: "bots", Enabled: true}, nil, true); err != nil {
		t.Fatal(err)
	}
	bot := Channel{Key: "support-bot", Channel: "Telegram", Container: "bots", Enabled: true, Config: map[string]any{"username": "support_bot"}}
	if err := s.SaveChannel(ctx, bot, "424242:token-value-abcdefghijklmnopqrst", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel(ctx, bot, "", false); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetChannel(ctx, ChannelRef{Channel: "Telegram", Key: "support-bot"})
	if err != nil || saved.Address != "424242" || !contains(saved.ConfiguredSecrets, "secret") {
		t.Fatalf("bot identity: %#v %v", saved, err)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "token-value") {
		t.Fatal("bot token leaked")
	}
}
