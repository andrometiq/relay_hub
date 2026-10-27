package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

type directoryClient struct {
	t        *testing.T
	mux      *http.ServeMux
	role     string
	reloads  int
	verified []string
	store    *containers.Store
}

func newDirectoryClient(t *testing.T) *directoryClient {
	ctx, db := adminTestDatabase(t)
	_ = ctx
	store, err := containers.New(db, "directory-admin-key")
	if err != nil {
		t.Fatal(err)
	}
	client := &directoryClient{t: t, mux: http.NewServeMux(), role: "admin", store: store}
	admin := directoryAdmin{store: store, origin: "https://hub.test",
		session: func(*http.Request) (auth.User, error) { return auth.User{ID: 1, Role: client.role}, nil },
		verifyWhatsApp: func(_ context.Context, a whatsapp.Account) error {
			if a.AppSecret == "" || a.AccessToken == "" {
				return errors.New("missing app secret or token")
			}
			client.verified = append(client.verified, a.PhoneID)
			return nil
		},
		reload:          func(context.Context) error { client.reloads++; return nil },
		whatsappAccount: func(string) (whatsapp.Account, bool) { return whatsapp.Account{}, false },
	}
	admin.register(client.mux, func(h http.Handler) http.Handler { return h })
	return client
}

func (c *directoryClient) do(method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
	r.Header.Set("Origin", "https://hub.test")
	r.Header.Set("X-Relay-CSRF", auth.CSRF("session"))
	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, r)
	return w
}

func (c *directoryClient) expect(status int, method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	w := c.do(method, path, body)
	if w.Code != status {
		c.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return w
}

func TestDirectoryAdminBuildsTwoSitesFromOneContainer(t *testing.T) {
	c := newDirectoryClient(t)
	c.expect(201, "POST", "/api/containers", map[string]any{"id": "essdee", "name": "Essdee", "enabled": true})
	c.expect(204, "PUT", "/api/containers/essdee", map[string]any{"name": "Essdee", "enabled": true,
		"whatsapp": map[string]any{"config": map[string]any{"app_id": "77", "graph_version": "v25.0", "app_secret": "app-secret", "verify_token": "verify-token"}, "access_token": "system-user-token"}})
	for i, number := range []string{"101", "102"} {
		c.expect(204, "POST", "/api/containers/essdee/channels", map[string]any{
			"key": []string{"sales", "support"}[i], "channel": "WhatsApp", "label": "Number " + number, "enabled": true,
			"config": map[string]any{"phone_id": number, "waba_id": "201", "display_number": "+91 90000 0" + number}})
	}
	for i, bot := range []string{"500001:bot-one-secret-abcdefghijklmnopqrstu", "500002:bot-two-secret-abcdefghijklmnopqrstu"} {
		c.expect(204, "POST", "/api/containers/essdee/channels", map[string]any{"secret": bot,
			"key": []string{"bot-a", "bot-b"}[i], "channel": "Telegram", "label": "Bot", "enabled": true, "config": map[string]any{"username": "bot_" + []string{"a", "b"}[i]}})
	}
	for _, mailbox := range []string{"sales", "help"} {
		c.expect(204, "POST", "/api/containers/essdee/channels", map[string]any{"secret": "smtp-password",
			"key": "mail-" + mailbox, "channel": "Email", "label": mailbox, "enabled": true,
			"config": map[string]any{"host": "smtp.invalid", "port": float64(587), "username": mailbox, "from": mailbox + "@essdee.invalid"}})
	}
	if strings.Join(c.verified, ",") != "101,102" {
		t.Fatalf("numbers not verified with the container app: %v", c.verified)
	}
	c.expect(204, "PUT", "/api/containers/essdee/channels/WhatsApp/sales", map[string]any{"label": "Sales desk", "enabled": true, "config": map[string]any{"phone_id": "101", "waba_id": "201", "connection_status": "connected"}})
	c.expect(404, "PUT", "/api/containers/essdee/channels/WhatsApp/nobody", map[string]any{"label": "x", "enabled": false})
	core := map[string]any{"core_api_key": "key", "core_api_secret": "secret", "callback_secret": strings.Repeat("s", 32)}
	for _, site := range []string{"site-a", "site-b"} {
		c.expect(204, "POST", "/api/sites", map[string]any{"id": site, "name": "Site " + site, "origin": "https://" + site + ".invalid", "enabled": true, "secrets": core})
	}
	c.expect(204, "PUT", "/api/sites/site-b", map[string]any{"origin": "https://site-b.invalid", "enabled": true, "secrets": map[string]any{"core_api_key": ""}})
	c.expect(204, "PUT", "/api/sites/site-a/channels", map[string]any{"channels": []map[string]string{{"channel": "WhatsApp", "key": "sales"}, {"channel": "Telegram", "key": "bot-a"}, {"channel": "Email", "key": "mail-sales"}}})
	c.expect(204, "PUT", "/api/sites/site-b/channels", map[string]any{"channels": []map[string]string{{"channel": "WhatsApp", "key": "support"}, {"channel": "Telegram", "key": "bot-b"}, {"channel": "Email", "key": "mail-help"}}})
	c.expect(409, "PUT", "/api/sites/site-b/channels", map[string]any{"channels": []map[string]string{{"channel": "WhatsApp", "key": "sales"}}})
	c.expect(404, "PUT", "/api/sites/missing/channels", map[string]any{"channels": []map[string]string{}})

	var site struct {
		containers.Site
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(c.expect(200, "GET", "/api/sites/site-a", nil).Body.Bytes(), &site)
	if len(site.Channels) != 3 || len(site.ConfiguredSecrets) != 3 || site.Name != "Site site-a" || site.CoreAPIKey != "key" || site.LastCallbackAt != nil || site.ActiveTokens != 0 || site.CSRF != auth.CSRF("session") {
		t.Fatalf("site view: %#v", site)
	}
	body := c.expect(200, "GET", "/api/containers/essdee", nil).Body.String()
	for _, secret := range []string{"app-secret", "verify-token", "system-user-token", "bot-one-secret", "smtp-password", strings.Repeat("s", 32)} {
		if strings.Contains(body, secret) {
			t.Fatal("container view leaked a secret")
		}
	}
	var container containers.Container
	json.Unmarshal([]byte(body), &container)
	if !strings.Contains(body, `"csrf":"`+auth.CSRF("session")+`"`) || container.WhatsApp.LastEventAt != nil {
		t.Fatal("container view lacks csrf or shows a last event")
	}
	if len(container.Channels) != 6 || container.WhatsApp.Config["app_id"] != "77" || len(container.WhatsApp.ConfiguredSecrets) != 3 {
		t.Fatalf("container view: %#v", container)
	}
	if !strings.Contains(c.expect(200, "GET", "/api/containers/essdee/whatsapp/webhook", nil).Body.String(), "verify-token") {
		t.Fatal("verify token not shown to the admin")
	}
	if !strings.Contains(c.expect(200, "GET", "/api/sites?query=Site", nil).Body.String(), "site-b") || !strings.Contains(c.expect(200, "GET", "/api/containers?query=ess", nil).Body.String(), "essdee") {
		t.Fatal("list views")
	}
	if c.reloads != 14 {
		t.Fatalf("runtime reloads: %d", c.reloads)
	}
}

func TestDirectoryAdminRejectsBadInput(t *testing.T) {
	c := newDirectoryClient(t)
	c.expect(201, "POST", "/api/containers", map[string]any{"id": "bare", "enabled": true})
	c.expect(400, "POST", "/api/containers/bare/channels", map[string]any{"key": "n1", "channel": "WhatsApp", "enabled": true, "config": map[string]any{"phone_id": "1", "waba_id": "2"}})
	c.expect(400, "POST", "/api/containers/bare/channels", map[string]any{"key": "n1", "channel": "WhatsApp", "enabled": false, "config": map[string]any{"graph_version": "v25.0"}})
	c.expect(404, "POST", "/api/containers/missing/channels", map[string]any{"key": "x", "channel": "Telegram"})
	c.expect(404, "PUT", "/api/containers/missing", map[string]any{"name": "x"})
	c.expect(400, "POST", "/api/containers", map[string]any{"id": "bare"})
	c.expect(400, "POST", "/api/sites", map[string]any{"id": "s", "origin": "ftp://x"})
	c.expect(400, "POST", "/api/sites", map[string]any{"id": "s", "origin": "https://s.invalid", "secrets": map[string]any{"core_api_key": "only"}})
	c.role = "member"
	c.expect(403, "GET", "/api/containers", nil)
	c.role = "admin"
	r := httptest.NewRequest("POST", "/api/sites", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("change without CSRF accepted")
	}
}

func TestDirectoryAdminKeepsCheckStatusAndClearsNumberApp(t *testing.T) {
	c := newDirectoryClient(t)
	ctx := context.Background()
	c.expect(201, "POST", "/api/containers", map[string]any{"id": "essdee", "enabled": true,
		"whatsapp": map[string]any{"config": map[string]any{"app_id": "77", "graph_version": "v25.0", "app_secret": "app-secret", "verify_token": "verify-token"}, "access_token": "system-user-token"}})
	c.expect(204, "POST", "/api/containers/essdee/channels", map[string]any{"secret": "500001:bot-one-secret-abcdefghijklmnopqrstu",
		"key": "bot", "channel": "Telegram", "label": "Bot", "enabled": true, "config": map[string]any{"username": "bot"}})
	if err := c.store.SetStatus(ctx, containers.ChannelRef{Channel: "Telegram", Key: "bot"}, map[string]any{"webhook_status": "registered"}); err != nil {
		t.Fatal(err)
	}
	if c.store.SetStatus(ctx, containers.ChannelRef{Channel: "Telegram", Key: "bot"}, map[string]any{"username": "x"}) == nil {
		t.Fatal("status write changed a setting")
	}
	c.expect(204, "PUT", "/api/containers/essdee/channels/Telegram/bot", map[string]any{"label": "Bot", "enabled": true, "config": map[string]any{"username": "bot", "webhook_status": "forged"}})
	bot, err := c.store.GetChannel(ctx, containers.ChannelRef{Channel: "Telegram", Key: "bot"})
	if err != nil || bot.Config["webhook_status"] != "registered" {
		t.Fatalf("check status lost on save: %v %v", bot.Config, err)
	}

	number := map[string]any{"key": "sales", "channel": "WhatsApp", "label": "Sales", "enabled": true,
		"config": map[string]any{"phone_id": "101", "waba_id": "201", "app_secret": "own-secret", "verify_token": "own-verify"}}
	c.expect(204, "POST", "/api/containers/essdee/channels", number)
	sales, _ := c.store.GetChannel(ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "sales"})
	if strings.Join(sales.ConfiguredSecrets, ",") != "app_secret,verify_token" {
		t.Fatalf("number override: %v", sales.ConfiguredSecrets)
	}
	number["use_container_app"] = true
	c.expect(204, "PUT", "/api/containers/essdee/channels/WhatsApp/sales", number)
	sales, _ = c.store.GetChannel(ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "sales"})
	if len(sales.ConfiguredSecrets) != 0 || sales.Config["connection_status"] != "connected" {
		t.Fatalf("number still has its own app: %v %v", sales.ConfiguredSecrets, sales.Config)
	}
}

func TestNewContainersGetRandomPublicWebhookPaths(t *testing.T) {
	c := newDirectoryClient(t)
	var created struct{ ID, WebhookPath string }
	var body map[string]string
	json.Unmarshal(c.expect(201, "POST", "/api/containers", map[string]any{"name": "Essdee Retail", "enabled": true}).Body.Bytes(), &body)
	created.ID, created.WebhookPath = body["id"], body["webhook_path"]
	path := strings.TrimPrefix(created.WebhookPath, "/webhooks/whatsapp/")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(path) || strings.Contains(strings.ToLower(created.WebhookPath), "essdee") || strings.Contains(created.WebhookPath, created.ID) {
		t.Fatalf("public path is not random: %v", body)
	}
	c.expect(201, "POST", "/api/containers", map[string]any{"name": "Essdee Retail", "enabled": true})
	webhook := c.expect(200, "GET", "/api/containers/"+created.ID+"/whatsapp/webhook", nil).Body.String()
	if !strings.Contains(webhook, created.WebhookPath) {
		t.Fatalf("webhook view: %s", webhook)
	}
	c.expect(204, "POST", "/api/containers/"+created.ID+"/channels", map[string]any{"secret": "500001:bot-one-secret-abcdefghijklmnopqrstu",
		"key": "retail-bot", "channel": "Telegram", "label": "Retail bot", "enabled": true, "config": map[string]any{"username": "retail_bot"}})
	bot, err := c.store.GetChannel(context.Background(), containers.ChannelRef{Channel: "Telegram", Key: "retail-bot"})
	if err != nil || !regexp.MustCompile(`^/webhooks/telegram/[a-f0-9]{64}$`).MatchString(bot.WebhookPath) || strings.Contains(bot.WebhookPath, "retail") {
		t.Fatalf("bot path: %q %v", bot.WebhookPath, err)
	}
	rows, err := c.store.Runtime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := channels.Load(rows)
	if err != nil {
		t.Fatal(err)
	}
	if key, ok := registry.WebhookKey("Telegram", strings.TrimPrefix(bot.WebhookPath, "/webhooks/telegram/")); !ok || key != "retail-bot" {
		t.Fatal("public path does not reach the bot")
	}
	if _, ok := registry.WebhookKey("Telegram", "retail-bot"); ok {
		t.Fatal("a new bot answered on its readable key")
	}
}

func TestTurnOffOldAddressStopsTheReadablePath(t *testing.T) {
	c := newDirectoryClient(t)
	ctx := context.Background()
	var body map[string]string
	json.Unmarshal(c.expect(201, "POST", "/api/containers", map[string]any{"name": "Old", "enabled": true}).Body.Bytes(), &body)
	id := body["id"]
	c.expect(204, "POST", "/api/containers/"+id+"/channels", map[string]any{"secret": "500002:bot-two-secret-abcdefghijklmnopqrstu",
		"key": "old-bot", "channel": "Telegram", "label": "Old bot", "enabled": true, "config": map[string]any{"username": "old_bot"}})
	if _, err := c.store.DB.Exec(ctx, `UPDATE relay_containers SET legacy_path=true WHERE container_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.DB.Exec(ctx, `UPDATE relay_channel_credentials SET legacy_path=true WHERE channel_key='old-bot'`); err != nil {
		t.Fatal(err)
	}
	answers := func() bool {
		rows, err := c.store.Runtime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := channels.Load(rows)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := registry.WebhookKey("Telegram", "old-bot")
		return ok
	}
	container, _ := c.store.GetContainer(ctx, id)
	bot, _ := c.store.GetChannel(ctx, containers.ChannelRef{Channel: "Telegram", Key: "old-bot"})
	if !container.OldAddress || !bot.OldAddress || !answers() {
		t.Fatal("old address is not on before the action")
	}

	reloads := c.reloads
	c.expect(204, "DELETE", "/api/containers/"+id+"/channels/Telegram/old-bot/old-address", nil)
	c.expect(204, "DELETE", "/api/containers/"+id+"/old-address", nil)
	container, _ = c.store.GetContainer(ctx, id)
	bot, _ = c.store.GetChannel(ctx, containers.ChannelRef{Channel: "Telegram", Key: "old-bot"})
	if container.OldAddress || bot.OldAddress || answers() || c.reloads != reloads+2 {
		t.Fatalf("old address still on: container=%v bot=%v reloads=%d", container.OldAddress, bot.OldAddress, c.reloads-reloads)
	}
	c.expect(404, "DELETE", "/api/containers/missing/old-address", nil)
	c.role = "viewer"
	c.expect(403, "DELETE", "/api/containers/"+id+"/old-address", nil)
}
