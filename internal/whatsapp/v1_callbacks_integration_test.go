package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type callbackRecorder struct {
	mu       sync.Mutex
	events   []Event
	statuses []CompactStatus
	wrongAck bool
}

func (r *callbackRecorder) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var body struct {
		Events   []Event         `json:"events"`
		Statuses []CompactStatus `json:"statuses"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		w.WriteHeader(400)
		return
	}
	r.events = append(r.events, body.Events...)
	r.statuses = append(r.statuses, body.Statuses...)
	n := len(body.Events) + len(body.Statuses)
	if r.wrongAck {
		n++
	}
	fmt.Fprintf(w, `{"message":{"accepted":%d}}`, n)
}
func callbackFixture(t *testing.T, dsn string) (*CallbackDispatcher, Account, *callbackRecorder) {
	t.Helper()
	recorder := &callbackRecorder{}
	server := httptest.NewServer(http.HandlerFunc(recorder.serve))
	t.Cleanup(server.Close)
	a := testAccount()
	a.Site = server.URL
	a.CoreAPIKey = "fixture"
	a.CoreAPISecret = "fixture"
	a.CallbackSecret = strings.Repeat("s", 32)
	svc, err := New([]Account{a})
	if err != nil {
		t.Fatal(err)
	}
	return &CallbackDispatcher{URL: dsn, Service: svc}, a, recorder
}
func TestCallbacksSurviveReversedCommits(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	d, a, record := callbackFixture(t, dsn)
	second, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	for _, lane := range []string{"realtime", "status"} {
		t.Run(lane, func(t *testing.T) {
			first, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(ctx)
			later, err := second.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer later.Rollback(ctx)
			payload := []byte(`{"object":"test"}`)
			if lane == "status" {
				payload = statusFixture(1, 1).Payload
			}
			insert := func(tx pgx.Tx, digest string) int64 {
				var id int64
				if err := tx.QueryRow(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id) VALUES($1,$2,$3,$4,$1) RETURNING id`, a.ID, digest, payload, lane).Scan(&id); err != nil {
					t.Fatal(err)
				}
				return id
			}
			lower := insert(first, lane+"-first")
			higher := insert(later, lane+"-second")
			if lower >= higher {
				t.Fatal("fixture IDs not ordered")
			}
			if err = later.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = d.deliver(ctx, a, lane); err != nil {
				t.Fatal(err)
			}
			if err = first.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			restarted := &CallbackDispatcher{URL: dsn, Service: d.Service}
			if err = restarted.deliver(ctx, a, lane); err != nil {
				t.Fatal(err)
			}
			if err = restarted.deliver(ctx, a, lane); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = db.QueryRow(ctx, `SELECT count(*) FROM relay_whatsapp_callback_delivery WHERE event_id=ANY($1) AND acknowledged`, []int64{lower, higher}).Scan(&count); err != nil || count != 2 {
				t.Fatal("late commit skipped", count, err)
			}
			record.mu.Lock()
			defer record.mu.Unlock()
			if lane == "realtime" && (len(record.events) != 2 || record.events[0].ID != higher || record.events[1].ID != lower) {
				t.Fatal(record.events)
			}
			if lane == "status" && (len(record.statuses) != 2 || record.statuses[0].EventID != higher || record.statuses[1].EventID != lower) {
				t.Fatal(record.statuses)
			}
		})
	}
}
func TestMixedEnvelopeAndPoisonReceiptRecovery(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	d, a, record := callbackFixture(t, dsn)
	store := PostgresEvents{URL: dsn}
	mixed := []byte(`{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messages":[{"id":"wamid.in","from":"919999999999","text":{"body":"fast"}}],"statuses":[{"id":"wamid.out","recipient_id":"919999999999","status":"read","timestamp":"100"}]}}]}]}`)
	if err := store.Save(ctx, a.ID, mixed); err != nil {
		t.Fatal(err)
	}
	if err := d.deliver(ctx, a, "realtime"); err != nil {
		t.Fatal(err)
	}
	if err := d.deliver(ctx, a, "status"); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	if len(record.events) != 1 || strings.Contains(string(record.events[0].Payload), `"statuses"`) || !strings.Contains(string(record.events[0].Payload), `"messages"`) || len(record.statuses) != 1 {
		t.Fatal("lanes not split")
	}
	record.mu.Unlock()
	var unchanged bool
	if err := db.QueryRow(ctx, `SELECT payload=$1::jsonb AND raw_payload=$1::jsonb FROM relay_whatsapp_events WHERE account=$2`, mixed, a.ID).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("raw journal changed", err)
	}
	bad := []byte(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"bad","status":"read","timestamp":"no-time","recipient_id":"1"}]}}]}]}`)
	if err := store.Save(ctx, a.ID, bad); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, a.ID, statusFixture(5, 1).Payload); err != nil {
		t.Fatal(err)
	}
	if err := d.deliver(ctx, a, "status"); err != nil {
		t.Fatal("poison blocked valid receipt", err)
	}
	var id int64
	var position, failures int
	var backedOff bool
	if err := db.QueryRow(ctx, `SELECT event_id,error_position,failures,next_attempt>now() FROM relay_whatsapp_callback_delivery WHERE error<>''`).Scan(&id, &position, &failures, &backedOff); err != nil || position != 0 || failures != 1 || !backedOff {
		t.Fatal("failure not recorded", err, position, failures, backedOff)
	}
	if err := d.deliver(ctx, a, "status"); err != nil {
		t.Fatal("poison blocked later event", err)
	}
	record.mu.Lock()
	if len(record.statuses) != 2 {
		t.Fatal("later receipt not delivered")
	}
	record.mu.Unlock()
	if err := d.RecoverCallback(ctx, "other", "status", id, 1, "quarantine", "Synthetic incident"); err == nil {
		t.Fatal("cross-account quarantine")
	}
	if err := d.RecoverCallback(ctx, a.ID, "status", id, 1, "quarantine", "Synthetic incident"); err != nil {
		t.Fatal(err)
	}
	if err := d.deliver(ctx, a, "status"); err != nil {
		t.Fatal("quarantine replayed", err)
	}
	if err := d.RecoverCallback(ctx, a.ID, "status", id, 1, "replay", "Decoder recovery verification"); err != nil {
		t.Fatal(err)
	}
	if err := d.deliver(ctx, a, "status"); err == nil {
		t.Fatal("invalid immutable receipt acknowledged on replay")
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM relay_whatsapp_callback_audit WHERE event_id=$1 AND actor=1`, id).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("recovery not audited", audits, err)
	}
}
func TestMultiAccountWebhookAuthenticatesOriginalAndRoutesWABA(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	a := testAccount()
	a.AppSecret = "fixture-app"
	b := a
	b.ID = "site-b"
	b.PhoneID = "333"
	b.CoreToken = ""
	svc, err := New([]Account{a, b})
	if err != nil {
		t.Fatal(err)
	}
	svc.Events = PostgresEvents{URL: dsn}
	raw := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"111"},"messages":[{"id":"wamid.a","from":"919999999999"}]}},{"field":"messages","value":{"metadata":{"phone_number_id":"333"},"messages":[{"id":"wamid.b","from":"919999999998"}]}},{"field":"message_template_status_update","value":{"event":"APPROVED","message_template_id":123}}]}]}`
	post := func(secret string) int {
		req := httptest.NewRequest("POST", "/webhooks/whatsapp", strings.NewReader(raw))
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(raw))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		svc.Webhook(w, req)
		return w.Code
	}
	if post("wrong") != 403 {
		t.Fatal("unsigned envelope accepted")
	}
	if post(a.AppSecret) != 200 || post(a.AppSecret) != 200 {
		t.Fatal("multi-account envelope rejected")
	}
	for _, account := range []Account{a, b} {
		var body []byte
		var original bool
		if err := db.QueryRow(ctx, `SELECT payload,raw_payload=$2::jsonb FROM relay_whatsapp_events WHERE account=$1`, account.ID, raw).Scan(&body, &original); err != nil || !original {
			t.Fatal("missing original journal", err)
		}
		other := "wamid.b"
		if account.ID == b.ID {
			other = "wamid.a"
		}
		if strings.Contains(string(body), other) || !strings.Contains(string(body), "APPROVED") {
			t.Fatal("routing leaked phone or lost WABA event", string(body))
		}
	}
	b.AppSecret = "different-app"
	if err := svc.ReplaceAccounts([]Account{a, b}); err != nil {
		t.Fatal(err)
	}
	raw = strings.ReplaceAll(raw, "wamid.", "wamid.new.")
	if post(a.AppSecret) != 200 {
		t.Fatal("unauthenticated destination blocked authenticated account")
	}
	for _, account := range []Account{a, b} {
		var count int
		want := 1
		if account.ID == a.ID {
			want = 2
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE account=$1`, account.ID).Scan(&count); err != nil || count != want {
			t.Fatal("destination signature not respected", account.ID, count, err)
		}
	}
}

func TestMalformedReceiptBacklogDoesNotStarveValidStatuses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, dsn := richSimulationDatabase(t, ctx)
	d, a, record := callbackFixture(t, dsn)
	store := PostgresEvents{URL: dsn}
	if err := store.Save(ctx, a.ID, statusFixture(1, 1).Payload); err != nil {
		t.Fatal(err)
	}
	bad := `{"entry":[{"changes":[{"value":{"statuses":[{"id":"bad","status":"read","timestamp":"invalid","recipient_id":"1"}]}}]}]}`
	if _, err := db.Exec(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id)
 SELECT $1,'bad-' || n,$2::jsonb,'status',$1 FROM generate_series(1,300) n`, a.ID, bad); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, a.ID, statusFixture(2, 1).Payload); err != nil {
		t.Fatal(err)
	}
	for _, failures := range []int{0, 8} {
		if failures > 0 {
			if _, err := db.Exec(ctx, `UPDATE relay_whatsapp_callback_delivery SET failures=$1,next_attempt=now() WHERE NOT acknowledged`, failures); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(ctx, a.ID, statusFixture(3, 1).Payload); err != nil {
				t.Fatal(err)
			}
		}
		if err := d.deliver(ctx, a, "status"); err != nil {
			t.Fatal("malformed backlog blocked valid statuses", err)
		}
		var pending, backedOff int
		// The backlog can take longer than the first retry delay on a shared test
		// database. Assert the persisted schedule relative to its write time.
		if err := db.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE failures=$1 AND error_position=0 AND error<>'' AND next_attempt>=updated_at+interval '1 second')
 FROM relay_whatsapp_callback_delivery WHERE NOT acknowledged`, failures+1).Scan(&pending, &backedOff); err != nil || pending != 300 || backedOff != 300 {
			t.Fatal("invalid receipts not retained with backoff", pending, backedOff, err)
		}
		record.mu.Lock()
		count := len(record.statuses)
		record.mu.Unlock()
		want := 2
		if failures > 0 {
			want = 3
		}
		if count != want {
			t.Fatalf("delivered %d statuses, want %d", count, want)
		}
	}
}

func TestDisabledAccountDoesNotBlockSharedWebhook(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	d, a, record := callbackFixture(t, dsn)
	a.AppSecret = "fixture-app"
	b := a
	b.ID, b.PhoneID, b.CoreToken = "site-b", "333", ""
	if err := d.Service.ReplaceAccounts([]Account{a, b}); err != nil {
		t.Fatal(err)
	}
	// Disabled database accounts are removed from the runtime snapshot.
	if err := d.Service.ReplaceAccounts([]Account{a}); err != nil {
		t.Fatal(err)
	}
	d.Service.Events = PostgresEvents{URL: dsn}
	raw := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"333"},"messages":[{"id":"wamid.disabled","from":"919999999998"}],"statuses":[{"id":"wamid.disabled-out","status":"read","timestamp":"100","recipient_id":"919999999998"}]}},{"field":"messages","value":{"metadata":{"phone_number_id":"111"},"messages":[{"id":"wamid.active","from":"919999999999"}]}},{"field":"message_template_status_update","value":{"event":"APPROVED","message_template_id":123}}]}]}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/webhooks/whatsapp", strings.NewReader(raw))
		mac := hmac.New(sha256.New, []byte(a.AppSecret))
		mac.Write([]byte(raw))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		d.Service.Webhook(w, req)
		if w.Code != 200 {
			t.Fatal("disabled destination blocked envelope", w.Code, w.Body.String())
		}
	}
	var body, unknown []byte
	var original bool
	if err := db.QueryRow(ctx, `SELECT payload,raw_payload=$1::jsonb FROM relay_whatsapp_events WHERE account=$2`, raw, a.ID).Scan(&body, &original); err != nil || !original {
		t.Fatal("raw journal lost", err)
	}
	if strings.Contains(string(body), "disabled") || !strings.Contains(string(body), "wamid.active") || !strings.Contains(string(body), "APPROVED") {
		t.Fatal("incorrect account projection", string(body))
	}
	// The removed number's changes are kept unrouted for an admin replay.
	if err := db.QueryRow(ctx, `SELECT payload FROM relay_whatsapp_events WHERE account='' AND site_id IS NULL AND container_id=$1`, a.ID).Scan(&unknown); err != nil || !strings.Contains(string(unknown), "wamid.disabled") {
		t.Fatal("unknown number not kept unrouted", string(unknown), err)
	}
	var events, incoming, statuses int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM relay_whatsapp_events),
 (SELECT count(*) FROM relay_whatsapp_incoming WHERE account=$1 AND provider_id='wamid.active'),
 (SELECT count(*) FROM relay_whatsapp_status)`, a.ID).Scan(&events, &incoming, &statuses); err != nil || events != 2 || incoming != 1 || statuses != 0 {
		t.Fatal("disabled changes projected or retry duplicated", events, incoming, statuses, err)
	}
	for _, lane := range []string{"realtime", "status"} {
		if err := d.deliver(ctx, a, lane); err != nil {
			t.Fatal(err)
		}
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.events) != 1 || len(record.statuses) != 0 || strings.Contains(string(record.events[0].Payload), "disabled") {
		t.Fatal("callback leaked disabled destination")
	}
}
