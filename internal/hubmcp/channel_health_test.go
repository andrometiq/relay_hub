package hubmcp

import (
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/containers"
)

// healthKeys are the channel keys of one provider list of a channels.health answer.
func healthKeys(t *testing.T, result map[string]any, provider string) []string {
	t.Helper()
	list, ok := result[provider].([]any)
	if !ok {
		t.Fatalf("no %s list: %v", provider, result)
	}
	keys := []string{}
	for _, item := range list {
		row, _ := item.(map[string]any)
		key, _ := row["channel"].(string)
		keys = append(keys, key)
	}
	return keys
}

func healthRow(result map[string]any, provider, key string) map[string]any {
	list, _ := result[provider].([]any)
	for _, item := range list {
		if row, _ := item.(map[string]any); row["channel"] == key {
			return row
		}
	}
	return nil
}

func TestChannelHealthReadsEveryNumberOfTheSiteInOneCall(t *testing.T) {
	f := newNumberFixture(t)
	adminTurnsOff(t, f.store, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-121"})
	ok, result, body := f.call("site-a", "channels.health", map[string]any{})
	if !ok || strings.Join(healthKeys(t, result, "WhatsApp"), ",") != "wa-120,wa-121" {
		t.Fatalf("site numbers: %s", body)
	}
	if len(healthKeys(t, result, "Telegram")) != 0 || len(healthKeys(t, result, "Email")) != 0 {
		t.Fatalf("bots or mailboxes on a Hub without their tools: %s", body)
	}
	own := healthRow(result, "WhatsApp", "wa-120")
	if own["connection_status"] != "connected" || own["pin_kept"] != true || own["delivery"] == nil || own["admin_off"] != false {
		t.Fatalf("number health shape: %s", body)
	}
	if locked := healthRow(result, "WhatsApp", "wa-121"); locked["admin_off"] != true || locked["enabled"] != false {
		t.Fatalf("admin lock: %s", body)
	}
	f.noSecret(body)
	if ok, result, body = f.call("site-b", "channels.health", map[string]any{"provider": "WhatsApp"}); !ok || strings.Join(healthKeys(t, result, "WhatsApp"), ",") != "wa-130" {
		t.Fatalf("another site's numbers: %s", body)
	}
	if _, listed := result["Telegram"]; listed {
		t.Fatalf("a provider that was not asked for: %s", body)
	}
	if len(f.graphCalls()) != 0 {
		t.Fatalf("health contacted Meta: %v", f.graphCalls())
	}
}

func TestChannelHealthRefusesAnUnknownProviderAndAMissingSiteToken(t *testing.T) {
	f := newNumberFixture(t)
	if ok, result, body := f.call("site-a", "channels.health", map[string]any{"provider": "SMS"}); ok || errorCode(result) != "invalid_request" {
		t.Fatalf("unknown provider: %s", body)
	}
	if ok, _, body := f.callWith("", "channels.health", map[string]any{}); ok || !strings.Contains(body, "unauthorized") {
		t.Fatalf("no site token: %s", body)
	}
	revoked := f.tokens["site-b"]
	if _, err := f.auth.DB.Exec(f.ctx, `UPDATE relay_api_tokens SET revoked_at=now() WHERE site_id='site-b'`); err != nil {
		t.Fatal(err)
	}
	if ok, _, body := f.callWith(revoked, "channels.health", map[string]any{}); ok || !strings.Contains(body, "unauthorized") {
		t.Fatalf("revoked site token: %s", body)
	}
}

func TestChannelHealthReadsTheSiteBotsAndMailboxesWithoutCredentials(t *testing.T) {
	f := newChannelFixture(t)
	_, connected, _ := f.connectBot("site-a", botTokenA)
	bot := connected["channel"].(string)
	mailbox := f.storedMailbox("site-a", "sales@example.com")
	other := f.storedMailbox("site-b", "support@example.com")
	adminTurnsOff(t, f.store, containers.ChannelRef{Channel: "Telegram", Key: bot})
	calls := len(f.calls())
	ok, result, body := f.call("site-a", "channels.health", map[string]any{})
	if !ok || strings.Join(healthKeys(t, result, "Telegram"), ",") != bot || strings.Join(healthKeys(t, result, "Email"), ",") != mailbox || len(healthKeys(t, result, "WhatsApp")) != 0 {
		t.Fatalf("site channels: %s", body)
	}
	if row := healthRow(result, "Telegram", bot); row["username"] != "acme_textiles_bot" || row["token_saved"] != true || row["admin_off"] != true {
		t.Fatalf("bot health: %s", body)
	}
	if row := healthRow(result, "Email", mailbox); row["password_saved"] != true || row["address"] != "sales@example.com" || row["admin_off"] != false {
		t.Fatalf("mailbox health: %s", body)
	}
	f.clean(body)
	if ok, result, body = f.call("site-a", "telegram.bot.health", map[string]any{"channel": bot}); !ok || result["admin_off"] != true {
		t.Fatalf("bot health tool admin lock: %s", body)
	}
	if ok, result, body = f.call("site-b", "channels.health", map[string]any{"provider": "Email"}); !ok || strings.Join(healthKeys(t, result, "Email"), ",") != other {
		t.Fatalf("another site's mailboxes: %s", body)
	}
	f.clean(body)
	if len(f.calls()) != calls {
		t.Fatal("health contacted Telegram")
	}
}
