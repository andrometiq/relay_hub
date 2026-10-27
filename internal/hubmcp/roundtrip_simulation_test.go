package hubmcp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5/pgxpool"
)

type simulationTransport func(*http.Request) (*http.Response, error)

func (f simulationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func simulationDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	cfg.MaxConns = 2
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := "relay_roundtrip_test_" + auth.Secret()[:16]
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, e := base.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error("could not remove simulation schema:", e)
		}
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	files, err := filepath.Glob("../database/migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatal("migration files unavailable", err)
	}
	for _, file := range files {
		raw, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, string(raw)); e != nil {
			t.Fatalf("migration %s: %v", filepath.Base(file), e)
		}
	}
	scopedDSN := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, e := url.Parse(dsn)
		if e != nil {
			t.Fatal("invalid simulation database URL")
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		scopedDSN = parsed.String()
	}
	return db, scopedDSN
}

// This is an integrated wire/DB simulation, not evidence of live Meta service
// acceptance. It uses real Hub auth, MCP transport, outbox workers, webhook
// validation, PostgreSQL projections and callback dispatch. Only Meta and Core
// are local HTTP fixtures; no provider credentials or external network is used.
func TestWhatsAppAuthenticatedRoundTripSimulation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, scopedDSN := simulationDatabase(t, ctx)
	store := &auth.Store{DB: db}
	password := "synthetic-simulation-password-42"
	if err := store.Bootstrap(ctx, "simulation-admin", password); err != nil {
		t.Fatal(err)
	}
	session, err := store.Login(ctx, "simulation-admin", password, "simulation")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(account, label string, scopes []string) string {
		if _, err := db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES($1,'https://fixture.invalid') ON CONFLICT DO NOTHING`, account); err != nil {
			t.Fatal(err)
		}
		t.Helper()
		if e := store.Grant(ctx, admin, admin.ID, account, true); e != nil {
			t.Fatal(e)
		}
		token, e := store.Issue(ctx, admin, label, account, scopes, 1)
		if e != nil {
			t.Fatal(e)
		}
		return token
	}
	scopes := []string{"messages:send", "messages:read", "deliveries:read", "deliveries:cancel"}
	tokenA := issue("sim-a", "core-a", scopes)
	tokenB := issue("sim-b", "core-b", scopes)
	readToken := issue("sim-a", "read-only", []string{"messages:read"})

	type callbackBatch struct {
		Account  string                   `json:"account"`
		Events   []whatsapp.Event         `json:"events"`
		Statuses []whatsapp.CompactStatus `json:"statuses"`
	}
	var callbackMu sync.Mutex
	var statusBatches []int
	accepted := map[string]map[int64]bool{"sim-a": {}, "sim-b": {}}
	var statusFailed atomic.Bool
	var realtimeReceived atomic.Bool
	var callbackRequests atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callbackRequests.Add(1)
		raw, e := io.ReadAll(r.Body)
		var batch callbackBatch
		if e != nil || json.Unmarshal(raw, &batch) != nil || (len(batch.Events) == 0 && len(batch.Statuses) == 0) || len(batch.Events) > 50 || len(batch.Statuses) > 500 {
			t.Error("Core fixture received an invalid callback batch")
			w.WriteHeader(400)
			return
		}
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if accepted[batch.Account] == nil || (r.URL.Path != "/api/method/relay_core.inbound.receive" && r.URL.Path != "/api/method/relay_core.inbound.receive_statuses") {
			t.Error("Core callback routed to an unknown account/path")
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("Authorization") != "token key-"+batch.Account+":secret-"+batch.Account {
			t.Error("Core callback used another account's credentials")
			w.WriteHeader(403)
			return
		}
		mac := hmac.New(sha256.New, []byte(strings.Repeat(batch.Account, 8)))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if !hmac.Equal([]byte(r.Header.Get("X-Relay-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("invalid Hub -> Core HMAC signature")
			w.WriteHeader(403)
			return
		}
		isStatus := len(batch.Statuses) > 0
		if isStatus {
			statusBatches = append(statusBatches, len(batch.Statuses))
			// Account callbacks run concurrently. Fail sim-a's large batch,
			// regardless of whether sim-b's single receipt arrives first.
			if batch.Account == "sim-a" && statusFailed.CompareAndSwap(false, true) {
				w.WriteHeader(503)
				return
			}
		} else {
			realtimeReceived.Store(true)
		}
		for _, event := range batch.Events {
			var owner string
			var persisted []byte
			if e := db.QueryRow(r.Context(), "SELECT account,payload FROM relay_whatsapp_events WHERE id=$1", event.ID).Scan(&owner, &persisted); e != nil || owner != batch.Account {
				t.Error("callback crossed an account boundary", e)
				w.WriteHeader(403)
				return
			}
			var expected, actual any
			if json.Unmarshal(persisted, &expected) != nil || json.Unmarshal(event.Payload, &actual) != nil || !reflect.DeepEqual(expected, actual) {
				t.Error("callback payload differs from the durable provider event")
				w.WriteHeader(400)
				return
			}
			accepted[batch.Account][event.ID] = true
		}
		for _, status := range batch.Statuses {
			var owner string
			var persisted []byte
			if e := db.QueryRow(r.Context(), "SELECT account,payload FROM relay_whatsapp_events WHERE id=$1", status.EventID).Scan(&owner, &persisted); e != nil || owner != batch.Account {
				t.Error("compact status crossed account boundary", e)
				w.WriteHeader(403)
				return
			}
			var original struct {
				Entry []struct {
					Changes []struct {
						Value struct {
							Statuses []struct {
								ID        string `json:"id"`
								Recipient string `json:"recipient_id"`
								Status    string `json:"status"`
								Timestamp string `json:"timestamp"`
							} `json:"statuses"`
						} `json:"value"`
					} `json:"changes"`
				} `json:"entry"`
			}
			if json.Unmarshal(persisted, &original) != nil {
				t.Error("invalid persisted receipt")
				w.WriteHeader(400)
				return
			}
			receipt := original.Entry[0].Changes[0].Value.Statuses[0]
			if status.Position != 0 || status.MessageID != receipt.ID || status.Recipient != receipt.Recipient || status.Status != receipt.Status || fmt.Sprint(status.Timestamp) != receipt.Timestamp {
				t.Error("compact receipt changed persisted fields")
				w.WriteHeader(400)
				return
			}
			accepted[batch.Account][status.EventID] = true
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(batch.Events)+len(batch.Statuses))
	}))
	defer core.Close()
	accounts := []whatsapp.Account{}
	for i, name := range []string{"sim-a", "sim-b"} {
		accounts = append(accounts, whatsapp.Account{ID: name, Site: core.URL, AccessToken: "fake-provider-" + name, Version: "v25.0", PhoneID: fmt.Sprint(110 + i), WABAID: fmt.Sprint(220 + i), AppSecret: "fake-app-" + name, VerifyToken: "fake-verify-" + name, CoreAPIKey: "key-" + name, CoreAPISecret: "secret-" + name, CallbackSecret: strings.Repeat(name, 8), MessagesPerSecond: 1000})
	}
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			To       string `json:"to"`
			Tracking string `json:"biz_opaque_callback_data"`
			Text     struct {
				Body string `json:"body"`
			} `json:"text"`
		}
		var owner string
		for _, a := range accounts {
			if r.URL.Path == "/v25.0/"+a.PhoneID+"/messages" && r.Header.Get("Authorization") == "Bearer "+a.AccessToken {
				owner = a.ID
			}
		}
		if r.Method != "POST" || owner == "" || json.NewDecoder(r.Body).Decode(&body) != nil || !strings.HasPrefix(body.Tracking, "relay-outbox:") || body.To != "919999999999" || !strings.HasPrefix(body.Text.Body, "Synthetic message") {
			t.Error("invalid outbound Meta request or credentials/correlation missing")
			w.WriteHeader(400)
			return
		}
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"messages":[{"id":"wamid.%s.%s"}]}`, owner, strings.TrimPrefix(body.Tracking, "relay-outbox:"))
	}))
	defer provider.Close()
	providerURL, _ := url.Parse(provider.URL)
	coreURL, _ := url.Parse(core.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	// Deny all destinations except our two local fixtures. This also prevents a
	// future adapter regression from accidentally contacting a real provider.
	client := &http.Client{Timeout: 3 * time.Second, Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "graph.facebook.com" && r.URL.Scheme == "https" {
			clone := r.Clone(r.Context())
			clone.URL.Scheme, clone.URL.Host = providerURL.Scheme, providerURL.Host
			clone.Host = providerURL.Host
			return transport.RoundTrip(clone)
		}
		if r.URL.Host == coreURL.Host && r.URL.Scheme == coreURL.Scheme {
			return transport.RoundTrip(r)
		}
		return nil, fmt.Errorf("simulation refused unexpected HTTP destination")
	})}
	wa, err := whatsapp.NewWithHTTPClient(accounts, client)
	if err != nil {
		t.Fatal(err)
	}
	wa.Events = whatsapp.PostgresEvents{URL: scopedDSN}
	wa.Outbox = &whatsapp.Outbox{URL: scopedDSN, Service: wa, Workers: 1}
	handler, err := NewAuthenticated(db.Ping, wa, store.Authenticate)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("/webhooks/whatsapp/{account}", wa.Webhook)
	hub := httptest.NewServer(mux)
	defer hub.Close()
	singleCall := func(token, tool string, arguments any, wantError bool) []whatsapp.Delivery {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}})
		req, _ := http.NewRequestWithContext(ctx, "POST", hub.URL+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		res, e := hub.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized && wantError {
			return nil
		}
		var envelope struct {
			Result struct {
				IsError    bool `json:"isError"`
				Structured struct {
					Deliveries []whatsapp.Delivery `json:"deliveries"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if e = json.NewDecoder(res.Body).Decode(&envelope); e != nil || res.StatusCode != 200 || envelope.Result.IsError != wantError {
			t.Fatalf("MCP %s: HTTP=%d, isError=%v, wanted error=%v, decode=%v", tool, res.StatusCode, envelope.Result.IsError, wantError, e)
		}
		return envelope.Result.Structured.Deliveries
	}
	call := func(token, tool string, arguments any, wantError bool) []whatsapp.Delivery {
		if tool != "whatsapp.bulk" {
			return singleCall(token, tool, arguments, wantError)
		}
		var deliveries []whatsapp.Delivery
		for _, item := range arguments.(map[string]any)["messages"].([]whatsapp.Submission) {
			deliveries = append(deliveries, singleCall(token, "whatsapp.send", map[string]any{"message": item}, wantError)...)
		}
		return deliveries
	}
	batch := []whatsapp.Submission{}
	for i := 0; i < 3; i++ {
		batch = append(batch, whatsapp.Submission{Key: fmt.Sprintf("request-%d", i), Message: whatsapp.Input{Operation: "messages.send", To: "919999999999", Body: fmt.Sprintf("Synthetic message %d", i)}})
	}
	args := map[string]any{"messages": batch}
	call("invalid-token", "whatsapp.bulk", args, true)
	call(session, "whatsapp.bulk", args, true)
	call(readToken, "whatsapp.bulk", args, true)
	first := call(tokenA, "whatsapp.bulk", args, false)
	replay := call(tokenA, "whatsapp.bulk", args, false)
	if len(first) != 3 || len(replay) != 3 {
		t.Fatal("bulk MCP did not persist three deliveries")
	}
	for i := range first {
		if first[i].ID != replay[i].ID || first[i].State != "queued" {
			t.Fatal("MCP replay duplicated or synchronously sent a delivery")
		}
	}
	changed := append([]whatsapp.Submission(nil), batch...)
	changed[2].Message.Body = "Synthetic message with conflicting payload"
	call(tokenA, "whatsapp.bulk", map[string]any{"messages": changed[2:]}, true)
	other := call(tokenB, "whatsapp.send", map[string]any{"message": batch[0]}, false)
	if len(other) != 1 || other[0].ID == first[0].ID || providerCalls.Load() != 0 {
		t.Fatal("account idempotency scope or asynchronous enqueue failed")
	}
	call(tokenB, "whatsapp.deliveries", map[string]any{"cancel_id": first[0].ID}, true)
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	wa.Outbox.Run(workerCtx)
	wait := func(label string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(label, ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatal("timed out waiting for", label)
	}
	wait("four outbox submissions", func() bool {
		var count int
		return db.QueryRow(ctx, "SELECT count(*) FROM relay_outbox WHERE state='submitted'").Scan(&count) == nil && count == 4
	})
	stopWorkers()
	if providerCalls.Load() != 4 {
		t.Fatal("provider received duplicate or missing messages")
	}
	if got := call(tokenB, "whatsapp.deliveries", map[string]any{}, false); len(got) != 1 || got[0].ID != other[0].ID {
		t.Fatal("MCP deliveries leaked another account")
	}
	webhook := func(a whatsapp.Account, payload []byte, signature string, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "POST", hub.URL+"/webhooks/whatsapp/"+a.ID, bytes.NewReader(payload))
		if signature == "" {
			mac := hmac.New(sha256.New, []byte(a.AppSecret))
			mac.Write(payload)
			signature = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		req.Header.Set("X-Hub-Signature-256", signature)
		res, e := hub.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("webhook status %d, want %d", res.StatusCode, want)
		}
	}
	wrap := func(a whatsapp.Account, value any) []byte {
		raw, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": a.WABAID, "changes": []any{map[string]any{"field": "messages", "value": value}}}}})
		return raw
	}
	status := func(n int, state string, a whatsapp.Account, id string) []byte {
		return wrap(a, map[string]any{"metadata": map[string]any{"phone_number_id": a.PhoneID}, "statuses": []any{map[string]any{"id": "wamid." + a.ID + "." + id, "status": state, "recipient_id": "919999999999", "timestamp": fmt.Sprint(1000 + n), "biz_opaque_callback_data": "relay-outbox:" + id}}})
	}
	firstStatus := status(0, "read", accounts[0], first[0].ID)
	webhook(accounts[0], firstStatus, "sha256=forged", 403)
	// Another container's valid signature cannot reach this number: kept unrouted.
	webhook(accounts[1], firstStatus, "", 200)
	for i := 0; i < 52; i++ {
		state := "delivered"
		if i == 0 {
			state = "read"
		}
		raw := status(i, state, accounts[0], first[i%3].ID)
		webhook(accounts[0], raw, "", 200)
		webhook(accounts[0], raw, "", 200)
	}
	webhook(accounts[1], status(0, "sent", accounts[1], other[0].ID), "", 200)
	incoming := wrap(accounts[0], map[string]any{"metadata": map[string]any{"phone_number_id": accounts[0].PhoneID}, "messages": []any{map[string]any{"id": "wamid.inbound", "from": "919999999999", "type": "text", "text": map[string]any{"body": "Synthetic customer reply"}}}})
	webhook(accounts[0], incoming, "", 200)
	webhook(accounts[0], incoming, "", 200)
	var events, incomingCount int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM relay_whatsapp_events WHERE account<>''").Scan(&events); err != nil || events != 58 {
		t.Fatal("durable webhook dedupe failed", events, err)
	}
	if err = db.QueryRow(ctx, "SELECT count(*) FROM relay_whatsapp_incoming").Scan(&incomingCount); err != nil || incomingCount != 1 {
		t.Fatal("incoming projection dedupe failed", incomingCount, err)
	}
	deliveries := call(tokenA, "whatsapp.deliveries", map[string]any{}, false)
	if len(deliveries) != 3 || deliveries[0].DeliveryStatus != "read" || deliveries[1].DeliveryStatus != "delivered" || deliveries[2].DeliveryStatus != "delivered" {
		t.Fatal("correlation or out-of-order status projection failed", deliveries)
	}

	callbackCtx, stopCallbacks := context.WithCancel(ctx)
	defer stopCallbacks()
	dispatcher := &whatsapp.CallbackDispatcher{URL: scopedDSN, Service: wa}
	dispatcher.Run(callbackCtx)
	wait("realtime callback while statuses retry", func() bool { return realtimeReceived.Load() && statusFailed.Load() })
	var cursor int64
	if err = db.QueryRow(ctx, "SELECT last_id FROM relay_callback_cursor WHERE account='sim-a' AND lane='status'").Scan(&cursor); err != nil || cursor != 0 {
		t.Fatal("failed status callback advanced its cursor", cursor, err)
	}
	wait("all callback acknowledgements", func() bool {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		return len(accepted["sim-a"]) == 56 && len(accepted["sim-b"]) == 2
	})
	wait("durable callback cursor acknowledgements", func() bool {
		var count int
		return db.QueryRow(ctx, `SELECT count(*) FROM (SELECT account,lane,max(id) AS id FROM relay_whatsapp_events GROUP BY account,lane) e JOIN relay_callback_cursor c ON c.account=e.account AND c.lane=e.lane AND c.last_id=e.id`).Scan(&count) == nil && count == 4
	})
	stopCallbacks()
	callbackMu.Lock()
	// All 52 statuses fit a compact batch; its failed attempt is retried intact.
	var fifty int
	for _, n := range statusBatches {
		if n == 52 {
			fifty++
		}
	}
	callbackMu.Unlock()
	if fifty != 2 {
		t.Fatalf("status batching/retry did not match expected 52,52: %v", statusBatches)
	}
	// A new dispatcher instance must read the persisted cursors, not depend on
	// process-local state or replay acknowledged callbacks after restart.
	beforeRestart := callbackRequests.Load()
	restartCtx, stopRestart := context.WithCancel(ctx)
	defer stopRestart()
	(&whatsapp.CallbackDispatcher{URL: scopedDSN, Service: wa}).Run(restartCtx)
	select {
	case <-time.After(2200 * time.Millisecond): // both account/status scheduler ticks
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopRestart()
	if callbackRequests.Load() != beforeRestart {
		t.Fatal("dispatcher restart replayed acknowledged events")
	}
	if err = store.Grant(ctx, admin, admin.ID, "sim-a", false); err != nil {
		t.Fatal(err)
	}
	call(tokenA, "whatsapp.bulk", args, true)
	if providerCalls.Load() != 4 {
		t.Fatal("revoked credential or webhook retry resent a message")
	}
	t.Log("PASS: real PostgreSQL auth/MCP/outbox -> local Meta -> signed webhook/status/inbound -> signed Core callback; 4 sends, 54 provider events and 4 send outcomes, payload comparison, duplicate suppression, 50+2 status batching/retry, realtime lane, callback restart, tenant isolation and revocation")
}

// Admission simulation measures chunked ingestion and durable queue semantics.
// It deliberately does not start workers: this is not a provider throughput
// benchmark and cannot be interpreted as a claim of 2,000 external deliveries.
func TestBulk2000MessageAdmissionSimulation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, scopedDSN := simulationDatabase(t, ctx)
	accounts := []whatsapp.Account{}
	for i, account := range []string{"bulk-a", "bulk-b"} {
		accounts = append(accounts, whatsapp.Account{ID: account, Site: "http://127.0.0.1:1", AccessToken: "synthetic-" + account, PhoneID: fmt.Sprint(510 + i), WABAID: fmt.Sprint(610 + i), Version: "v25.0"})
	}
	var networkCalls atomic.Int32
	wa, err := whatsapp.NewWithHTTPClient(accounts, &http.Client{Transport: simulationTransport(func(*http.Request) (*http.Response, error) {
		networkCalls.Add(1)
		return nil, fmt.Errorf("admission simulation forbids provider requests")
	})})
	if err != nil {
		t.Fatal(err)
	}
	wa.Outbox = &whatsapp.Outbox{URL: scopedDSN, Service: wa, Workers: 1}
	chunk := func(index int) []whatsapp.Submission {
		batch := make([]whatsapp.Submission, 50)
		for i := range batch {
			n := index*50 + i
			batch[i] = whatsapp.Submission{Key: fmt.Sprintf("request-%d", n), Message: whatsapp.Input{Operation: "messages.send", To: "919999999999", Body: fmt.Sprintf("Synthetic bulk %d: %s", n, strings.Repeat("x", 1024))}}
		}
		return batch
	}
	maxRequestBytes := 0
	submit := func(account string, batch []whatsapp.Submission, wantError bool) []whatsapp.Delivery {
		t.Helper()
		raw, _ := json.Marshal(batch)
		maxRequestBytes = max(maxRequestBytes, len(raw))
		a := accounts[0]
		if account == accounts[1].ID {
			a = accounts[1]
		}
		rows, err := wa.Outbox.Enqueue(ctx, a, batch)
		if (err != nil) != wantError {
			t.Fatalf("admission error=%v, want error=%v", err, wantError)
		}
		return rows
	}
	var before, memory runtime.MemStats
	runtime.ReadMemStats(&before)
	peakHeap := before.HeapAlloc
	started := time.Now()
	var head, tail []whatsapp.Delivery
	for i := 0; i < 40; i++ {
		deliveries := submit("bulk-a", chunk(i), false)
		if i == 0 {
			head = deliveries
		}
		if i == 39 {
			tail = deliveries
		}
		runtime.ReadMemStats(&memory)
		peakHeap = max(peakHeap, memory.HeapAlloc)
	}
	admissionTime := time.Since(started)
	var count, payloads int
	if err = db.QueryRow(ctx, `SELECT count(*), count(*) FILTER(WHERE payload->>'body'='Synthetic bulk '||substring(request_key from 9)||': '||repeat('x',1024)) FROM relay_outbox WHERE account='bulk-a' AND state='queued'`).Scan(&count, &payloads); err != nil || count != 2000 || payloads != 2000 {
		t.Fatal("bulk queue count or persisted payload comparison failed", count, payloads, err)
	}
	for _, pair := range []struct {
		index    int
		original []whatsapp.Delivery
	}{{0, head}, {39, tail}} {
		replay := submit("bulk-a", chunk(pair.index), false)
		for i := range replay {
			if replay[i].ID != pair.original[i].ID {
				t.Fatal("retry duplicated bulk delivery intent")
			}
		}
	}
	other := submit("bulk-b", chunk(0), false)
	if other[0].ID == head[0].ID {
		t.Fatal("bulk request key was shared across tenants")
	}
	oversized := append(chunk(0), whatsapp.Submission{Key: "overflow-item", Message: chunk(0)[0].Message})
	submit("bulk-a", oversized, true)
	conflict := chunk(0)
	conflict[49].Message.Body = "different payload under existing idempotency key"
	submit("bulk-a", conflict, true)
	if err = db.QueryRow(ctx, "SELECT count(*) FROM relay_outbox").Scan(&count); err != nil || count != 2050 || networkCalls.Load() != 0 {
		t.Fatal("duplicate/invalid batch changed backlog or contacted provider", count, networkCalls.Load(), err)
	}
	page, err := wa.Outbox.List(ctx, accounts[0], "")
	if err != nil || len(page) != 50 {
		t.Fatal("large backlog read is not bounded to 50 deliveries", len(page), err)
	}
	if maxRequestBytes > 128*1024 {
		t.Fatal("chunked submission unexpectedly constructed an oversized request", maxRequestBytes)
	}
	t.Logf("PASS: admission only; 2000 synthetic 1 KiB messages in 40x50 internal queue chunks: %s (%.1f intents/s), max request=%d bytes, sampled heap delta=%d bytes, total allocations=%d bytes; replay/conflict/account isolation and 50-row page verified; external sends=0", admissionTime, 2000/admissionTime.Seconds(), maxRequestBytes, peakHeap-before.HeapAlloc, memory.TotalAlloc-before.TotalAlloc)
}
