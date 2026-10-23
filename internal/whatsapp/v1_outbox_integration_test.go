package whatsapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSafeSendFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state string
	}{
		{"429", &ProviderError{Status: 429}, "queued"},
		{"transient", &ProviderError{Status: 500, Transient: true}, "queued"},
		{"rate code", &ProviderError{Status: 400, Code: 130429}, "queued"},
		{"pair limit", &ProviderError{Status: 400, Code: 131056}, "queued"},
		{"rejection", &ProviderError{Status: 400, Code: 100}, "rejected"},
		{"unknown 500", &ProviderError{Status: 500}, "uncertain"},
		{"timeout", errors.New("lost response"), "uncertain"},
		{"missing config", &deferredError{errors.New("not configured")}, "queued"},
		{"pacing DB", &deferredError{errors.New("db unavailable")}, "queued"},
		{"validation", &preSubmissionError{errors.New("invalid input")}, "rejected"},
		{"upload timeout", &preSubmissionError{&deferredError{errors.New("lost upload")}}, "queued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _, delay, _ := sendOutcome(tc.err, 0)
			if state != tc.state || (state == "queued" && delay <= 0) {
				t.Fatal(state, delay)
			}
		})
	}
	state, _, delay, metadata := sendOutcome(&ProviderError{Status: 429, Code: 130429, RetryAfter: 60}, 2)
	if state != "queued" || delay != time.Minute || !strings.Contains(string(metadata), "130429") {
		t.Fatal(state, delay, string(metadata))
	}
	state, _, _, _ = sendOutcome(&ProviderError{Status: 429}, 7)
	if state != "rejected" {
		t.Fatal("unbounded provider retries")
	}
}

func TestProviderErrorsRetainOnlySafeMetadata(t *testing.T) {
	for _, body := range []string{`{"error":{"code":130429,"error_subcode":42,"is_transient":true,"message":"secret-token recipient body","fbtrace_id":"private"}}`, `not-json`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			fmt.Fprint(w, body)
		}))
		svc, _ := New(nil)
		req, _ := http.NewRequest("POST", server.URL, nil)
		_, err := svc.request(req)
		server.Close()
		var provider *ProviderError
		if !errors.As(err, &provider) || !provider.Retryable() || provider.RetryAfter != 60 {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(provider)
		if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private") || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe provider metadata", string(raw))
		}
	}
}

func TestOutboxRetriesAndPushesDurableOutcomes(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	d, a, record := callbackFixture(t, dsn)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":130429,"message":"private content"}}`)
			return
		}
		fmt.Fprintf(w, `{"messages":[{"id":"wamid.accepted"}],"padding":%q}`, strings.Repeat("x", 70000))
	}))
	defer provider.Close()
	d.Service.base = provider.URL
	o := &Outbox{URL: dsn, Service: d.Service}
	if _, err := o.Enqueue(ctx, a, []Submission{{Key: "stable-request", Message: Input{Operation: "messages.send", To: "919999999999", Body: "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts int
	var delayed bool
	var metadata []byte
	if err := db.QueryRow(ctx, `SELECT state,attempts,next_attempt>now()+interval '50 seconds',provider_error FROM relay_outbox`).Scan(&state, &attempts, &delayed, &metadata); err != nil || state != "queued" || attempts != 1 || !delayed || strings.Contains(string(metadata), "private") {
		t.Fatal(state, attempts, delayed, string(metadata), err)
	}
	if err := o.process(ctx); err != nil || calls.Load() != 1 {
		t.Fatal("retry was not deferred", err)
	}
	if _, err := db.Exec(ctx, "UPDATE relay_outbox SET next_attempt=now(); UPDATE relay_account_send_rate SET next_at=now()"); err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err := o.Lookup(ctx, a, "stable-request")
	if err != nil || delivery.State != "submitted" || calls.Load() != 2 {
		t.Fatal(delivery, err)
	}
	record.mu.Lock()
	record.wrongAck = true
	record.mu.Unlock()
	if err := d.deliver(ctx, a, "realtime"); err == nil {
		t.Fatal("wrong acknowledgement accepted")
	}
	var acknowledged bool
	if err := db.QueryRow(ctx, `SELECT acknowledged FROM relay_whatsapp_callback_delivery`).Scan(&acknowledged); err != nil || acknowledged {
		t.Fatal("outcome not durable", err)
	}
	record.mu.Lock()
	record.wrongAck = false
	record.mu.Unlock()
	if _, err := db.Exec(ctx, "UPDATE relay_whatsapp_callback_delivery SET next_attempt=now()"); err != nil {
		t.Fatal(err)
	}
	restarted := &CallbackDispatcher{URL: dsn, Service: d.Service}
	if err := restarted.deliver(ctx, a, "realtime"); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	if len(record.events) != 2 || record.events[0].ID != record.events[1].ID {
		t.Fatal("outcome retry identity changed")
	}
	raw := string(record.events[1].Payload)
	record.mu.Unlock()
	for _, value := range []string{"stable-request", "accepted", "wamid.accepted"} {
		if !strings.Contains(raw, value) {
			t.Fatal("outcome missing correlation", raw)
		}
	}
	// Crash recovery and manual resolution must also journal in the same transaction.
	if _, err := db.Exec(ctx, `INSERT INTO relay_outbox(account,request_key,payload,state,submission_started,updated_at)
 VALUES($1,'crashed','{"operation":"messages.send","to":"919999999998"}','submitting',true,now()-interval '3 minutes')`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	crashed, err := o.Lookup(ctx, a, "crashed")
	if err != nil || crashed.State != "uncertain" {
		t.Fatal(crashed, err)
	}
	var id int64
	if err := db.QueryRow(ctx, "SELECT id FROM relay_outbox WHERE request_key='crashed'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := o.Resolve(ctx, a, id, "submitted", "Verified synthetic provider acceptance", "wamid.resolved"); err != nil {
		t.Fatal(err)
	}
	var transitions int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE payload->'delivery'->>'request_key'='crashed'`).Scan(&transitions); err != nil || transitions != 2 {
		t.Fatal("resolution/crash outcomes missing", transitions, err)
	}
	// An explicit rejection carries the request key, with no raw provider body.
	if _, err := db.Exec(ctx, `UPDATE relay_outbox SET state='rejected',error='Synthetic rejection' WHERE request_key='crashed'`); err != nil {
		t.Fatal(err)
	}
	if err := restarted.deliver(ctx, a, "realtime"); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if !strings.Contains(string(record.events[len(record.events)-1].Payload), `"outcome": "rejected"`) && !strings.Contains(string(record.events[len(record.events)-1].Payload), `"outcome":"rejected"`) {
		t.Fatal("rejection not forwarded")
	}
}

func TestPreSubmissionFailuresNeverBecomeUncertain(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	a := testAccount()
	var messages, uploads atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/media") {
			uploads.Add(1)
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"code":1}}`)
			return
		}
		messages.Add(1)
		fmt.Fprint(w, `{"messages":[{"id":"wamid.fixture"}]}`)
	}))
	defer provider.Close()
	svc, _ := New([]Account{a})
	svc.base = provider.URL
	o := &Outbox{URL: dsn, Service: svc}
	enqueue := func(key string, in Input) {
		t.Helper()
		if _, err := o.Enqueue(ctx, a, []Submission{{Key: key, Message: in}}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue("missing", Input{Operation: "messages.send", To: "919999999991", Body: "fixture"})
	if err := svc.ReplaceAccounts(nil); err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := o.Lookup(ctx, a, "missing")
	if err != nil || row.State != "queued" {
		t.Fatal("missing configuration", row, err)
	}
	if err := svc.ReplaceAccounts([]Account{a}); err != nil {
		t.Fatal(err)
	}
	// Fail the actual PostgreSQL pacing gate, after claiming but before Meta.
	if _, err := db.Exec(ctx, `CREATE FUNCTION fail_fixture_pacing() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic pacing outage'; END $$;
 CREATE TRIGGER fixture_pacing BEFORE INSERT ON relay_account_send_rate FOR EACH ROW EXECUTE FUNCTION fail_fixture_pacing()`); err != nil {
		t.Fatal(err)
	}
	enqueue("pacing", Input{Operation: "messages.send", To: "919999999992", Body: "fixture"})
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = o.Lookup(ctx, a, "pacing")
	if err != nil || row.State != "queued" {
		t.Fatal("pacing failure", row, err)
	}
	// Replace the fixture failure with a working gate without touching other schemas.
	if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION fail_fixture_pacing() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	enqueue("media", Input{Operation: "messages.media", To: "919999999993", MediaType: "document", ContentBase64: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fixture")), MIMEType: "application/pdf", Filename: "fixture.pdf"})
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = o.Lookup(ctx, a, "media")
	if err != nil || row.State != "queued" || messages.Load() != 0 || uploads.Load() != 1 {
		t.Fatal("upload failure", row, err, messages.Load(), uploads.Load())
	}
	if _, err := db.Exec(ctx, `UPDATE relay_outbox SET state='submitting',updated_at=now()-interval '3 minutes' WHERE request_key='missing'`); err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = o.Lookup(ctx, a, "missing")
	if err != nil || row.State == "uncertain" {
		t.Fatal("pre-submission crash became uncertain", row, err)
	}
}

func TestAccountFairnessAndConversationOrder(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	a := testAccount()
	b := a
	b.ID = "site-b"
	b.PhoneID = "333"
	b.CoreToken = ""
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		calls.Add(1)
		text, _ := body["text"].(map[string]any)
		if text["body"] == "first" {
			started <- struct{}{}
			<-release
		}
		fmt.Fprint(w, `{"messages":[{"id":"wamid.fixture"}]}`)
	}))
	defer provider.Close()
	svc, _ := New([]Account{a, b})
	svc.base = provider.URL
	o := &Outbox{URL: dsn, Service: svc}
	enqueue := func(account Account, key, to string) {
		t.Helper()
		if _, err := o.Enqueue(ctx, account, []Submission{{Key: key, Message: Input{Operation: "messages.send", To: to, Body: key}}}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(a, "throttled", "919999999990")
	enqueue(b, "other-account", "919999999990")
	if _, err := db.Exec(ctx, `INSERT INTO relay_account_send_rate(account,next_at) VALUES($1,now()+interval '1 hour')`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := o.Lookup(ctx, b, "other-account")
	if err != nil || row.State != "submitted" || calls.Load() != 1 {
		t.Fatal("throttled account blocked other account", row, err)
	}
	if err := o.Cancel(ctx, a, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE relay_account_send_rate SET next_at=now()"); err != nil {
		t.Fatal(err)
	}
	enqueue(a, "first", "919999999991")
	enqueue(a, "second", "919999999991")
	done := make(chan error, 1)
	go func() { done <- o.process(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first send did not start")
	}
	// Release the provider even if an ordering assertion fails.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if _, err := db.Exec(ctx, "UPDATE relay_account_send_rate SET next_at=now()"); err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = o.Lookup(ctx, a, "second")
	if err != nil || row.State != "queued" || calls.Load() != 2 {
		t.Fatal("conversation overtook in-flight send", row, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := o.process(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = o.Lookup(ctx, a, "second")
	if err != nil || row.State != "submitted" {
		t.Fatal("conversation did not resume", row, err)
	}
}

func TestFlowSubmissionCheckpointAndAmbiguousResponse(t *testing.T) {
	a := testAccount()
	a.Templates = true
	for _, failCheckpoint := range []bool{false, true} {
		checkpoint, messages := false, 0
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v25.0/222/flows":
				fmt.Fprint(w, `{"data":[{"id":"900"}]}`)
			case "/v25.0/900":
				fmt.Fprint(w, `{"status":"PUBLISHED"}`)
			case "/v25.0/111/messages":
				messages++
				var payload map[string]any
				json.NewDecoder(r.Body).Decode(&payload)
				if payload["biz_opaque_callback_data"] != "relay-outbox:42" {
					t.Error("Flow lost receipt correlation")
				}
				c, _, _ := w.(http.Hijacker).Hijack()
				c.Close()
			default:
				t.Error("unexpected request", r.URL.Path)
				w.WriteHeader(400)
			}
		}))
		svc, _ := New([]Account{a})
		svc.base = provider.URL
		in := Input{Operation: "messages.flow", ID: "900", To: "919999999999", Body: "fixture", FlowCTA: "Open", FlowScreen: "START", FlowToken: "fixture", tracking: "42", beforeSubmit: func(context.Context) error {
			checkpoint = true
			if failCheckpoint {
				return errors.New("checkpoint database outage")
			}
			return nil
		}}
		_, err := svc.Execute(context.Background(), a, in)
		provider.Close()
		state, _, _, _ := sendOutcome(err, 0)
		expected := "uncertain"
		expectedMessages := 1
		if failCheckpoint {
			expected = "queued"
			expectedMessages = 0
		}
		if !checkpoint || messages != expectedMessages || state != expected {
			t.Fatal(checkpoint, messages, state, err)
		}
	}
}

func TestUnavailableAccountDoesNotMonopolizeClaims(t *testing.T) {
	ctx := context.Background()
	_, dsn := richSimulationDatabase(t, ctx)
	missing := testAccount()
	healthy := missing
	healthy.ID = "healthy"
	healthy.PhoneID = "333"
	healthy.CoreToken = ""
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"messages":[{"id":"wamid.healthy"}]}`) }))
	defer provider.Close()
	svc, err := New([]Account{healthy})
	if err != nil {
		t.Fatal(err)
	}
	svc.base = provider.URL
	o := &Outbox{URL: dsn, Service: svc}
	backlog := []Submission{}
	for i := 0; i < 10; i++ {
		backlog = append(backlog, Submission{Key: fmt.Sprintf("missing-%d", i), Message: Input{Operation: "messages.send", To: fmt.Sprintf("9199999999%02d", i), Body: "fixture"}})
	}
	if _, err = o.Enqueue(ctx, missing, backlog); err != nil {
		t.Fatal(err)
	}
	if _, err = o.Enqueue(ctx, healthy, []Submission{{Key: "healthy", Message: Input{Operation: "messages.send", To: "919999999999", Body: "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = o.process(ctx); err != nil {
			t.Fatal(err)
		}
	}
	row, err := o.Lookup(ctx, healthy, "healthy")
	if err != nil || row.State != "submitted" {
		t.Fatal("unavailable account consumed the healthy account's turn", row, err)
	}
}
