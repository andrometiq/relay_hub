package main

import (
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
)

// signedHub accepts webhooks signed with the signup container app secret.
type signedHub struct {
	mu       sync.Mutex
	payloads []string
}

func (h *signedHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	mac := hmac.New(sha256.New, []byte(esAppSecret))
	mac.Write(raw)
	if r.Header.Get("X-Hub-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || r.URL.Path != "/webhooks/whatsapp/sim-v1" {
		http.Error(w, "bad signature", 403)
		return
	}
	h.mu.Lock()
	h.payloads = append(h.payloads, string(raw))
	h.mu.Unlock()
	w.Write([]byte(`{"ok":true}`))
}

func (h *signedHub) received() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.payloads...)
}

func newSignupSimulation(t *testing.T) (*simulation, *esHarness, *signedHub) {
	received := &signedHub{}
	hub := httptest.NewServer(received)
	t.Cleanup(hub.Close)
	hu, _ := url.Parse(hub.URL)
	h := newESHarness(t)
	tr := newTransport(hu, hu, h.meta.records)
	t.Cleanup(tr.base.CloseIdleConnections)
	return &simulation{transport: tr, meta: h.meta, hub: hu, token: strings.Repeat("c", 32)}, h, received
}

func controlCall(t *testing.T, s *simulation, method, path, body string, want int) string {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	s.control(w, r)
	if w.Code != want {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestSignupControlRoutes(t *testing.T) {
	s, h, hub := newSignupSimulation(t)
	token, data := h.signupNumber(esDialogInput{Scenario: "coexistence", Number: "919876500002", ShareHistory: true})
	waba, phone := data["waba_id"].(string), data["phone_number_id"].(string)

	controlCall(t, s, "POST", "/_sim/es/payment", `{"waba_id":"999","ready":true}`, 404)
	controlCall(t, s, "POST", "/_sim/es/payment", `{"waba_id":"`+waba+`","ready":true}`, 200)
	if v := h.graph("GET", "/v25.0/"+waba, token, "", 200); v["primary_funding_id"] == nil {
		t.Fatal("payment method not set")
	}
	controlCall(t, s, "POST", "/_sim/es/echo", `{"phone_id":"`+phone+`","to":"919900000001"}`, 400)
	if out := controlCall(t, s, "POST", "/_sim/es/echo", `{"phone_id":"`+phone+`","to":"919900000001","text":"Sent from the phone"}`, 200); !strings.Contains(out, `"status":200`) {
		t.Fatal(out)
	}
	controlCall(t, s, "POST", "/_sim/es/name-status", `{"phone_id":"`+phone+`","status":"MAYBE"}`, 400)
	controlCall(t, s, "POST", "/_sim/es/name-status", `{"phone_id":"`+phone+`","status":"DECLINED"}`, 200)
	h.graph("POST", "/v25.0/"+waba+"/subscribed_apps", token, "", 200)
	controlCall(t, s, "POST", "/_sim/es/partner-removed", `{"waba_id":"`+waba+`"}`, 200)
	if apps := h.graph("GET", "/v25.0/"+waba+"/subscribed_apps", token, "", 200)["data"].([]any); len(apps) != 0 {
		t.Fatal("partner removal kept the subscription")
	}
	got := hub.received()
	if len(got) != 3 {
		t.Fatalf("want 3 signed webhooks, got %d", len(got))
	}
	for i, want := range []string{`"field":"smb_message_echoes"`, `"field":"phone_number_name_update"`, `"event":"PARTNER_REMOVED"`} {
		if !strings.Contains(got[i], want) {
			t.Fatal(want, got[i])
		}
	}
	if !strings.Contains(got[0], `"from":"919876500002"`) || !strings.Contains(got[1], `"decision":"DECLINED"`) {
		t.Fatal("webhook shapes")
	}

	controlCall(t, s, "POST", "/_sim/faults", `{"target":"signup","faults":["429"]}`, 400)
	controlCall(t, s, "POST", "/_sim/faults", `{"target":"graph","faults":["subscribe_503"]}`, 400)
	controlCall(t, s, "POST", "/_sim/faults", `{"target":"signup","faults":["subscribe_503","register_133016"]}`, 200)
	if !h.meta.records.take("signup", "register_133016") || !h.meta.records.take("signup", "subscribe_503") || h.meta.records.take("signup", "subscribe_503") {
		t.Fatal("signup faults are one-shot by name")
	}
	var state map[string]any
	json.Unmarshal([]byte(controlCall(t, s, "GET", "/_sim/es/state", "", 200)), &state)
	if len(state["phones"].([]any)) != 1 || len(state["calls"].([]any)) == 0 || strings.Contains(mustJSON(t, state), token) {
		t.Fatal("state dump")
	}
	r := httptest.NewRequest("GET", "/_sim/es/state", nil)
	w := httptest.NewRecorder()
	s.control(w, r)
	if w.Code != 401 {
		t.Fatal("signup control needs the control token")
	}
}

func TestSignupWebhooksDeliverInOrderWithRetry(t *testing.T) {
	webhooks := newSignupWebhooks()
	webhooks.gap = 0
	var mu sync.Mutex
	posts := []string{}
	failures := 1
	webhooks.post = func(_ context.Context, payload []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		posts = append(posts, string(payload))
		if failures > 0 {
			failures--
			return 503, nil
		}
		return 200, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go webhooks.run(ctx)
	webhooks.queue(envelope("230", "account_update", map[string]any{"event": "PARTNER_ADDED"}))
	webhooks.queue(envelope("230", "history", map[string]any{}))
	deadline := time.Now().Add(5 * time.Second)
	for len(webhooks.snapshot()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	sent := webhooks.snapshot()
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || sent[0]["attempts"] != 2 || sent[1]["attempts"] != 1 || len(posts) != 3 || !strings.Contains(posts[2], `"history"`) {
		t.Fatal(sent, len(posts))
	}
}
