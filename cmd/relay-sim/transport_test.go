package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTransportRoutesLocalhostSitesToCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"host":"` + r.Host + `"}}`))
	}))
	defer core.Close()
	cu, _ := url.Parse(core.URL)
	records := newRecorder()
	tr := newTransport(cu, cu, records)
	defer tr.base.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: denyRedirect}
	site := "http://relay-onboard.localhost:" + cu.Port()
	res, err := client.Post(site+"/api/method/relay_core.onboarding.api.enrolment_proof", "application/json", strings.NewReader(`{"nonce":"n"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "relay-onboard.localhost:"+cu.Port()) {
		t.Fatal("Frappe site Host lost", string(body))
	}
	for _, refused := range []string{"http://relay-onboard.localhost:9999", "https://relay-onboard.localhost:" + cu.Port(), "http://localhost.example.com:" + cu.Port()} {
		if _, err := client.Get(refused + "/"); err == nil {
			t.Fatal("unexpected destination allowed", refused)
		}
	}
}

func TestTransportForwardsLargeCallbacksWhole(t *testing.T) {
	received := make(chan int, 1)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- len(body)
		w.Write([]byte(`{"message":{"accepted":1}}`))
	}))
	defer core.Close()
	cu, _ := url.Parse(core.URL)
	tr := newTransport(cu, cu, newRecorder())
	defer tr.base.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: denyRedirect}
	payload := strings.Repeat("x", 3<<20)
	res, err := client.Post("http://relay-onboard.localhost:"+cu.Port()+"/api/method/relay_core.inbound.receive", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal("a 3 MiB history callback must reach Core", err)
	}
	res.Body.Close()
	if got := <-received; got != len(payload) {
		t.Fatal("callback body cut", got)
	}
}

func TestRecorderRedactsSignupSecrets(t *testing.T) {
	records := newRecorder()
	r := httptest.NewRequest("GET", "/v25.0/oauth/access_token?client_id=sim-app&client_secret=secret-one&code=secret-two&input_token=secret-three&fields=id", nil)
	records.record(r, "graph", []byte(`{"messaging_product":"whatsapp","pin":"123456"}`), 200, []byte(`{"access_token":"sim-biz-1","token_type":"bearer"}`), time.Now(), "")
	r = httptest.NewRequest("POST", "/v25.0/120/messages", nil)
	records.record(r, "graph", []byte(`{"to":"9198","type":"text"}`), 200, []byte(`{"error":{"code":100}}`), time.Now(), "")
	rows := records.snapshot()
	first := mustJSON(t, rows[0])
	for _, secret := range []string{"secret-one", "secret-two", "secret-three", "123456", "sim-biz-1"} {
		if strings.Contains(first, secret) {
			t.Fatal("capture leaked", secret, first)
		}
	}
	if !strings.Contains(rows[0].Query, "fields=id") || !strings.Contains(first, "whatsapp") {
		t.Fatal("redaction removed plain fields", first)
	}
	if string(rows[1].Body) != `{"to":"9198","type":"text"}` || string(rows[1].Response) != `{"error":{"code":100}}` {
		t.Fatal("bodies without secrets must stay unchanged")
	}
}

func mustJSON(t *testing.T, v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
