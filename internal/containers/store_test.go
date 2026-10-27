package containers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidationRejectsUnsafeSites(t *testing.T) {
	for _, site := range []string{"ftp://example.com", "https://example.com/path", "https://user@example.com", "https://example.com?x=1", "not-url"} {
		if validate("site-a", site) == nil {
			t.Fatalf("accepted unsafe site %q", site)
		}
	}
	if validate("bad id!", "https://example.com") == nil {
		t.Fatal("accepted unsafe account id")
	}
	if validate("local-site", "http://yrp.site:8002") != nil {
		t.Fatal("local HTTP origin rejected")
	}
}
func TestConfiguredChannelsAreLimited(t *testing.T) {
	if !channels["WhatsApp"] || !channels["Telegram"] || !channels["Email"] || channels["SMS"] {
		t.Fatal("channel allowlist drift")
	}
}

func TestCredentialDecryptionRejectsCorruption(t *testing.T) {
	s, err := New(nil, "test-only-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, nonce, err := s.box("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.unbox(encrypted, nil); err == nil {
		t.Fatal("missing nonce accepted")
	}
	encrypted[0] ^= 1
	if _, err = s.unbox(encrypted, nonce); err == nil {
		t.Fatal("tampered secret accepted")
	}
}

func TestAccountPersistenceLifecycle(t *testing.T) {
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RELAY_TEST_DATABASE_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("relay_accounts_test_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	store, _ := New(pool, "test-only-encryption-key")
	a := Account{ID: "test-account", Site: "https://example.test", Enabled: true, Channels: []AccountChannel{{Channel: "WhatsApp", Enabled: true}}}
	if err = store.Save(ctx, a, nil); err != nil {
		t.Fatal("blank secret insert", err)
	}
	if err = store.Save(ctx, a, map[string]string{"WhatsApp": "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, a, nil); err != nil {
		t.Fatal("blank update", err)
	}
	duplicate := a
	duplicate.Site = "https://wrong.example.test"
	if err := store.Create(ctx, duplicate, map[string]string{"WhatsApp": "replacement"}); err == nil {
		t.Fatal("duplicate create overwrote account")
	}
	runtime, err := store.Runtime(ctx)
	if err != nil || len(runtime) != 1 || runtime[0].Secret != "synthetic-token" {
		t.Fatal("secret preservation", err)
	}
	listed, err := store.List(ctx)
	if err != nil || len(listed) != 1 || len(listed[0].Channels) != 1 {
		t.Fatal("account listing", err)
	}

	t.Run("partial channel save", func(t *testing.T) {
		defer pool.Exec(ctx, `DELETE FROM relay_channel_credentials WHERE channel_key='partial-save'; DELETE FROM relay_sites WHERE site_id='partial-save'`)
		a := Account{ID: "partial-save", Site: "https://fixture.invalid", Enabled: true, Channels: []AccountChannel{
			{Channel: "WhatsApp", Enabled: true, Config: map[string]any{"groups": true, "calls": true}},
			{Channel: "Telegram", Enabled: true, Config: map[string]any{}},
		}}
		if err := store.Save(ctx, a, map[string]string{"Telegram": "synthetic-secret"}); err != nil {
			t.Fatal(err)
		}
		a.Channels = a.Channels[:1]
		if err := store.Save(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		rows, err := store.Runtime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.Key == a.ID && row.Channel == "Telegram" {
				found = row.Secret == "synthetic-secret"
			}
		}
		if !found {
			t.Fatal("partial WhatsApp save disabled hidden Telegram credentials")
		}
	})

	t.Run("bounded searchable keyset pages", func(t *testing.T) {
		for i := 0; i < 65; i++ {
			row := Account{ID: fmt.Sprintf("page_%03d", i), Site: "https://page.example.test", Enabled: true, Channels: []AccountChannel{{Channel: "Telegram", Enabled: true}, {Channel: "Email", Enabled: true}}}
			if err := store.Save(ctx, row, nil); err != nil {
				t.Fatal(err)
			}
		}
		defer pool.Exec(ctx, `DELETE FROM relay_channel_credentials WHERE channel_key LIKE 'page_%'; DELETE FROM relay_sites WHERE site_id LIKE 'page_%'`)
		seen := map[string]bool{}
		cursor := ""
		for {
			page, next, err := store.Page(ctx, "page_", cursor, 30)
			if err != nil || len(page) > 30 {
				t.Fatalf("page: %d %v", len(page), err)
			}
			for _, row := range page {
				if seen[row.ID] || len(row.Channels) != 2 {
					t.Fatalf("duplicate or split account %s", row.ID)
				}
				seen[row.ID] = true
			}
			if next == "" {
				break
			}
			if next == cursor {
				t.Fatal("cursor did not advance")
			}
			cursor = next
		}
		if len(seen) != 65 {
			t.Fatalf("lost accounts: %d", len(seen))
		}
		page, _, err := store.Page(ctx, "%", "", 30)
		if err != nil || len(page) != 0 {
			t.Fatal("wildcard search was not escaped", err)
		}
		if _, _, err := store.Page(ctx, "", "", 101); err == nil {
			t.Fatal("unbounded page accepted")
		}
		before, more, err := store.PageBefore(ctx, "page_", "page_040", 10)
		if err != nil || !more || len(before) != 10 || before[0].ID != "page_030" || before[9].ID != "page_039" {
			t.Fatalf("backward page: %#v %v %v", before, more, err)
		}
	})
	a.Channels[0].Config = map[string]any{"app_secret": "private-meta-secret", "core_api_key": "private-core-key", "core_api_secret": "private-core-secret", "callback_secret": "private-callback-secret-0123456789abcdef", "phone_id": "123"}
	if err = store.Save(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT config FROM relay_channel_credentials WHERE channel_key=$1`, a.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") {
		t.Fatal("plaintext secret in config")
	}
	listed, err = store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(listed)
	if strings.Contains(string(public), "private-") {
		t.Fatal("secret leaked in listing")
	}
	if len(listed[0].Channels[0].ConfiguredSecrets) != 5 {
		t.Fatal("missing presence indicators")
	}
	a.Channels[0].Config = map[string]any{"app_secret": "", "phone_id": "456"}
	if err = store.Save(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	runtime, err = store.Runtime(ctx)
	if err != nil || runtime[0].Core.APISecret != "private-core-secret" || runtime[0].Config["app_secret"] != "private-meta-secret" {
		t.Fatal("blank edit lost secrets", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE relay_channel_credentials SET config=config || '{"verify_token":"legacy-private-verification"}'::jsonb`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = store.ProtectLegacyConfig(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err = store.Runtime(ctx)
	if err != nil || runtime[0].Config["verify_token"] != "legacy-private-verification" {
		t.Fatal("legacy migration failed", err)
	}
	pool.QueryRow(ctx, `SELECT config FROM relay_channel_credentials WHERE channel_key=$1`, a.ID).Scan(&raw)
	if strings.Contains(string(raw), "legacy-private") {
		t.Fatal("legacy plaintext retained")
	}
	a.Channels[0].Config = map[string]any{"password": "unsupported-secret-key"}
	if store.Save(ctx, a, nil) == nil {
		t.Fatal("unknown plaintext credential accepted")
	}
	a.Channels = nil
	if err = store.Save(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	runtime, err = store.Runtime(ctx)
	if err != nil || len(runtime) != 1 {
		t.Fatal("omitted channel was disabled", err)
	}
	a.Enabled = false
	if err = store.Save(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	restarted, _ := New(pool, "test-only-encryption-key")
	runtime, err = restarted.Runtime(ctx)
	if err != nil || len(runtime) != 1 || runtime[0].SiteEnabled {
		t.Fatal("disabled account reactivated after restart", err)
	}
	a.Enabled = true
	a.Channels = []AccountChannel{{Channel: "WhatsApp", Enabled: false}}
	if err = store.Save(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	runtime, err = restarted.Runtime(ctx)
	if err != nil || len(runtime) != 0 {
		t.Fatal("explicit channel disable ignored", err)
	}

	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM relay_channel_credentials").Scan(&count); err != nil || count != 1 {
		t.Fatal("removed credentials were deleted", err)
	}
}
