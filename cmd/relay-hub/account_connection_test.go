package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/accounts"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/jackc/pgx/v5"
)

type connectionFixture struct {
	calls   int
	repair  bool
	fail    bool
	address string
}

func (f *connectionFixture) TelegramConnection(_ context.Context, account, address, master string, repair bool) (telegram.WebhookHealth, error) {
	f.calls++
	f.repair = repair
	f.address = address
	if f.fail {
		return telegram.WebhookHealth{}, errors.New("private-provider-secret")
	}
	return telegram.WebhookHealth{Registered: true, Matches: true, Pending: 4}, nil
}
func (f *connectionFixture) TestEmailConnection(context.Context, string) (channels.EmailConnection, error) {
	f.calls++
	return channels.EmailConnection{SMTP: "connected", IMAP: "failed"}, nil
}

func TestAccountConnectionAuthorizationAndShape(t *testing.T) {
	for _, tc := range []struct {
		name, role, method, channel, body, origin, csrf string
		status                                          int
		fail                                            bool
	}{
		{name: "signed out", method: "GET", channel: "telegram", status: 401},
		{name: "member", role: "member", method: "GET", channel: "telegram", status: 403},
		{name: "status", role: "admin", method: "GET", channel: "telegram", status: 200},
		{name: "email GET denied", role: "admin", method: "GET", channel: "email", status: 405},
		{name: "invalid channel", role: "admin", method: "GET", channel: "sms", status: 400},
		{name: "origin denied", role: "admin", method: "POST", channel: "telegram", body: `{"action":"repair"}`, origin: "https://evil.invalid", csrf: "valid", status: 403},
		{name: "csrf denied", role: "admin", method: "POST", channel: "email", body: `{"action":"test"}`, origin: "https://hub.invalid", csrf: "bad", status: 403},
		{name: "target injection denied", role: "admin", method: "POST", channel: "telegram", body: `{"action":"repair","url":"https://evil.invalid"}`, origin: "https://hub.invalid", csrf: "valid", status: 400},
		{name: "repair", role: "admin", method: "POST", channel: "telegram", body: `{"action":"repair"}`, origin: "https://hub.invalid", csrf: "valid", status: 200},
		{name: "test email", role: "admin", method: "POST", channel: "email", body: `{"action":"test"}`, origin: "https://hub.invalid", csrf: "valid", status: 200},
		{name: "provider failure redacted", role: "admin", method: "GET", channel: "telegram", status: 502, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &connectionFixture{fail: tc.fail}
			h := accountConnection(fixture, func(*http.Request) (auth.User, error) {
				if tc.role == "" {
					return auth.User{}, errors.New("no session")
				}
				return auth.User{Role: tc.role}, nil
			}, "https://hub.invalid", "https://hub.invalid/hub", "master-secret")
			r := httptest.NewRequest(tc.method, "/api/accounts/local/"+tc.channel+"/connection", strings.NewReader(tc.body))
			r.SetPathValue("account", "local")
			r.SetPathValue("channel", tc.channel)
			r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
			r.Header.Set("Origin", tc.origin)
			if tc.csrf == "valid" {
				r.Header.Set("X-Relay-CSRF", auth.CSRF("session"))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("secret leaked")
			}
			if tc.status == 200 {
				var result map[string]any
				if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("invalid response")
				}
				if tc.channel == "telegram" && (result["matches"] != true || fixture.address != "https://hub.invalid/hub/webhooks/telegram/local" || fixture.repair != (tc.name == "repair")) {
					t.Fatal("invalid webhook result")
				}
				if tc.channel == "email" && (result["smtp"] != "connected" || result["imap"] != "failed") {
					t.Fatal("invalid email result")
				}
			} else if tc.status != 502 && fixture.calls != 0 {
				t.Fatal("denied request reached provider")
			}
		})
	}
}

type savedCredential struct{ value accounts.RuntimeCredential }

func (s savedCredential) Credential(context.Context, string, string) (accounts.RuntimeCredential, error) {
	if s.value.Secret == "" {
		return s.value, pgx.ErrNoRows
	}
	return s.value, nil
}
func TestChannelAccountValidationPreservesSecrets(t *testing.T) {
	account := accounts.Account{ID: "local", Enabled: true, Channels: []accounts.Channel{{Channel: "Telegram", Enabled: true, Config: map[string]any{}}}}
	saved := savedCredential{accounts.RuntimeCredential{Secret: "123456:abcdefghijklmnopqrstuv"}}
	if err := validateChannelAccount(t.Context(), saved, account, nil); err != nil {
		t.Fatal(err)
	}
	if validateChannelAccount(t.Context(), saved, account, map[string]string{"Telegram": "bad"}) == nil {
		t.Fatal("invalid replacement accepted")
	}
	account.Channels = []accounts.Channel{{Channel: "Email", Enabled: true, Config: map[string]any{"host": "smtp.invalid", "port": float64(587), "from": "sender@example.invalid", "imap_host": "imap.invalid", "username": "local"}}}
	if err := validateChannelAccount(t.Context(), saved, account, nil); err != nil {
		t.Fatal(err)
	}
	account.Channels[0].Config["port"] = float64(65536)
	if validateChannelAccount(t.Context(), saved, account, nil) == nil {
		t.Fatal("invalid port accepted")
	}
	account.Enabled = false
	if validateChannelAccount(t.Context(), saved, account, nil) != nil {
		t.Fatal("disabled draft rejected")
	}
}

func TestMixedChannelSettingsRoundTrip(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	store, err := accounts.New(db, "local-only-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	account := accounts.Account{ID: "mixed-local", Site: "https://core.invalid", Enabled: true, Channels: []accounts.Channel{
		{Channel: "Telegram", Enabled: true, Config: map[string]any{}},
		{Channel: "Email", Enabled: true, Config: map[string]any{"host": "smtp.invalid", "port": float64(587), "from": "Local Sender <sender@example.invalid>", "username": "local", "imap_host": "imap.invalid", "imap_password": "local-imap-secret", "trusted_authserv_id": "mx.invalid", "trusted_dsn_domain": "dsn.invalid"}},
	}}
	secrets := map[string]string{"Telegram": "123456:abcdefghijklmnopqrstuv", "Email": "local-smtp-secret"}
	if err = validateChannelAccount(ctx, store, account, secrets); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, account, secrets); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatal("list saved account", err)
	}
	raw, _ := json.Marshal(listed)
	for _, secret := range []string{secrets["Telegram"], secrets["Email"], "local-imap-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("credential leaked")
		}
	}
	if !strings.Contains(string(raw), "mx.invalid") || !strings.Contains(string(raw), "dsn.invalid") {
		t.Fatal("trust settings missing")
	}
	if err = validateChannelAccount(ctx, store, listed[0], nil); err != nil {
		t.Fatal("saved secret presence", err)
	}
	if err = store.Save(ctx, listed[0], nil); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"Telegram", "Email"} {
		credential, err := store.Credential(ctx, account.ID, channel)
		if err != nil || credential.Secret != secrets[channel] {
			t.Fatal("secret not preserved", err)
		}
		if channel == "Email" && credential.Config["imap_password"] != "local-imap-secret" {
			t.Fatal("IMAP secret not preserved")
		}
	}
	for _, bad := range []any{587.5, float64(0), "bad"} {
		account.Channels[1].Config["port"] = bad
		if validateChannelAccount(ctx, store, account, nil) == nil {
			t.Fatal("invalid port accepted")
		}
	}
}

func TestMixedChannelsRejectConflictingCoreCredentials(t *testing.T) {
	saved := savedCredential{accounts.RuntimeCredential{Secret: "123456:abcdefghijklmnopqrstuv"}}
	config := func(signing string) map[string]any {
		return map[string]any{"host": "smtp.invalid", "port": float64(587), "from": "sender@example.invalid", "core_api_key": "local-key", "core_api_secret": "local-secret", "callback_secret": strings.Repeat(signing, 32)}
	}
	account := accounts.Account{ID: "local", Enabled: true, Channels: []accounts.Channel{
		{Channel: "Telegram", Enabled: true, Config: map[string]any{"core_api_key": "local-key", "core_api_secret": "local-secret", "callback_secret": strings.Repeat("a", 32)}},
		{Channel: "Email", Enabled: true, Config: config("b")},
	}}
	if validateChannelAccount(t.Context(), saved, account, nil) == nil {
		t.Fatal("conflicting shared callback credentials accepted")
	}
	account.Channels[1].Config = config("a")
	if err := validateChannelAccount(t.Context(), saved, account, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDisablingSharedCoreCredentialsRequiresReplacement(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	store, err := accounts.New(db, "local-only-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"Telegram", "Email"} {
		t.Run(owner, func(t *testing.T) {
			core := map[string]any{"core_api_key": "local-key", "core_api_secret": "local-secret", "callback_secret": strings.Repeat("a", 32)}
			account := accounts.Account{ID: "disable-" + strings.ToLower(owner), Site: "https://core.invalid", Enabled: true, Channels: []accounts.Channel{
				{Channel: "Telegram", Enabled: true, Config: map[string]any{}},
				{Channel: "Email", Enabled: true, Config: map[string]any{"host": "smtp.invalid", "port": float64(587), "from": "local@example.invalid"}},
			}}
			for i := range account.Channels {
				if account.Channels[i].Channel == owner {
					for k, v := range core {
						account.Channels[i].Config[k] = v
					}
				}
			}
			if err := store.Save(ctx, account, map[string]string{"Telegram": "123456:abcdefghijklmnopqrstuv", "Email": "local-password"}); err != nil {
				t.Fatal(err)
			}
			for i := range account.Channels {
				if account.Channels[i].Channel == owner {
					account.Channels[i].Enabled = false
				}
				for k := range core {
					delete(account.Channels[i].Config, k)
				} // Same redacted payload returned to the UI.
			}
			if err := validateChannelAccount(ctx, store, account, nil); err == nil || !strings.Contains(err.Error(), "copy all three") {
				t.Fatalf("unsafe save not rejected with action: %v", err)
			}
			// Partial updates must also consider the enabled channel omitted from Save.
			partial := account
			for _, ch := range account.Channels {
				if ch.Channel == owner {
					partial.Channels = []accounts.Channel{ch}
				}
			}
			if err := validateChannelAccount(ctx, store, partial, nil); err == nil {
				t.Fatal("partial save silently disabled shared callbacks")
			}
			disabled := account
			disabled.Enabled = false
			if err := validateChannelAccount(ctx, store, disabled, nil); err != nil {
				t.Fatal("disabling entire account rejected", err)
			}
			bothOff := account
			bothOff.Channels = append([]accounts.Channel(nil), account.Channels...)
			for i := range bothOff.Channels {
				bothOff.Channels[i].Enabled = false
			}
			if err := validateChannelAccount(ctx, store, bothOff, nil); err != nil {
				t.Fatal("disabling both channels rejected", err)
			}
			for i := range account.Channels {
				if account.Channels[i].Enabled {
					for k, v := range core {
						account.Channels[i].Config[k] = v
					}
				}
			}
			if err := validateChannelAccount(ctx, store, account, nil); err != nil {
				t.Fatal("replacement callback credentials rejected", err)
			}
			if err := store.Save(ctx, account, nil); err != nil {
				t.Fatal(err)
			}
			runtime, err := store.Runtime(ctx)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range runtime {
				if row.AccountID == account.ID {
					found = true
					if row.Channel == owner || row.Config["callback_secret"] != core["callback_secret"] {
						t.Fatal("runtime lost shared credentials")
					}
				}
			}
			if !found {
				t.Fatal("remaining channel not active")
			}
			// Omitted disabled channels must stay disabled during later validation.
			account.Channels = account.Channels[:0]
			if err := validateChannelAccount(ctx, store, account, nil); err != nil {
				t.Fatal("omitted saved channels validated incorrectly", err)
			}
		})
	}
}

func TestEmailHostIPv6Validation(t *testing.T) {
	for _, tc := range []struct {
		host  string
		valid bool
	}{
		{"::1", true}, {"2001:db8::25", true}, {"::ffff:192.0.2.1", true}, {"2001:db8:0:1:2:3:4:5", true},
		{"smtp.example.invalid", true}, {"127.0.0.1", true}, {"[::1]", false}, {"[2001:db8::25]", false},
		{"2001:::1", false}, {"1:2:3", false}, {"fe80::1%eth0", false}, {"::ffff:999.0.0.1", false}, {"smtp.invalid:587", false}, {"https://smtp.invalid", false},
	} {
		config := map[string]any{"host": tc.host, "imap_host": tc.host, "port": float64(587), "username": "local", "from": "sender@example.invalid"}
		if err := validateEmailSettings(config, "local-only"); (err == nil) != tc.valid {
			t.Errorf("host %q valid=%v: %v", tc.host, tc.valid, err)
		}
	}
}
