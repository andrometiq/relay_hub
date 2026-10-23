package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func TestDestinationGuards(t *testing.T) {
	for _, raw := range []string{"http://localhost:80", "http://example.com", "https://127.0.0.1", "http://0.0.0.0:1", "http://169.254.169.254", "http://127.0.0.1:1/path", "http://user:secret@127.0.0.1", "http://127.0.0.1?next=evil", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:18091/"} {
		if _, err := localURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:18091", "http://[::1]:18091"} {
		if _, err := localURL(raw); err != nil {
			t.Error(err)
		}
	}
	for _, dsn := range []string{"postgres://u:p@127.0.0.1:18433/channel_hub", "postgres://u:p@example.com:18433/relay_hub_sim", "postgres://u:p@127.0.0.1:5432/relay_hub_sim", "host=127.0.0.1,192.0.2.1 port=18433 dbname=relay_hub_sim", "postgres://u:p@127.0.0.1:18433/relay_hub_sim?search_path=public"} {
		if simulationDSN(dsn) == nil {
			t.Error("unsafe database accepted")
		}
	}
	if err := simulationDSN("postgres://u:p@127.0.0.1:18433/relay_hub_sim?sslmode=disable"); err != nil {
		t.Fatal(err)
	}
}

func TestMetaTemplatesFailuresAndCaptures(t *testing.T) {
	records := newRecorder()
	meta := &fakeMeta{records: records, pageSize: 1}
	call := func(method, path, body string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer sim-provider-token")
		r.Header.Set("Cookie", "secret")
		r.Header.Set("X-Relay-Signature", "signature")
		w := httptest.NewRecorder()
		meta.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := call("POST", "/v25.0/220/message_templates", `{"name":"first","language":"en_US","category":"UTILITY","components":[]}`, 200)
	call("POST", "/v25.0/220/message_templates", `{"name":"second","language":"en_US"}`, 200)
	if first["status"] != "PENDING" {
		t.Fatal(first)
	}
	page := call("GET", "/v25.0/220/message_templates", "", 200)
	if len(page["data"].([]any)) != 1 || page["paging"].(map[string]any)["cursors"].(map[string]any)["after"] != "1" {
		t.Fatal(page)
	}
	next := call("GET", "/v25.0/220/message_templates?after=1", "", 200)
	if next["data"].([]any)[0].(map[string]any)["name"] != "second" {
		t.Fatal(next)
	}
	filtered := call("GET", "/v25.0/220/message_templates?name=second", "", 200)
	if len(filtered["data"].([]any)) != 1 || filtered["data"].([]any)[0].(map[string]any)["name"] != "second" || filtered["paging"] != nil {
		t.Fatal("template filtering must precede pagination", filtered)
	}
	call("GET", "/v25.0/220/message_templates?after=-1", "", 400)
	if _, ok := meta.approve(first["id"].(string), "APPROVED"); !ok {
		t.Fatal("template missing")
	}
	if got := call("GET", "/v25.0/220/message_templates", "", 200)["data"].([]any)[0].(map[string]any)["status"]; got != "APPROVED" {
		t.Fatal(got)
	}
	records.faults["graph"] = []string{"429", "503"}
	call("POST", "/v25.0/110/messages", `{"to":"919999999999"}`, 429)
	call("POST", "/v25.0/110/messages", `{}`, 503)
	a := call("POST", "/v25.0/110/messages", `{}`, 200)
	b := call("POST", "/v25.0/110/messages", `{}`, 200)
	if a["messages"].([]any)[0].(map[string]any)["id"] == b["messages"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("duplicate provider ID")
	}
	for _, row := range records.snapshot() {
		if row.Headers.Get("Authorization") != "[redacted]" || row.Headers.Get("Cookie") != "[redacted]" || row.Headers.Get("X-Relay-Signature") != "[redacted]" {
			t.Fatal("secret header leaked")
		}
		if row.StartedNS < 0 || row.FinishedNS < row.StartedNS || row.Method == "" || row.Path == "" || len(row.Response) == 0 {
			t.Fatal("incomplete capture")
		}
	}
}

func TestTransportRewritesAndRejectsRedirects(t *testing.T) {
	records := newRecorder()
	meta := httptest.NewServer(&fakeMeta{records: records})
	defer meta.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "yrp.site:8001" {
			t.Error("canonical Host lost")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://192.0.2.1/", 302)
			return
		}
		w.Write([]byte(`{"message":{"accepted":1}}`))
	}))
	defer core.Close()
	mu, _ := url.Parse(meta.URL)
	cu, _ := url.Parse(core.URL)
	tr := newTransport(mu, cu, records)
	defer tr.base.CloseIdleConnections()
	tr.site = "http://yrp.site:8001"
	client := &http.Client{Transport: tr, CheckRedirect: denyRedirect}
	for _, dest := range []string{"https://example.com/", "http://127.0.0.1:1/", "https://graph.facebook.com.evil/messages", "http://graph.facebook.com/messages", "http://yrp.site:8001/redirect"} {
		if response, err := client.Get(dest); err == nil {
			response.Body.Close()
			t.Errorf("unexpected destination/redirect accepted: %s", dest)
		}
	}
	if _, err := tr.base.DialContext(context.Background(), "tcp", "192.0.2.1:80"); err == nil {
		t.Fatal("non-loopback dial accepted")
	}
	req, _ := http.NewRequest("POST", "https://graph.facebook.com/v25.0/110/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sim-provider-token")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	records.faults["status"] = []string{"503", "wrong_ack"}
	records.hold = true
	for _, want := range []int{503, 200, 503} {
		res, err = client.Post("http://yrp.site:8001/api/method/relay_core.inbound.receive_statuses", "application/json", strings.NewReader(`{"statuses":[{}]}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatal(res.StatusCode, want)
		}
	}
	// The independent realtime lane reaches actual Core while receipts are held.
	res, err = client.Post("http://yrp.site:8001/api/method/relay_core.inbound.receive", "application/json", strings.NewReader(`{"events":[{}]}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	records.hold = false
	res, err = client.Post("http://yrp.site:8001/api/method/relay_core.inbound.receive_statuses", "application/json", strings.NewReader(`{"statuses":[{}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(body, []byte(`"accepted":1`)) {
		t.Fatal(string(body))
	}
}

func TestControlAuthenticationSignedIngressAndTemplateWebhook(t *testing.T) {
	wa, err := whatsapp.New([]whatsapp.Account{{ID: "sim-v1", Site: "http://127.0.0.1:1", PhoneID: "110", WABAID: "220", AccessToken: "fake", AppSecret: "sim-app-secret", Version: "v25.0"}})
	if err != nil {
		t.Fatal(err)
	}
	wa.Events = &memoryEvents{}
	var mu sync.Mutex
	var received []json.RawMessage
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The real webhook parser checks signature and account identity even without a journal.
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.SetPathValue("account", "sim-v1")
		out := httptest.NewRecorder()
		wa.Webhook(out, r)
		if out.Code == 200 {
			mu.Lock()
			received = append(received, raw)
			mu.Unlock()
		}
		w.WriteHeader(out.Code)
		w.Write(out.Body.Bytes())
	}))
	defer hub.Close()
	hu, _ := url.Parse(hub.URL)
	rec := newRecorder()
	meta := &fakeMeta{records: rec, templates: []map[string]any{{"id": "1", "name": "first", "language": "en_US", "status": "PENDING"}}}
	tr := newTransport(hu, hu, rec)
	defer tr.base.CloseIdleConnections()
	s := &simulation{wa: wa, transport: tr, meta: meta, hub: hu, token: strings.Repeat("x", 32)}
	call := func(path, body, token string, want int) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.control(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w
	}
	call("/_sim/faults", `{"target":"graph","faults":["429"]}`, "invalid", 401)
	call("/_sim/faults", `{"target":"graph","faults":["wrong_ack"]}`, s.token, 400)
	payload := `{"object":"whatsapp_business_account","entry":[{"id":"220","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"110"},"messages":[{"id":"wamid.in","from":"919999999999","type":"text","text":{"body":"Hello"}}]}}]}]}`
	out := call("/_sim/emit", `{"payload":`+payload+`}`, s.token, 200)
	if !strings.Contains(out.Body.String(), `"status":200`) {
		t.Fatal(out.Body.String())
	}
	out = call("/_sim/emit", `{"invalid_signature":true,"payload":`+payload+`}`, s.token, 200)
	if !strings.Contains(out.Body.String(), `"status":403`) {
		t.Fatal(out.Body.String())
	}
	call("/_sim/template", `{"id":"1","status":"APPROVED"}`, s.token, 200)
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 || !bytes.Contains(received[1], []byte("message_template_status_update")) {
		t.Fatal("signed template update missing")
	}
}

func TestConcurrentCaptures(t *testing.T) {
	rec := newRecorder()
	meta := &fakeMeta{records: rec}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v25.0/110/messages", strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer sim-provider-token")
			meta.ServeHTTP(httptest.NewRecorder(), r)
			rec.snapshot()
		}()
	}
	wg.Wait()
	if len(rec.snapshot()) != 20 {
		t.Fatal("capture lost")
	}
	if time.Since(rec.start) <= 0 {
		t.Fatal("invalid monotonic clock")
	}
}

type memoryEvents struct{}

func (*memoryEvents) Save(context.Context, string, []byte) error                    { return nil }
func (*memoryEvents) List(context.Context, string, int64) ([]whatsapp.Event, error) { return nil, nil }

func TestMetaRecordsRefusedRequests(t *testing.T) {
	records := newRecorder()
	meta := &fakeMeta{records: records}
	for _, tc := range []struct {
		path, body, token string
		status            int
	}{
		{"/v25.0/110/messages", `{}`, "wrong", 401},
		{"/unknown", `{}`, "sim-provider-token", 404},
		{"/v25.0/110/messages", strings.Repeat("x", (2<<20)+1), "sim-provider-token", 413},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		res := httptest.NewRecorder()
		meta.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatal(res.Code, tc.status)
		}
	}
	if len(records.snapshot()) != 3 || meta.messages != 0 {
		t.Fatal("refusal not recorded or generated provider ID")
	}
}
