package hubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/sitechannels"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

// Fake bot tokens: two bots, and a new token of the first bot.
const (
	botTokenA    = "111111:acme_bot_token_local_only_aaaa"
	botTokenB    = "222222:acme_bot_token_local_only_bbbb"
	botTokenA2   = "111111:acme_bot_new_token_local_only_a2"
	botTokenGone = "333333:revoked_bot_token_local_only_cc"
	telegramMain = "telegram-master-secret-local-only-0123456789"
)

var fakeBots = map[string]string{botTokenA: "acme_textiles_bot", botTokenB: "acme_sales_bot", botTokenA2: "acme_textiles_bot"}

// channelFixture has site-a and site-b with real site tokens, a signup
// container, a fake Telegram that records each call, and a runtime reload
// that builds the registry from the store as the Hub does.
type channelFixture struct {
	*numberFixture
	registry *channels.Registry
	telegram *http.Client
	mu       sync.Mutex
	methods  []string
	hooks    map[string]string
	// mail builds the mailbox client of a connect; tests replace it.
	mail func(relayemail.Config) (*relayemail.Client, error)
}

func (f *channelFixture) Telegram(token string) (*telegram.Client, error) {
	return telegram.NewWithHTTPClient(telegram.Config{AccountID: "connect", BotToken: token, Enabled: true}, f.telegram)
}

func (f *channelFixture) Email(cfg relayemail.Config) (*relayemail.Client, error) { return f.mail(cfg) }

func newChannelFixture(t *testing.T) *channelFixture {
	t.Helper()
	ctx := context.Background()
	db, _ := simulationDatabase(t, ctx)
	f := &channelFixture{numberFixture: &numberFixture{t: t, ctx: ctx, auth: &auth.Store{DB: db}, tokens: map[string]string{}}, registry: &channels.Registry{}, hooks: map[string]string{}}
	f.store, _ = containers.New(db, "mcp-channel-test-key")
	if err := f.store.SaveContainer(ctx, containers.Container{ID: "signup", Enabled: true, Signup: true}, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin,enabled) VALUES('site-a','https://a.example.com',true),('site-b','https://b.example.com',true)`); err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"site-a", "site-b"} {
		f.tokens[site] = f.siteToken(site)
	}
	server := httptest.NewServer(http.HandlerFunc(f.bot))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	f.telegram = &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host, copy.Host = endpoint.Scheme, endpoint.Host, endpoint.Host
		return http.DefaultTransport.RoundTrip(copy)
	})}
	f.mail = func(relayemail.Config) (*relayemail.Client, error) { t.Fatal("no mailbox expected"); return nil, nil }
	service := &sitechannels.Service{Store: f.store, Registry: f.registry, Clients: f, Reload: f.reloadRuntime, Hosts: relayemail.PublicHosts{Allow: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}},
		PublicURL: "https://hub.example.com", TelegramMaster: telegramMain}
	var err error
	f.handler, err = NewAuthenticatedChannelsWithOptions(db.Ping, nil, f.auth.Authenticate, f.registry, channels.Store{DB: db}, Options{Channels: service})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// reloadRuntime loads the stored channels with clients that reach the fakes.
func (f *channelFixture) reloadRuntime(ctx context.Context) error {
	f.reloads++
	rows, err := f.store.Runtime(ctx)
	if err != nil {
		return err
	}
	next, err := channels.Load(rows)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Channel == "Telegram" {
			if next.Telegram[row.Key], err = f.Telegram(row.Secret); err != nil {
				return err
			}
		}
	}
	return f.registry.Replace(next)
}

func (f *channelFixture) bot(w http.ResponseWriter, r *http.Request) {
	token, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	var body map[string]any
	json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	username, known := fakeBots[token]
	f.methods = append(f.methods, method)
	w.Header().Set("Content-Type", "application/json")
	if !known {
		w.WriteHeader(401)
		io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	var result any = true
	switch method {
	case "getMe":
		id, _, _ := strings.Cut(token, ":")
		result = map[string]any{"id": id, "is_bot": true, "username": username}
	case "setWebhook":
		f.hooks[username], _ = body["url"].(string)
	case "deleteWebhook":
		delete(f.hooks, username)
	case "getWebhookInfo":
		result = map[string]any{"url": f.hooks[username], "pending_update_count": 0}
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *channelFixture) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

// clean fails when a tool response carries a token, a password or a webhook secret.
func (f *channelFixture) clean(body string) {
	f.t.Helper()
	for _, secret := range []string{botTokenA, botTokenB, botTokenA2, "acme_bot", telegramMain, "smtp-local-only", "imap-local-only"} {
		if strings.Contains(body, secret) {
			f.t.Fatalf("response carries a credential: %s", body)
		}
	}
	if secret, err := f.registry.TelegramWebhookSecret(f.firstBot(), telegramMain); err == nil && strings.Contains(body, secret) {
		f.t.Fatalf("response carries the webhook secret: %s", body)
	}
}

func (f *channelFixture) firstBot() string {
	var key string
	f.store.DB.QueryRow(f.ctx, `SELECT channel_key FROM relay_channel_credentials WHERE channel='Telegram' ORDER BY id LIMIT 1`).Scan(&key)
	return key
}

func (f *channelFixture) connectBot(site, token string) (bool, map[string]any, string) {
	ok, result, body := f.call(site, "telegram.bot.connect", map[string]any{"bot_token": token, "label": "Acme Textiles"})
	f.clean(body)
	return ok, result, body
}

func TestBotConnectChecksStoresLinksAndRegistersTheWebhook(t *testing.T) {
	f := newChannelFixture(t)
	ok, result, body := f.connectBot("site-a", botTokenA)
	key, _ := result["channel"].(string)
	if !ok || !strings.HasPrefix(key, "tg-") || result["username"] != "acme_textiles_bot" || result["bot_id"] != "111111" || result["webhook_status"] != "registered" || result["token_saved"] != true {
		t.Fatalf("connect: %s", body)
	}
	if got := strings.Join(f.calls(), ","); got != "getMe,getMe,setWebhook,getWebhookInfo" {
		t.Fatalf("Telegram calls: %s", got)
	}
	if !strings.HasPrefix(f.hooks["acme_textiles_bot"], "https://hub.example.com/webhooks/telegram/") || strings.Contains(f.hooks["acme_textiles_bot"], key) {
		t.Fatalf("webhook address: %s", f.hooks["acme_textiles_bot"])
	}
	if f.notices("site-a") == 0 {
		t.Fatal("Core was not told about the new bot")
	}
	ok, result, body = f.call("site-a", "channels.list", map[string]any{})
	if listed, _ := result["channels"].([]any); !ok || len(listed) != 1 {
		t.Fatalf("channel list: %s", body)
	}
	f.clean(body)
	for _, site := range []string{"site-a", "site-b"} {
		if ok, result, body = f.connectBot(site, botTokenA); ok || errorCode(result) != "in_use" || strings.Contains(body, "site-a") {
			t.Fatalf("%s connected a used bot: %s", site, body)
		}
	}
	var audits int
	f.store.DB.QueryRow(f.ctx, `SELECT count(*) FROM relay_operational_audit WHERE account_id='site-a' AND action='telegram.bot.connect' AND outcome='completed'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("connect audit rows: %d", audits)
	}
}

func TestBotConnectWithABadTokenStoresNothing(t *testing.T) {
	f := newChannelFixture(t)
	for _, token := range []string{botTokenGone, "not-a-token"} {
		if ok, result, body := f.connectBot("site-a", token); ok || errorCode(result) != "invalid_token" {
			t.Fatalf("bad token %q: %s", token, body)
		}
	}
	var stored int
	f.store.DB.QueryRow(f.ctx, `SELECT count(*) FROM relay_channel_credentials`).Scan(&stored)
	if stored != 0 || f.reloads != 0 {
		t.Fatalf("a refused token stored %d channels", stored)
	}
}

func TestBotToolsAreScopedToTheTokenSite(t *testing.T) {
	f := newChannelFixture(t)
	_, result, _ := f.connectBot("site-a", botTokenA)
	key := result["channel"].(string)
	if ok, result, body := f.call("site-a", "telegram.bot.health", map[string]any{}); !ok || result["channel"] != key {
		t.Fatalf("own health: %s", body)
	}
	calls := len(f.calls())
	for tool, arguments := range map[string]map[string]any{"telegram.bot.health": {"channel": key}, "telegram.bot.check": {"channel": key, "repair": true},
		"telegram.bot.settings": {"channel": key, "label": "Taken"}, "telegram.bot.remove": {"channel": key, "confirm": true}} {
		ok, result, body := f.call("site-b", tool, arguments)
		if ok || errorCode(result) != "not_found" {
			t.Fatalf("%s on another site's bot: %s", tool, body)
		}
		f.clean(body)
	}
	if ok, result, body := f.call("site-b", "telegram.bot.health", map[string]any{}); ok || errorCode(result) != "no_bot" {
		t.Fatalf("site without a bot: %s", body)
	}
	if len(f.calls()) != calls {
		t.Fatal("a refused call reached Telegram")
	}
	if ok, _, body := f.callWith("", "telegram.bot.health", map[string]any{}); ok || !strings.Contains(body, "unauthorized") {
		t.Fatalf("no site token: %s", body)
	}
}

func TestBotCheckRepairsTheWebhookAndSettingsKeepTheSameBot(t *testing.T) {
	f := newChannelFixture(t)
	_, result, _ := f.connectBot("site-a", botTokenA)
	key := result["channel"].(string)
	f.mu.Lock()
	delete(f.hooks, "acme_textiles_bot")
	f.mu.Unlock()
	if ok, result, body := f.call("site-a", "telegram.bot.check", map[string]any{"channel": key}); !ok || result["webhook_status"] != "not_registered" {
		t.Fatalf("check without repair: %s", body)
	}
	if ok, result, body := f.call("site-a", "telegram.bot.check", map[string]any{"channel": key, "repair": true}); !ok || result["webhook_status"] != "registered" {
		t.Fatalf("repair: %s", body)
	}
	if ok, result, body := f.call("site-a", "telegram.bot.settings", map[string]any{"channel": key, "bot_token": botTokenB}); ok || errorCode(result) != "bot_changed" {
		t.Fatalf("token of another bot: %s", body)
	}
	ok, result, body := f.call("site-a", "telegram.bot.settings", map[string]any{"channel": key, "bot_token": botTokenA2, "label": "Acme Sales"})
	if !ok || result["label"] != "Acme Sales" || result["webhook_status"] != "registered" {
		t.Fatalf("new token of the same bot: %s", body)
	}
	f.clean(body)
	credential, err := f.store.Credential(f.ctx, "Telegram", key)
	if err != nil || credential.Secret != botTokenA2 {
		t.Fatal("the new token was not stored")
	}
	if ok, result, body = f.call("site-a", "telegram.bot.settings", map[string]any{"channel": key, "enabled": false}); !ok || result["enabled"] != false {
		t.Fatalf("turn off: %s", body)
	}
	if ok, result, body = f.call("site-a", "telegram.bot.check", map[string]any{"channel": key}); ok || errorCode(result) != "channel_disabled" {
		t.Fatalf("check of a bot that is off: %s", body)
	}
	if ok, result, body = f.call("site-a", "telegram.bot.settings", map[string]any{"channel": key}); ok || errorCode(result) != "invalid_request" {
		t.Fatalf("no change: %s", body)
	}
}

func TestBotRemoveWaitsForSendsWipesTheTokenAndKeepsTheKey(t *testing.T) {
	f := newChannelFixture(t)
	_, result, _ := f.connectBot("site-a", botTokenA)
	key := result["channel"].(string)
	if ok, result, body := f.call("site-a", "telegram.bot.remove", map[string]any{"channel": key}); ok || errorCode(result) != "confirm_required" {
		t.Fatalf("remove without confirm: %s", body)
	}
	if _, err := f.store.DB.Exec(f.ctx, `INSERT INTO relay_channel_outbox(site_id,account,operation,request_key,payload,payload_hash,state) VALUES('site-a',$1,'telegram.message','k1','{}','h1','queued')`, key); err != nil {
		t.Fatal(err)
	}
	if ok, result, body := f.call("site-a", "telegram.bot.remove", map[string]any{"channel": key, "confirm": true}); ok || errorCode(result) != "pending_sends" {
		t.Fatalf("remove with an open send: %s", body)
	}
	f.store.DB.Exec(f.ctx, `UPDATE relay_channel_outbox SET state='submitted'`)
	ok, result, body := f.call("site-a", "telegram.bot.remove", map[string]any{"channel": key, "confirm": true})
	if !ok || result["removed"] != true || result["webhook_deleted"] != true || f.hooks["acme_textiles_bot"] != "" {
		t.Fatalf("remove: %s", body)
	}
	removed, err := f.store.GetChannel(f.ctx, containers.ChannelRef{Channel: "Telegram", Key: key})
	if err != nil || removed.Site != "" || removed.Enabled || len(removed.ConfiguredSecrets) != 0 {
		t.Fatalf("removed bot: %+v %v", removed, err)
	}
	if len(f.registry.SiteKeys("Telegram", "site-a")) != 0 {
		t.Fatal("the runtime still uses the removed bot")
	}
	if ok, result, body = f.connectBot("site-a", botTokenA); !ok || result["channel"] != key {
		t.Fatalf("connect again: %s", body)
	}
	if ok, _, body = f.call("site-a", "telegram.bot.remove", map[string]any{"channel": key, "confirm": true}); !ok {
		t.Fatalf("remove again: %s", body)
	}
	if ok, result, body = f.connectBot("site-b", botTokenA); !ok || result["channel"] == key {
		t.Fatalf("another site got the old key and its history: %s", body)
	}
}

func TestBotConnectIsRateLimited(t *testing.T) {
	f := newChannelFixture(t)
	for range 10 {
		f.connectBot("site-a", botTokenGone)
	}
	if ok, result, body := f.connectBot("site-a", botTokenA); ok || errorCode(result) != "rate_limited" {
		t.Fatalf("eleventh try: %s", body)
	}
	if ok, _, body := f.connectBot("site-b", botTokenA); !ok {
		t.Fatalf("another site has its own limit: %s", body)
	}
}

// mailbox is a connect request on the documentation network, which the fixture allows.
func mailbox() map[string]any {
	return map[string]any{"from": "sales@example.com", "label": "Acme Textiles sales", "host": "192.0.2.10", "port": 587, "username": "sales@example.com",
		"password": "smtp-local-only", "imap_host": "192.0.2.10", "imap_port": 993, "imap_password": "imap-local-only"}
}

// refusedMail is a mail server that is never reached.
func refusedMail(cfg relayemail.Config) (*relayemail.Client, error) {
	return relayemail.NewWithDialer(cfg, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") })
}

func TestMailboxConnectStoresNothingUntilTheSignInWorks(t *testing.T) {
	f := newChannelFixture(t)
	dialed := 0
	f.mail = func(cfg relayemail.Config) (*relayemail.Client, error) { dialed++; return refusedMail(cfg) }
	cases := map[string]func(map[string]any){
		"login_failed":     func(map[string]any) {},
		"host_refused":     func(in map[string]any) { in["host"] = "127.0.0.1" },
		"port_refused":     func(in map[string]any) { in["port"] = 22 },
		"invalid_settings": func(in map[string]any) { in["from"] = "Acme <sales@example.com>" },
	}
	for code, change := range cases {
		in := mailbox()
		change(in)
		ok, result, body := f.call("site-a", "email.mailbox.connect", in)
		if ok || errorCode(result) != code {
			t.Fatalf("%s: %s", code, body)
		}
		f.clean(body)
	}
	metadata := mailbox()
	metadata["imap_host"] = "169.254.169.254"
	if ok, result, body := f.call("site-a", "email.mailbox.connect", metadata); ok || errorCode(result) != "host_refused" {
		t.Fatalf("metadata IMAP host: %s", body)
	}
	var count int
	f.store.DB.QueryRow(f.ctx, `SELECT count(*) FROM relay_channel_credentials`).Scan(&count)
	if count != 0 || dialed != 1 || f.reloads != 0 {
		t.Fatalf("refused connects stored %d mailboxes after %d sign-ins", count, dialed)
	}
}

// storedMailbox adds a mailbox of site the way a connect stores it.
func (f *channelFixture) storedMailbox(site, from string) string {
	config := map[string]any{"from": from, "host": "192.0.2.10", "port": 587, "username": from, "imap_host": "192.0.2.10", "imap_port": 993,
		"imap_password": "imap-local-only", "connection_status": "connected", "smtp_status": "connected", "imap_status": "connected"}
	key, err := f.store.SaveSiteChannel(f.ctx, site, containers.Channel{Channel: "Email", Label: "Acme Textiles", Config: config}, "smtp-local-only")
	if err != nil {
		f.t.Fatal(err)
	}
	return key
}

func TestMailboxToolsAreScopedToTheTokenSiteAndShowNoPassword(t *testing.T) {
	f := newChannelFixture(t)
	key := f.storedMailbox("site-a", "sales@example.com")
	ok, result, body := f.call("site-a", "email.mailbox.health", map[string]any{})
	server, _ := result["server"].(map[string]any)
	if !ok || result["channel"] != key || result["address"] != "sales@example.com" || result["password_saved"] != true || result["imap_password_saved"] != true || server["imap_folder"] != nil || server["host"] != "192.0.2.10" {
		t.Fatalf("own health: %s", body)
	}
	f.clean(body)
	for tool, arguments := range map[string]map[string]any{"email.mailbox.health": {"channel": key}, "email.mailbox.check": {"channel": key},
		"email.mailbox.settings": {"channel": key, "label": "Taken"}, "email.mailbox.remove": {"channel": key, "confirm": true}} {
		if ok, result, body := f.call("site-b", tool, arguments); ok || errorCode(result) != "not_found" {
			t.Fatalf("%s on another site's mailbox: %s", tool, body)
		}
	}
	if ok, result, body := f.call("site-b", "email.mailbox.health", map[string]any{}); ok || errorCode(result) != "no_mailbox" {
		t.Fatalf("site without a mailbox: %s", body)
	}
	in := mailbox()
	in["from"] = "SALES@example.com"
	f.mail = refusedMail
	if ok, result, body := f.call("site-b", "email.mailbox.connect", in); ok || errorCode(result) != "login_failed" {
		t.Fatalf("connect of a used mailbox with a wrong login: %s", body)
	}
}

func TestMailboxSettingsTestNewSignInAndRemoveWipesBothPasswords(t *testing.T) {
	f := newChannelFixture(t)
	key := f.storedMailbox("site-a", "sales@example.com")
	f.reloadRuntime(f.ctx)
	ok, result, body := f.call("site-a", "email.mailbox.settings", map[string]any{"channel": key, "label": "Acme Sales", "enabled": false})
	if !ok || result["label"] != "Acme Sales" || result["enabled"] != false {
		t.Fatalf("rename and turn off: %s", body)
	}
	f.mail = refusedMail
	if ok, result, body = f.call("site-a", "email.mailbox.settings", map[string]any{"channel": key, "imap_folder": "Support", "password": "new-local-only"}); ok || errorCode(result) != "login_failed" {
		t.Fatalf("a new sign-in that fails: %s", body)
	}
	credential, err := f.store.Credential(f.ctx, "Email", key)
	if err != nil || credential.Secret != "smtp-local-only" || credential.Config["imap_folder"] != nil {
		t.Fatal("a failed change was stored")
	}
	if ok, result, body = f.call("site-a", "email.mailbox.settings", map[string]any{"channel": key, "host": "10.0.0.5"}); ok || errorCode(result) != "host_refused" {
		t.Fatalf("private host: %s", body)
	}
	if ok, result, body = f.call("site-a", "email.mailbox.remove", map[string]any{"channel": key, "confirm": true}); !ok || result["removed"] != true {
		t.Fatalf("remove: %s", body)
	}
	removed, err := f.store.GetChannel(f.ctx, containers.ChannelRef{Channel: "Email", Key: key})
	if err != nil || removed.Site != "" || len(removed.ConfiguredSecrets) != 0 || removed.Config["connection_status"] != "disabled" {
		t.Fatalf("removed mailbox: %+v %v", removed, err)
	}
	if len(f.registry.SiteKeys("Email", "site-a")) != 0 {
		t.Fatal("the runtime still reads the removed mailbox")
	}
}
