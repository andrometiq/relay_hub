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
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

// bulkCore is a signed Core fixture that records what each callback lane sent.
type bulkCore struct {
	mu        sync.Mutex
	t         *testing.T
	secret    string
	latest    map[string]whatsapp.BulkUpdate
	batches   []time.Time
	sizes     []int
	statuses  int
	delivery  int
	replies   map[string]time.Time
	statusMax int
}

func (c *bulkCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
	mac.Write(raw)
	if !hmac.Equal([]byte(r.Header.Get("X-Relay-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		c.t.Error("unsigned Core callback")
		w.WriteHeader(403)
		return
	}
	var batch struct {
		Updates  []whatsapp.BulkUpdate    `json:"updates"`
		Statuses []whatsapp.CompactStatus `json:"statuses"`
		Events   []whatsapp.Event         `json:"events"`
	}
	if json.Unmarshal(raw, &batch) != nil {
		w.WriteHeader(400)
		return
	}
	now := time.Now()
	c.mu.Lock()
	accepted := 0
	switch r.URL.Path {
	case "/api/method/relay_core.inbound.receive_bulk_updates":
		if len(batch.Updates) == 0 || len(batch.Updates) > 500 || !slices.IsSortedFunc(batch.Updates, func(a, b whatsapp.BulkUpdate) int { return int(a.ID - b.ID) }) {
			c.t.Error("bulk batch out of contract", len(batch.Updates))
		}
		for _, u := range batch.Updates {
			c.latest[u.Key] = u
		}
		c.batches = append(c.batches, now)
		c.sizes = append(c.sizes, len(batch.Updates))
		accepted = len(batch.Updates)
	case "/api/method/relay_core.inbound.receive_statuses":
		c.statuses += len(batch.Statuses)
		accepted = len(batch.Statuses)
	case "/api/method/relay_core.inbound.receive":
		for _, event := range batch.Events {
			var envelope struct {
				Object string `json:"object"`
				Entry  []struct {
					Changes []struct {
						Value struct {
							Messages []struct {
								ID string `json:"id"`
							} `json:"messages"`
						} `json:"value"`
					} `json:"changes"`
				} `json:"entry"`
			}
			json.Unmarshal(event.Payload, &envelope)
			if envelope.Object == "relay_delivery" {
				c.delivery++
			}
			for _, entry := range envelope.Entry {
				for _, change := range entry.Changes {
					for _, message := range change.Value.Messages {
						if _, seen := c.replies[message.ID]; !seen {
							c.replies[message.ID] = now
						}
					}
				}
			}
		}
		accepted = len(batch.Events)
	}
	c.mu.Unlock()
	fmt.Fprintf(w, `{"message":{"accepted":%d}}`, accepted)
}

type bulkMeta struct {
	mu   sync.Mutex
	sent []string
	ids  map[string]string
	n    int
	last time.Time
}

func (m *bulkMeta) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To       string `json:"to"`
		Type     string `json:"type"`
		Tracking string `json:"biz_opaque_callback_data"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json")
	if body.Type != "template" || !strings.HasPrefix(body.Tracking, "relay-outbox:") {
		w.WriteHeader(400)
		return
	}
	if strings.HasSuffix(body.To, "0007") {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"(#131009) Parameter value is not valid","type":"OAuthException","code":131009,"error_user_title":"Parameter value is not valid","error_user_msg":"Header image URL could not be downloaded.\nCheck the link.","fbtrace_id":"secret-trace"}}`)
		return
	}
	m.mu.Lock()
	m.n++
	m.last = time.Now()
	id := "wamid.bulk." + strconv.Itoa(m.n)
	m.sent = append(m.sent, id)
	m.ids[id] = body.To + "|" + body.Tracking
	m.mu.Unlock()
	fmt.Fprintf(w, `{"messages":[{"id":%q}]}`, id)
}

func (m *bulkMeta) take() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sent
	m.sent = nil
	return out
}

func envInt(key string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(key)); err == nil && value > 0 {
		return value
	}
	return fallback
}

// TestWhatsAppBulkCampaignSimulation pushes a campaign through the real MCP
// tool, outbox worker, webhook ingress and callback lanes. Meta and Core are
// local fixtures. RELAY_TEST_BULK=5000 runs the full-size proof.
func TestWhatsAppBulkCampaignSimulation(t *testing.T) {
	total := envInt("RELAY_TEST_BULK", 300)
	window := time.Duration(envInt("RELAY_TEST_BULK_WINDOW_SECONDS", 2)) * time.Second
	rate := envInt("RELAY_TEST_BULK_RATE", 80)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(total/rate+120)*time.Second+3*window)
	defer cancel()
	db, scopedDSN := simulationDatabase(t, ctx)
	store := &auth.Store{DB: db}
	if err := store.Bootstrap(ctx, "bulk-admin", "synthetic-bulk-password-42"); err != nil {
		t.Fatal(err)
	}
	session, _ := store.Login(ctx, "bulk-admin", "synthetic-bulk-password-42", "bulk")
	admin, _ := store.Session(ctx, session)
	if _, err := db.Exec(ctx, `INSERT INTO relay_accounts(account_id,site) VALUES('bulk','https://fixture.invalid')`); err != nil {
		t.Fatal(err)
	}
	store.Grant(ctx, admin, admin.ID, "bulk", true)
	token, err := store.Issue(ctx, admin, "core", "bulk", []string{"messages:send", "deliveries:read"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, _ := store.Issue(ctx, admin, "reader", "bulk", []string{"messages:read"}, 1)

	core := &bulkCore{t: t, secret: strings.Repeat("bulk-secret-", 4), latest: map[string]whatsapp.BulkUpdate{}, replies: map[string]time.Time{}}
	meta := &bulkMeta{ids: map[string]string{}}
	coreServer := httptest.NewServer(core)
	defer coreServer.Close()
	client := &http.Client{Timeout: 3 * time.Second, Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		switch {
		case r.URL.Host == "graph.facebook.com":
			meta.ServeHTTP(recorder, r)
		case strings.HasPrefix(coreServer.URL, "http://"+r.URL.Host):
			core.ServeHTTP(recorder, r)
		default:
			return nil, fmt.Errorf("simulation refused unexpected HTTP destination")
		}
		return recorder.Result(), nil
	})}
	account := whatsapp.Account{ID: "bulk", Site: coreServer.URL, AccessToken: "fake-provider", Version: "v25.0", PhoneID: "110", WABAID: "220", AppSecret: "fake-app-secret", VerifyToken: "fake-verify", CoreAPIKey: "key", CoreAPISecret: "secret", CallbackSecret: core.secret, MessagesPerSecond: rate, Templates: true}
	wa, err := whatsapp.NewWithHTTPClient([]whatsapp.Account{account}, client)
	if err != nil {
		t.Fatal(err)
	}
	wa.Events = whatsapp.PostgresEvents{URL: scopedDSN, Pool: db}
	wa.Outbox, err = whatsapp.NewOutbox(ctx, scopedDSN, wa, envInt("RELAY_TEST_BULK_WORKERS", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer wa.Outbox.Close()
	handler, err := NewAuthenticated(db.Ping, wa, store.Authenticate)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	wa.Outbox.Run(runCtx)
	(&whatsapp.CallbackDispatcher{URL: scopedDSN, Service: wa, BulkWindow: window}).Run(runCtx)

	call := func(bearer string, messages []map[string]any) (int, map[string]any) {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "whatsapp.bulk", "arguments": map[string]any{"messages": messages}}})
		r := httptest.NewRequest("POST", "http://localhost/mcp", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+bearer)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var envelope struct {
			Result struct {
				IsError    bool           `json:"isError"`
				Structured map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		json.Unmarshal(w.Body.Bytes(), &envelope)
		if envelope.Result.IsError {
			return w.Code, nil
		}
		return w.Code, envelope.Result.Structured
	}
	recipient := func(row int) map[string]any {
		to := fmt.Sprintf("9198%08d", row)
		if row == 2 {
			to = "12"
		}
		return map[string]any{"key": fmt.Sprintf("winter-launch-%d", row), "message": map[string]any{
			"operation": "templates.send", "to": to, "name": "winter_launch", "language": "en",
			"components": []map[string]any{{"type": "header", "parameters": []map[string]any{{"type": "image", "image": map[string]string{"link": "https://example.test/winter.jpg"}}}}, {"type": "body", "parameters": []map[string]any{{"type": "text", "parameter_name": "name", "text": fmt.Sprintf("Customer %d", row)}}}},
		}}
	}

	if code, result := call(readOnly, []map[string]any{recipient(1)}); code != 200 || result != nil {
		t.Fatal("read-only token queued a bulk send", code)
	}
	inline := map[string]any{"operation": "messages.media", "to": "919800000001", "media_type": "document", "mime_type": "application/pdf", "content_base64": "JVBERi0xLjQK"}
	if code, result := call(token, []map[string]any{{"key": "inline-row", "message": inline}}); code != 200 || !strings.Contains(result["deliveries"].([]any)[0].(map[string]any)["error"].(string), "media_id or media_url") {
		t.Fatal("bulk accepted inline media bytes", result)
	}
	tooMany := make([]map[string]any, 51)
	for i := range tooMany {
		tooMany[i] = recipient(10000 + i)
	}
	if _, result := call(token, tooMany); result != nil {
		t.Fatal("bulk accepted more than 50 messages")
	}

	var deadlocksBefore int
	db.QueryRow(ctx, "SELECT deadlocks FROM pg_stat_database WHERE datname=current_database()").Scan(&deadlocksBefore)
	started := time.Now()
	peakQueue := 0
	for first := 1; first <= total; first += 50 {
		batch := []map[string]any{}
		for row := first; row < first+50 && row <= total; row++ {
			batch = append(batch, recipient(row))
		}
		_, result := call(token, batch)
		if result == nil {
			t.Fatal("bulk batch refused")
		}
		if first == 1 {
			// Retrying with the same keys returns the same rows, never a second send.
			_, again := call(token, batch)
			for i, row := range again["deliveries"].([]any) {
				before := result["deliveries"].([]any)[i].(map[string]any)
				if row.(map[string]any)["id"] != before["id"] || row.(map[string]any)["key"] != before["key"] {
					t.Fatal("bulk retry was not idempotent", row, before)
				}
			}
			refused := result["deliveries"].([]any)[1].(map[string]any)
			if refused["state"] != "rejected" || refused["id"] != "" || !strings.Contains(refused["error"].(string), "recipient") {
				t.Fatal("invalid recipient did not get its own rejection", refused)
			}
		}
		if result["messages_per_second"] != float64(rate) {
			t.Fatal("bulk reply lacks the account send rate", result["messages_per_second"])
		}
		queued := int(result["queued"].(float64))
		peakQueue = max(peakQueue, queued)
		// Core-side backpressure: wait while more than 1,000 rows are queued.
		for queued > 1000 {
			time.Sleep(time.Duration(queued-500) * time.Second / time.Duration(rate))
			queued = 0
			var count int
			db.QueryRow(ctx, "SELECT count(*) FROM relay_outbox WHERE state IN ('queued','submitting')").Scan(&count)
			queued = count
		}
	}
	submitted := time.Since(started)

	// Meta webhooks: sent+delivered for each message, read for half, failed for some, and
	// replies quoting the campaign message for one recipient in 25.
	webhook := func(payload map[string]any) {
		raw, _ := json.Marshal(payload)
		mac := hmac.New(sha256.New, []byte(account.AppSecret))
		mac.Write(raw)
		for attempt := 0; attempt < 20; attempt++ {
			r := httptest.NewRequest("POST", "http://localhost/webhooks/whatsapp", bytes.NewReader(raw))
			r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			w := httptest.NewRecorder()
			wa.Webhook(w, r)
			if w.Code == 200 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Error("webhook was not accepted")
	}
	envelope := func(value map[string]any) map[string]any {
		value["metadata"] = map[string]string{"phone_number_id": "110"}
		return map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "220", "changes": []any{map[string]any{"field": "messages", "value": value}}}}}
	}
	replySent := map[string]time.Time{}
	expected := map[string]string{}
	emitted := 0
	stamp := time.Now().Unix()
	for emitted < total-2 {
		ids := meta.take()
		if len(ids) == 0 {
			if ctx.Err() != nil {
				t.Fatal("campaign did not finish sending")
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		for _, id := range ids {
			emitted++
			meta.mu.Lock()
			to, tracking, _ := strings.Cut(meta.ids[id], "|")
			meta.mu.Unlock()
			sequence, _ := strconv.Atoi(strings.TrimPrefix(id, "wamid.bulk."))
			status := func(state string, extra map[string]any) map[string]any {
				row := map[string]any{"id": id, "recipient_id": to, "status": state, "timestamp": strconv.FormatInt(stamp, 10), "biz_opaque_callback_data": tracking}
				for k, v := range extra {
					row[k] = v
				}
				return row
			}
			statuses := []any{status("sent", nil)}
			expected[id] = "sent"
			switch {
			case sequence%250 == 0:
				statuses = append(statuses, status("failed", map[string]any{"errors": []any{map[string]any{"code": 131049, "title": "This message was not delivered to maintain healthy ecosystem engagement.", "message": "This message was not delivered", "error_data": map[string]string{"details": "In order to maintain a healthy ecosystem engagement, the message failed to be delivered."}}}}))
				expected[id] = "failed"
			case sequence%2 == 0:
				statuses = append(statuses, status("delivered", nil), status("read", nil))
				expected[id] = "read"
			default:
				statuses = append(statuses, status("delivered", nil))
				expected[id] = "delivered"
			}
			webhook(envelope(map[string]any{"statuses": statuses}))
			if sequence%25 == 0 {
				reply := fmt.Sprintf("wamid.reply.%d", sequence)
				replySent[reply] = time.Now()
				webhook(envelope(map[string]any{"contacts": []any{map[string]any{"wa_id": to}}, "messages": []any{map[string]any{"id": reply, "from": to, "timestamp": strconv.FormatInt(stamp, 10), "type": "text", "text": map[string]string{"body": "Interested"}, "context": map[string]string{"from": "110", "id": id}}}}))
			}
		}
	}
	emittedAt := time.Since(started)

	deadline := time.Now().Add(3*window + 30*time.Second)
	for {
		core.mu.Lock()
		done := len(core.latest) >= total-1 && len(core.replies) >= len(replySent)
		for _, u := range core.latest {
			if u.MessageID != "" && u.Status != expected[u.MessageID] {
				done = false
			}
		}
		core.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			core.mu.Lock()
			t.Fatalf("Core did not receive final campaign state: %d/%d rows, %d/%d replies", len(core.latest), total-1, len(core.replies), len(replySent))
		}
		time.Sleep(100 * time.Millisecond)
	}
	finished := time.Since(started)
	// Core has the final state; Hub commits its acknowledgement just after.
	for wait := 0; wait < 50; wait++ {
		var left int
		if db.QueryRow(ctx, "SELECT count(*) FROM relay_bulk_pending").Scan(&left); left == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()

	core.mu.Lock()
	defer core.mu.Unlock()
	rejected := 0
	for key, u := range core.latest {
		if u.State == "rejected" {
			rejected++
			var detail struct {
				Code  int    `json:"code"`
				Title string `json:"title"`
				Msg   string `json:"message"`
			}
			if json.Unmarshal(u.Error, &detail) != nil || detail.Code != 131009 || detail.Title != "Parameter value is not valid" || detail.Msg != "Header image URL could not be downloaded. Check the link." || strings.Contains(string(u.Error), "trace") {
				t.Fatalf("rejected row %s lacks safe Meta error detail: %s", key, u.Error)
			}
		}
		if u.Status == "failed" && !strings.Contains(string(u.Error), `"code":131049`) {
			t.Fatalf("failed receipt lacks Meta error code: %s", u.Error)
		}
	}
	if rejected == 0 || core.statuses != 0 || core.delivery != 0 {
		t.Fatalf("bulk results leaked into per-event lanes or rejections missing: rejected=%d statuses=%d deliveries=%d", rejected, core.statuses, core.delivery)
	}
	for i := 1; i < len(core.batches); i++ {
		if gap := core.batches[i].Sub(core.batches[i-1]); core.sizes[i] < 500 && gap < window-1500*time.Millisecond {
			t.Errorf("partial bulk batch %d sent %v after the previous one; window is %v", i, gap, window)
		}
	}
	latencies := []time.Duration{}
	for id, sent := range replySent {
		latencies = append(latencies, core.replies[id].Sub(sent))
	}
	slices.Sort(latencies)
	p95 := latencies[max(0, (len(latencies)*95+99)/100-1)]
	if p95 > time.Second {
		t.Errorf("reply p95 %v exceeds 1s during the campaign", p95)
	}
	var pending, deadlocks int
	db.QueryRow(context.Background(), "SELECT count(*) FROM relay_bulk_pending").Scan(&pending)
	db.QueryRow(context.Background(), "SELECT deadlocks FROM pg_stat_database WHERE datname=current_database()").Scan(&deadlocks)
	if pending != 0 {
		t.Errorf("%d bulk updates were never acknowledged", pending)
	}
	if deadlocks != deadlocksBefore {
		t.Errorf("%d database deadlocks during the campaign", deadlocks-deadlocksBefore)
	}
	sum := 0
	for _, size := range core.sizes {
		sum += size
	}
	sendTime := meta.last.Sub(started)
	t.Logf("bulk simulation: %d recipients, rate limit %d/s, %d outbox workers, window %v; admitted in %v; provider sends done in %v (%.0f msg/s); receipts emitted by %v; final state at Core in %v; peak queue %d",
		total, rate, wa.Outbox.Workers, window, submitted.Round(time.Millisecond), sendTime.Round(time.Millisecond), float64(meta.n)/sendTime.Seconds(), emittedAt.Round(time.Millisecond), finished.Round(time.Millisecond), peakQueue)
	t.Logf("bulk callbacks: %d batches, %d rows (avg %.0f, max %d) for %d recipients; status-lane rows %d; realtime delivery events %d; new database deadlocks %d",
		len(core.sizes), sum, float64(sum)/float64(max(1, len(core.sizes))), slices.Max(core.sizes), total, core.statuses, core.delivery, deadlocks-deadlocksBefore)
	t.Logf("replies during campaign: %d, p50 %v, p95 %v, max %v", len(latencies), latencies[len(latencies)/2].Round(time.Millisecond), p95.Round(time.Millisecond), latencies[len(latencies)-1].Round(time.Millisecond))
}
