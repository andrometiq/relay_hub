package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// enrolToken is a fixed test enrolment token.
var enrolToken = "relay_enrol_" + strings.Repeat("ab", 32)

// controlStatus posts to a control path and returns only the status.
func (l *launcher) controlStatus(path string, body any) int {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(l.ctx, "POST", "http://"+l.metaAddr+"/_sim/"+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+l.controlToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestSignupContainerControl(t *testing.T) {
	dsn := isolatedLauncherDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	core := httptest.NewServer(http.NotFoundHandler())
	defer core.Close()
	l := newLauncher(t, ctx, dsn, core.URL)
	l.start(false)
	if status := l.controlStatus("signup-container", map[string]any{"token": "short", "origin_pattern": "http://*.localhost:8001"}); status != 400 {
		t.Fatal("weak token accepted", status)
	}
	if status := l.controlStatus("signup-container", map[string]any{"token": enrolToken}); status != 400 {
		t.Fatal("missing origin pattern accepted", status)
	}
	if got := l.call("signup-container", map[string]any{"token": enrolToken, "origin_pattern": "http://*.localhost:8001"}); got["signup"] != true {
		t.Fatal(got)
	}
	code := "relay_enrol_" + strings.Repeat("cd", 32)
	l.call("signup-container", map[string]any{"token": code, "origin_pattern": "http://*.localhost:8001", "kind": "setup_code", "max_sites": 1, "expires_in": -60})
	l.call("signup-container", map[string]any{"token": enrolToken, "origin_pattern": "http://shop.localhost:8001"})
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var signup bool
	var config string
	if err = conn.QueryRow(ctx, "SELECT signup FROM relay_containers WHERE container_id='sim-app'").Scan(&signup); err != nil || !signup {
		t.Fatal("sim-app is not the signup container", err)
	}
	if err = conn.QueryRow(ctx, "SELECT count(*)::text FROM relay_enrolment_tokens WHERE token_hash NOT LIKE 'relay_enrol_%'").Scan(&config); err != nil || config != "2" {
		t.Fatal("tokens must be stored hashed, once each", config, err)
	}
	var pattern string
	var expired bool
	if err = conn.QueryRow(ctx, "SELECT origin_pattern, expires_at < now() FROM relay_enrolment_tokens WHERE kind='bench'").Scan(&pattern, &expired); err != nil || pattern != "http://shop.localhost:8001" || expired {
		t.Fatal("bench token update", pattern, expired, err)
	}
	if err = conn.QueryRow(ctx, "SELECT expires_at < now() FROM relay_enrolment_tokens WHERE kind='setup_code' AND max_sites=1").Scan(&expired); err != nil || !expired {
		t.Fatal("expired setup code", err)
	}
	state := l.call("es/state", nil)
	if len(state["phones"].([]any)) != 0 {
		t.Fatal("fresh signup state expected")
	}
}

// fakeEnrolCore is a Frappe site that answers the enrolment proof with its
// callback secret and accepts every signed callback.
type fakeEnrolCore struct {
	secret string
	mu     sync.Mutex
	bodies []string
}

func (c *fakeEnrolCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if r.URL.Path == "/api/method/relay_core.onboarding.api.enrolment_proof" {
		var in struct{ Nonce, Challenge string }
		json.Unmarshal(raw, &in)
		mac := hmac.New(sha256.New, []byte(c.secret))
		mac.Write([]byte(in.Nonce + ":" + in.Challenge + ":http://" + r.Host))
		if r.Header.Get("Authorization") != "token enrol-key:enrol-secret" {
			http.Error(w, "forbidden", 403)
			return
		}
		writeJSON(w, map[string]any{"message": map[string]any{"proof": hex.EncodeToString(mac.Sum(nil))}})
		return
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, string(raw))
	c.mu.Unlock()
	var body map[string]any
	json.Unmarshal(raw, &body)
	accepted := 0
	for _, key := range []string{"events", "statuses", "updates"} {
		rows, _ := body[key].([]any)
		accepted += len(rows)
	}
	writeJSON(w, map[string]any{"message": map[string]any{"accepted": accepted}})
}

func (c *fakeEnrolCore) waitBody(t *testing.T, parts ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		bodies := append([]string{}, c.bodies...)
		c.mu.Unlock()
	next:
		for _, body := range bodies {
			for _, part := range parts {
				if !strings.Contains(body, part) {
					continue next
				}
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Core did not receive a callback with %v", parts)
}

// hubJSON calls a public Hub or fake Meta route and decodes its reply.
func (l *launcher) hubJSON(method, address, path, launch string, body any, want int) map[string]any {
	l.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(l.ctx, method, "http://"+address+path, reader)
	req.Header.Set("Content-Type", "application/json")
	if launch != "" {
		req.Header.Set("X-Relay-Launch", launch)
	}
	if strings.HasPrefix(launch, "Bearer ") {
		req.Header.Del("X-Relay-Launch")
		req.Header.Set("Authorization", launch)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		l.t.Fatalf("%s %s: status %d want %d: %s", method, path, res.StatusCode, want, raw)
	}
	var v map[string]any
	json.Unmarshal(raw, &v)
	return v
}

func structured(result map[string]any) map[string]any {
	value, _ := result["result"].(map[string]any)["structuredContent"].(map[string]any)
	return value
}

// enrolSite links one *.localhost site through the enrolment API.
func (l *launcher) enrolSite(host string, core *fakeEnrolCore) string {
	l.t.Helper()
	body := map[string]any{"origin": "http://" + host, "site_name": strings.Split(host, ":")[0], "display_name": "Acme Traders", "core_api_key": "enrol-key",
		"core_api_secret": "enrol-secret", "callback_secret": core.secret, "nonce": strings.Repeat("1", 64), "relay_core_version": "1.0.0"}
	enrolled := l.hubJSON("POST", l.hubAddr, "/api/enrol", "Bearer "+enrolToken, body, 201)
	token, _ := enrolled["hub_token"].(string)
	if token == "" {
		l.t.Fatal(enrolled)
	}
	return token
}

// TestSignupLauncherEndToEnd drives enrolment, a new-number signup through
// the fake dialog (HTTP only, no browser), the Hub steps against the fake
// Graph calls and a hello_world send.
func TestSignupLauncherEndToEnd(t *testing.T) {
	dsn := isolatedLauncherDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	core := &fakeEnrolCore{secret: strings.Repeat("e", 64)}
	server := httptest.NewServer(core)
	defer server.Close()
	l := newLauncher(t, ctx, dsn, server.URL)
	l.start(false)
	port := server.URL[strings.LastIndex(server.URL, ":")+1:]
	l.call("signup-container", map[string]any{"token": enrolToken, "origin_pattern": "http://*.localhost:" + port})
	token := l.enrolSite("acme.localhost:"+port, core)
	other := l.enrolSite("other.localhost:"+port, core)

	started := structured(l.mcp(token, "whatsapp.signup.start", map[string]any{"flow": "new", "actor": "admin@acme.localhost",
		"prefill": map[string]any{"business": map[string]any{"name": "Acme Traders", "email": "admin@acme.localhost", "country": "IN"}, "phone": map[string]any{"display_name": "Acme Traders"}}}, 200))
	launch, err := url.Parse(started["launch_url"].(string))
	if err != nil || launch.Fragment == "" {
		t.Fatal(started)
	}
	session := strings.TrimPrefix(launch.Path, "/onboard/whatsapp/")
	l.hubJSON("GET", l.hubAddr, launch.Path+"/config", "wrong-secret", nil, 404)
	config := l.hubJSON("GET", l.hubAddr, launch.Path+"/config", launch.Fragment, nil, 200)
	if config["app_id"] != esAppID || config["config_id"] != esConfigID || config["sdk_url"] != "http://"+l.metaAddr+"/sdk.js" {
		t.Fatal(config)
	}

	dialog := l.hubJSON("POST", l.metaAddr, "/es/dialog/finish", "", map[string]any{"app_id": config["app_id"], "config_id": config["config_id"],
		"scenario": "new", "number": "919876500001", "otp": esOTP, "display_name": "Acme Traders"}, 200)
	data := dialog["data"].(map[string]any)
	l.hubJSON("POST", l.hubAddr, launch.Path+"/event", launch.Fragment, map[string]any{"event": dialog["event"], "data": data, "origin": "http://" + l.metaAddr}, 200)
	info := map[string]any{"event": dialog["event"], "data": data}
	l.hubJSON("POST", l.hubAddr, launch.Path+"/code", launch.Fragment, map[string]any{"code": dialog["code"], "session_info": info}, 200)
	l.hubJSON("POST", l.hubAddr, launch.Path+"/code", launch.Fragment, map[string]any{"code": dialog["code"], "session_info": info}, 200)

	deadline := time.Now().Add(40 * time.Second)
	var state map[string]any
	for time.Now().Before(deadline) {
		state = structured(l.mcp(token, "whatsapp.signup.get", map[string]any{"session_id": session}, 200))
		if state["state"] == "completed" || state["state"] == "failed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if state["state"] != "completed" || state["channel_key"] != "wa-120" {
		t.Fatalf("signup did not complete: %v", state)
	}
	l.mcpError(other, "whatsapp.signup.get", map[string]any{"session_id": session})

	listed, _ := json.Marshal(l.mcp(token, "channels.list", map[string]any{}, 200))
	if !strings.Contains(string(listed), "wa-120") {
		t.Fatalf("channel not listed: %s", listed)
	}
	es := l.call("es/state", nil)
	registers, exchanges := 0, 0
	for _, call := range es["calls"].([]any) {
		if call.(map[string]any)["kind"] == "register" {
			registers++
		}
	}
	for _, code := range es["codes"].([]any) {
		exchanges += int(code.(map[string]any)["exchange_calls"].(float64))
	}
	if registers != 1 || exchanges != 1 {
		t.Fatalf("want one register and one code exchange, got %d and %d", registers, exchanges)
	}

	l.mcp(token, "whatsapp.send", map[string]any{"channel": "wa-120", "message": map[string]any{"key": "hello-1", "message": map[string]any{
		"operation": "templates.send", "to": "919800000001", "name": "hello_world", "language": "en_US"}}}, 200)
	sim := l.wait(func(s map[string]any) bool {
		for _, d := range s["deliveries"].([]any) {
			if row := d.(map[string]any); row["request_key"] == "hello-1" {
				return row["state"] == "submitted"
			}
		}
		return false
	})
	provider := ""
	for _, d := range sim["deliveries"].([]any) {
		if row := d.(map[string]any); row["request_key"] == "hello-1" {
			provider, _ = row["provider_message_id"].(string)
		}
	}
	captured := false
	for _, r := range sim["records"].([]any) {
		row := r.(map[string]any)
		raw, _ := json.Marshal(row["body"])
		captured = captured || row["path"] == "/v25.0/120/messages" && strings.Contains(string(raw), "hello_world") && row["status"] == float64(200)
	}
	if !captured || provider == "" {
		t.Fatal("hello_world send not captured")
	}
	core.waitBody(t, provider, "delivered")
	if sessions, _ := sim["signup_sessions"].([]any); len(sessions) != 1 || strings.Contains(mustJSON(t, sessions), "token_ciphertext") {
		t.Fatal("signup sessions in state", sessions)
	}
}
