package hubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/numbers"
	"github.com/andrometiq/relay_hub/internal/onboarding"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

// numberFixture has site-a with two numbers and site-b with one, real site
// tokens and a fake Graph API that records each call.
type numberFixture struct {
	t       *testing.T
	ctx     context.Context
	store   *containers.Store
	auth    *auth.Store
	handler http.Handler
	tokens  map[string]string
	mu      sync.Mutex
	calls   []string
	bearers []string
	bodies  map[string]map[string]any
	// reply answers one Graph call by "METHOD path"; nil is a plain success.
	reply   func(call string) (int, string)
	reloads int
	// reloadErr makes the runtime reload fail.
	reloadErr error
}

const numberSecrets = "biz-token-a1"

func newNumberFixture(t *testing.T) *numberFixture {
	t.Helper()
	ctx := context.Background()
	db, _ := simulationDatabase(t, ctx)
	f := &numberFixture{t: t, ctx: ctx, auth: &auth.Store{DB: db}, tokens: map[string]string{}, bodies: map[string]map[string]any{}}
	f.store, _ = containers.New(db, "mcp-number-test-key")
	app := &containers.ProviderInput{Config: map[string]any{"app_id": "333", "graph_version": "v25.0", "es_config_id": "es-1", "app_secret": "app-secret-value", "verify_token": "verify-value"}}
	if err := f.store.SaveContainer(ctx, containers.Container{ID: "signup", Enabled: true, Signup: true}, app, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin,enabled) VALUES('site-a','https://a.example.com',true),('site-b','https://b.example.com',true)`); err != nil {
		t.Fatal(err)
	}
	for _, n := range []struct{ key, site, phone, waba, token string }{
		{"wa-120", "site-a", "120", "230", numberSecrets}, {"wa-121", "site-a", "121", "231", "biz-token-a2"}, {"wa-130", "site-b", "130", "240", "biz-token-b"}} {
		config := map[string]any{"phone_id": n.phone, "waba_id": n.waba, "display_number": "+91 99999 99" + n.phone, "templates": true, "sync_metadata": true,
			"partner_status": "active", "connection_status": "connected", "pin": "123456"}
		if err := f.store.SaveChannel(ctx, containers.Channel{Key: n.key, Channel: "WhatsApp", Container: "signup", Label: "Acme Textiles", Enabled: true, Config: config}, n.token, true); err != nil {
			t.Fatal(err)
		}
		if err := f.store.LinkChannel(ctx, n.site, containers.ChannelRef{Channel: "WhatsApp", Key: n.key}); err != nil {
			t.Fatal(err)
		}
		f.tokens[n.site] = f.siteToken(n.site)
	}
	graph := httptest.NewServer(http.HandlerFunc(f.graph))
	t.Cleanup(graph.Close)
	endpoint, _ := url.Parse(graph.URL)
	wa, err := whatsapp.NewWithHTTPClient(nil, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host, copy.Host = endpoint.Scheme, endpoint.Host, endpoint.Host
		return http.DefaultTransport.RoundTrip(copy)
	})})
	if err != nil {
		t.Fatal(err)
	}
	reload := func(context.Context) error { f.reloads++; return f.reloadErr }
	service := &numbers.Service{Store: f.store, WhatsApp: wa, Reload: reload}
	signup, err := onboarding.New(onboarding.Config{DB: db, Store: f.store, Auth: f.auth, PublicOrigin: "https://hub.example.com", Reload: reload})
	if err != nil {
		t.Fatal(err)
	}
	f.handler, err = NewAuthenticatedChannelsWithOptions(db.Ping, wa, f.auth.Authenticate, &channels.Registry{}, channels.Store{DB: db}, Options{Numbers: service, Signup: signup})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// siteToken issues the site's service token the way enrolment does.
func (f *numberFixture) siteToken(site string) string {
	tx, err := f.auth.DB.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	user, err := auth.ServiceUser(f.ctx, tx, site)
	if err != nil {
		f.t.Fatal(err)
	}
	token, err := auth.RotateServiceToken(f.ctx, tx, user, site)
	if err != nil || tx.Commit(f.ctx) != nil {
		f.t.Fatal(err)
	}
	return token
}

func (f *numberFixture) graph(w http.ResponseWriter, r *http.Request) {
	call := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/v25.0/")
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.bearers = append(f.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	var body map[string]any
	if json.Unmarshal(raw, &body) == nil {
		f.bodies[call] = body
	}
	reply := f.reply
	f.mu.Unlock()
	status, text := 200, `{"success":true}`
	if reply != nil {
		if code, answer := reply(call); answer != "" {
			status, text = code, answer
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, text)
}

func (f *numberFixture) graphCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// call runs one tool with the site's token, or with a raw bearer value when token is set.
func (f *numberFixture) call(site, name string, arguments map[string]any) (bool, map[string]any, string) {
	return f.callWith(f.tokens[site], name, arguments)
}

func (f *numberFixture) callWith(token, name string, arguments map[string]any) (bool, map[string]any, string) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	request := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(string(raw)))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != 200 {
		return false, nil, response.Body.String()
	}
	var envelope struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	ok := json.Unmarshal(response.Body.Bytes(), &envelope) == nil && !envelope.Result.IsError
	return ok, envelope.Result.Structured, response.Body.String()
}

// noSecret fails when a tool response carries a stored credential.
func (f *numberFixture) noSecret(body string) {
	f.t.Helper()
	for _, secret := range []string{numberSecrets, "biz-token-a2", "biz-token-b", "123456", "app-secret-value", "verify-value"} {
		if strings.Contains(body, secret) {
			f.t.Fatalf("response carries a credential: %s", body)
		}
	}
}

func errorCode(result map[string]any) string {
	failure, _ := result["error"].(map[string]any)
	code, _ := failure["code"].(string)
	return code
}

func TestNumberHealthIsScopedToTheTokenSite(t *testing.T) {
	f := newNumberFixture(t)
	ok, result, body := f.call("site-a", "whatsapp.number.health", map[string]any{"channel": "wa-120"})
	if !ok || result["channel"] != "wa-120" || result["connection_status"] != "connected" || result["partner_status"] != "active" || result["pin_kept"] != true {
		t.Fatalf("own number health: %s", body)
	}
	if settings, _ := result["settings"].(map[string]any); settings["templates"] != true || result["delivery"] == nil {
		t.Fatalf("health shape: %s", body)
	}
	f.noSecret(body)
	if ok, result, body = f.call("site-a", "whatsapp.number.health", map[string]any{"channel": "wa-130"}); ok || errorCode(result) != "not_found" {
		t.Fatalf("another site's number: %s", body)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.health", map[string]any{}); ok || errorCode(result) != "channel_required" {
		t.Fatalf("two numbers without a key: %s", body)
	}
	if ok, result, body = f.call("site-b", "whatsapp.number.health", map[string]any{}); !ok || result["channel"] != "wa-130" {
		t.Fatalf("the only number: %s", body)
	}
	ok, result, body = f.call("site-a", "channels.list", map[string]any{})
	first, _ := result["channels"].([]any)[0].(map[string]any)
	if !ok || first["connection_status"] != "connected" || first["partner_status"] != "active" {
		t.Fatalf("channel list status: %s", body)
	}
	f.noSecret(body)
	if len(f.graphCalls()) != 0 {
		t.Fatalf("health contacted Meta: %v", f.graphCalls())
	}
}

func TestNumberToolsRefuseAMissingOrRevokedSiteLink(t *testing.T) {
	f := newNumberFixture(t)
	if ok, _, body := f.callWith("", "whatsapp.number.health", map[string]any{}); ok || !strings.Contains(body, "unauthorized") {
		t.Fatalf("no site link: %s", body)
	}
	if ok, _, body := f.callWith("relay_"+strings.Repeat("0", 64), "whatsapp.number.health", map[string]any{}); ok || !strings.Contains(body, "unauthorized") {
		t.Fatalf("unknown site link: %s", body)
	}
	revoked := f.tokens["site-b"]
	if _, err := f.auth.DB.Exec(f.ctx, `UPDATE relay_api_tokens SET revoked_at=now() WHERE site_id='site-b'`); err != nil {
		t.Fatal(err)
	}
	if ok, _, body := f.callWith(revoked, "whatsapp.number.health", map[string]any{}); ok || !strings.Contains(body, "unauthorized") {
		t.Fatalf("revoked site link: %s", body)
	}
	if ok, _, body := f.call("site-a", "whatsapp.number.health", map[string]any{"channel": "wa-120"}); !ok {
		t.Fatalf("another site's revocation blocked site-a: %s", body)
	}
}

// notices counts the relay_channels events that tell a site to re-read its channels.
func (f *numberFixture) notices(site string) int {
	var count int
	if err := f.auth.DB.QueryRow(f.ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE site_id=$1 AND payload->>'object'='relay_channels'`, site).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func TestNumberCheckRecordsTheResultAndRepairsTheSubscription(t *testing.T) {
	f := newNumberFixture(t)
	subscribed := false
	f.reply = func(call string) (int, string) {
		switch call {
		case "GET 120":
			return 200, `{"id":"120","display_phone_number":"+91 99999 99120","verified_name":"Acme Textiles","status":"CONNECTED","quality_rating":"GREEN"}`
		case "GET 230/subscribed_apps":
			if subscribed {
				return 200, `{"data":[{"whatsapp_business_api_data":{"id":"333"}}]}`
			}
			return 200, `{"data":[]}`
		case "POST 230/subscribed_apps":
			subscribed = true
		}
		return 0, ""
	}
	before := f.notices("site-a")
	ok, result, body := f.call("site-a", "whatsapp.number.check", map[string]any{"channel": "wa-120"})
	if !ok || result["connection_status"] != "connected" || result["webhook_subscribed"] != false || result["reconnect_required"] != false {
		t.Fatalf("check without repair: %s", body)
	}
	if profile, _ := result["profile"].(map[string]any); profile["verified_name"] != "Acme Textiles" {
		t.Fatalf("check profile: %s", body)
	}
	f.noSecret(body)
	if ok, result, body = f.call("site-a", "whatsapp.number.check", map[string]any{"channel": "wa-120", "repair": true}); !ok || result["webhook_subscribed"] != true {
		t.Fatalf("check with repair: %s", body)
	}
	if calls := f.graphCalls(); !strings.Contains(strings.Join(calls, ","), "POST 230/subscribed_apps") {
		t.Fatalf("repair did not subscribe: %v", calls)
	}
	for _, bearer := range f.bearers {
		if bearer != numberSecrets {
			t.Fatalf("check used another number's token: %v", f.bearers)
		}
	}
	if f.notices("site-a") <= before {
		t.Fatal("check did not tell the site to re-read its channels")
	}
}

func TestNumberCheckAsksForReconnectWhenMetaRefusesTheToken(t *testing.T) {
	f := newNumberFixture(t)
	f.reply = func(call string) (int, string) {
		return 401, `{"error":{"code":190,"message":"Error validating access token","type":"OAuthException"}}`
	}
	ok, result, body := f.call("site-a", "whatsapp.number.check", map[string]any{"channel": "wa-121"})
	if !ok || result["connection_status"] != "reconnect_required" || result["reconnect_required"] != true {
		t.Fatalf("refused token: %s", body)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.health", map[string]any{"channel": "wa-121"}); !ok || result["connection_status"] != "reconnect_required" {
		t.Fatalf("stored status: %s", body)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.check", map[string]any{"channel": "wa-130", "repair": true}); ok || errorCode(result) != "not_found" {
		t.Fatalf("check of another site's number: %s", body)
	}
	if calls := f.graphCalls(); len(calls) != 1 {
		t.Fatalf("Meta calls: %v", calls)
	}
}

func TestNumberSettingsChangeOnlyTheSiteNumber(t *testing.T) {
	f := newNumberFixture(t)
	f.reply = func(call string) (int, string) {
		if call == "GET 121" {
			return 200, `{"id":"121","status":"CONNECTED"}`
		}
		return 0, ""
	}
	ok, result, body := f.call("site-a", "whatsapp.number.settings", map[string]any{"channel": "wa-121", "enabled": false, "templates": false})
	settings, _ := result["settings"].(map[string]any)
	if !ok || result["enabled"] != false || result["connection_status"] != "disabled" || settings["templates"] != false || settings["sync_metadata"] != true {
		t.Fatalf("turn off: %s", body)
	}
	if f.reloads != 1 {
		t.Fatalf("runtime reloads: %d", f.reloads)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.settings", map[string]any{"channel": "wa-121", "enabled": true}); !ok || result["enabled"] != true || result["connection_status"] != "connected" {
		t.Fatalf("turn on checks the number again: %s", body)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.settings", map[string]any{"channel": "wa-130", "enabled": false}); ok || errorCode(result) != "not_found" {
		t.Fatalf("settings of another site's number: %s", body)
	}
	other, _ := f.store.GetChannel(f.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-130"})
	if !other.Enabled {
		t.Fatal("another site's number was turned off")
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.settings", map[string]any{"channel": "wa-121"}); ok || errorCode(result) != "invalid_request" {
		t.Fatalf("no change: %s", body)
	}
	if _, err := f.auth.DB.Exec(f.ctx, `UPDATE relay_channel_credentials SET enabled=false,config=config||'{"partner_status":"removed"}' WHERE channel_key='wa-120'`); err != nil {
		t.Fatal(err)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.settings", map[string]any{"channel": "wa-120", "enabled": true}); ok || errorCode(result) != "reconnect_required" {
		t.Fatalf("turn on a number Meta removed: %s", body)
	}
	f.noSecret(body)
}

// adminTurnsOff saves a channel off the way the Hub admin directory does.
func adminTurnsOff(t *testing.T, store *containers.Store, ref containers.ChannelRef) {
	t.Helper()
	c, err := store.GetChannel(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled = false
	if err = store.SaveChannel(t.Context(), c, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestNumberAnAdminTurnedOffStaysOff(t *testing.T) {
	f := newNumberFixture(t)
	adminTurnsOff(t, f.store, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-121"})
	ok, result, body := f.call("site-a", "whatsapp.number.settings", map[string]any{"channel": "wa-121", "enabled": true})
	if ok || errorCode(result) != "admin_off" || !strings.Contains(body, "Relay Hub admin turned this channel off") {
		t.Fatalf("turn on a number the admin turned off: %s", body)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.settings", map[string]any{"channel": "wa-121", "templates": false}); !ok || result["enabled"] != false {
		t.Fatalf("other settings of an admin-off number: %s", body)
	}
	if c, _ := f.store.GetChannel(f.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-121"}); c.Enabled {
		t.Fatal("the number is on")
	}
}

func TestPINResetKeepsTheNewPINInTheHubOnly(t *testing.T) {
	f := newNumberFixture(t)
	ok, result, body := f.call("site-a", "whatsapp.number.pin_reset", map[string]any{"channel": "wa-120"})
	if !ok || result["pin_kept"] != true {
		t.Fatalf("PIN reset: %s", body)
	}
	f.mu.Lock()
	sent, _ := f.bodies["POST 120"]["pin"].(string)
	f.mu.Unlock()
	stored, err := f.store.Credential(f.ctx, "WhatsApp", "wa-120")
	if err != nil || len(sent) != 6 || stored.Config["pin"] != sent || sent == "123456" {
		t.Fatalf("new PIN sent %q, stored %v %v", sent, stored.Config["pin"], err)
	}
	if strings.Contains(body, sent) {
		t.Fatalf("the PIN left the Hub: %s", body)
	}
	f.noSecret(body)

	f.reply = func(string) (int, string) {
		return 400, `{"error":{"code":100,"error_user_msg":"Invalid PIN","type":"OAuthException"}}`
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.pin_reset", map[string]any{"channel": "wa-120"}); ok || errorCode(result) != "meta_rejected" {
		t.Fatalf("refused PIN: %s", body)
	}
	if kept, _ := f.store.Credential(f.ctx, "WhatsApp", "wa-120"); kept.Config["pin"] != sent {
		t.Fatal("a refused PIN replaced the stored PIN")
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.pin_reset", map[string]any{"channel": "wa-130"}); ok || errorCode(result) != "not_found" {
		t.Fatalf("PIN reset of another site's number: %s", body)
	}
	if _, err := f.auth.DB.Exec(f.ctx, `UPDATE relay_channel_credentials SET config=config||'{"coexistence":true}' WHERE channel_key='wa-121'`); err != nil {
		t.Fatal(err)
	}
	if ok, result, body = f.call("site-a", "whatsapp.number.pin_reset", map[string]any{"channel": "wa-121"}); ok || errorCode(result) != "not_supported" {
		t.Fatalf("PIN reset of a Business app number: %s", body)
	}
}

func TestRemoveTakesTheNumberAwayAndDeregistersIt(t *testing.T) {
	f := newNumberFixture(t)
	if ok, result, body := f.call("site-a", "whatsapp.number.remove", map[string]any{"channel": "wa-121"}); ok || errorCode(result) != "confirm_required" {
		t.Fatalf("remove without confirm: %s", body)
	}
	if ok, result, body := f.call("site-a", "whatsapp.number.remove", map[string]any{"channel": "wa-130", "confirm": true}); ok || errorCode(result) != "not_found" {
		t.Fatalf("remove another site's number: %s", body)
	}
	before := f.notices("site-a")
	ok, result, body := f.call("site-a", "whatsapp.number.remove", map[string]any{"channel": "wa-121", "confirm": true})
	if !ok || result["removed"] != true || result["deregistered"] != true || result["unsubscribed"] != true {
		t.Fatalf("remove: %s", body)
	}
	f.noSecret(body)
	calls := strings.Join(f.graphCalls(), ",")
	if !strings.Contains(calls, "POST 121/deregister") || !strings.Contains(calls, "DELETE 231/subscribed_apps") {
		t.Fatalf("Meta calls: %s", calls)
	}
	removed, err := f.store.Credential(f.ctx, "WhatsApp", "wa-121")
	if err != nil || removed.Site != "" || removed.Enabled || removed.Secret != "" || removed.Config["pin"] != nil || removed.Config["partner_status"] != "removed" {
		t.Fatalf("removed number %+v %v", removed, err)
	}
	if f.notices("site-a") <= before || f.reloads != 1 {
		t.Fatal("removal did not tell the site or reload the runtime")
	}
	ok, result, body = f.call("site-a", "channels.list", map[string]any{})
	if channels, _ := result["channels"].([]any); !ok || len(channels) != 1 {
		t.Fatalf("site channels after removal: %s", body)
	}
}

func TestRemoveStillDeregistersWhenTheRuntimeReloadFails(t *testing.T) {
	f := newNumberFixture(t)
	f.reloadErr = errors.New("runtime reload failed")
	ok, result, body := f.call("site-a", "whatsapp.number.remove", map[string]any{"channel": "wa-121", "confirm": true})
	if !ok || result["removed"] != true || result["deregistered"] != true {
		t.Fatalf("remove with a failed reload: %s", body)
	}
	if calls := strings.Join(f.graphCalls(), ","); !strings.Contains(calls, "POST 121/deregister") {
		t.Fatalf("Meta calls: %s", calls)
	}
}

func TestRemoveWaitsForOpenSendsAndKeepsASharedWABASubscribed(t *testing.T) {
	f := newNumberFixture(t)
	if _, err := f.auth.DB.Exec(f.ctx, `INSERT INTO relay_outbox(account,request_key,payload,site_id) VALUES('wa-120','k1','{}','site-a')`); err != nil {
		t.Fatal(err)
	}
	if ok, result, body := f.call("site-a", "whatsapp.number.remove", map[string]any{"channel": "wa-120", "confirm": true}); ok || errorCode(result) != "pending_sends" {
		t.Fatalf("remove with an open send: %s", body)
	}
	if kept, _ := f.store.Credential(f.ctx, "WhatsApp", "wa-120"); kept.Site != "site-a" || kept.Secret == "" || len(f.graphCalls()) != 0 {
		t.Fatal("a refused removal changed the number or called Meta")
	}
	if _, err := f.auth.DB.Exec(f.ctx, `DELETE FROM relay_outbox; UPDATE relay_channel_credentials SET config=config||'{"waba_id":"230"}' WHERE channel_key='wa-130'`); err != nil {
		t.Fatal(err)
	}
	ok, result, body := f.call("site-a", "whatsapp.number.remove", map[string]any{"channel": "wa-120", "confirm": true})
	if !ok || result["deregistered"] != true || result["unsubscribed"] != false {
		t.Fatalf("remove from a shared WABA: %s", body)
	}
	if strings.Contains(strings.Join(f.graphCalls(), ","), "DELETE") {
		t.Fatal("the app left a WABA that another site still uses")
	}
}

func TestSiteDisconnectEndsTheSiteLink(t *testing.T) {
	f := newNumberFixture(t)
	if ok, result, body := f.call("site-a", "site.disconnect", map[string]any{}); ok || errorCode(result) != "confirm_required" {
		t.Fatalf("disconnect without confirm: %s", body)
	}
	if ok, result, body := f.call("site-a", "site.disconnect", map[string]any{"confirm": true}); !ok || result["disconnected"] != true {
		t.Fatalf("disconnect: %s", body)
	}
	if ok, _, body := f.call("site-a", "whatsapp.number.health", map[string]any{"channel": "wa-120"}); ok || !strings.Contains(body, "unauthorized") {
		t.Fatalf("a call after disconnect: %s", body)
	}
	if ok, _, body := f.call("site-b", "whatsapp.number.health", map[string]any{}); !ok {
		t.Fatalf("another site was disconnected: %s", body)
	}
}
