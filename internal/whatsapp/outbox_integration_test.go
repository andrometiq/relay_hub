package whatsapp

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutboxDurabilityIsolationAndConcurrentWorkers(t *testing.T) {
	connstr := os.Getenv("RELAY_TEST_DATABASE_URL")
	if connstr == "" {
		t.Skip("requires test database")
	}
	burstSize := 100
	if raw := os.Getenv("RELAY_TEST_BURST"); raw != "" {
		var parseErr error
		burstSize, parseErr = strconv.Atoi(raw)
		if parseErr != nil || burstSize < 100 || burstSize > 2000 || burstSize%50 != 0 {
			t.Fatal("burst must be a multiple of 50 between 100 and 2000")
		}
	}
	workers := 2
	if raw := os.Getenv("RELAY_TEST_WORKERS"); raw != "" {
		var e error
		workers, e = strconv.Atoi(raw)
		if e != nil || workers < 2 || workers > 8 {
			t.Fatal("workers must be between 2 and 8")
		}
	}
	rate := 50
	if raw := os.Getenv("RELAY_TEST_RATE"); raw != "" {
		var e error
		rate, e = strconv.Atoi(raw)
		if e != nil || (rate != 50 && rate != 100) {
			t.Fatal("rate must be 50 or 100")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, connstr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	schema := fmt.Sprintf("relay_outbox_test_%d", time.Now().UnixNano())
	if _, err = c.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	sql, err := os.ReadFile("../database/migrations/0003_outbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	statusSQL, err := os.ReadFile("../database/migrations/0004_delivery_status.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, string(statusSQL)); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"0002_whatsapp_events.sql", "0005_core_callbacks.sql", "0006_incoming_messages.sql", "0007_account_send_rate.sql", "0022_whatsapp_resolution.sql", "0028_whatsapp_callback_delivery.sql", "0029_whatsapp_send_recovery.sql"} {
		raw, err := os.ReadFile("../database/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	scoped := connstr + " search_path=" + schema
	if strings.HasPrefix(connstr, "postgres") {
		u, _ := url.Parse(connstr)
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"messages":[{"id":"wamid.test"}]}`)
	}))
	defer provider.Close()
	a := testAccount()
	a.MessagesPerSecond = rate
	b := a
	b.ID = "other"
	b.CoreToken = strings.Repeat("b", 64)
	svc, _ := New([]Account{a, b})
	svc.base = provider.URL
	o := &Outbox{URL: scoped, Service: svc}
	item := Submission{Key: "request-1", Message: Input{Operation: "messages.send", To: "919999999999", Body: "Hello"}}
	first, err := o.Enqueue(ctx, a, []Submission{item})
	if err != nil {
		t.Fatal(err)
	}
	again, err := o.Enqueue(ctx, a, []Submission{item})
	if err != nil || again[0].ID != first[0].ID {
		t.Fatal("retry duplicated intent", err)
	}
	other, err := o.Enqueue(ctx, b, []Submission{item})
	if err != nil || other[0].ID == first[0].ID {
		t.Fatal("account scope failed", err)
	}
	changed := item
	changed.Message.Body = "different"
	if _, err = o.Enqueue(ctx, a, []Submission{changed}); err == nil {
		t.Fatal("conflicting key accepted")
	}
	if err = o.Cancel(ctx, b, first[0].ID); err == nil {
		t.Fatal("cross-account cancellation allowed")
	}
	if calls.Load() != 0 {
		t.Fatal("enqueue sent to provider synchronously")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- o.process(ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one send per account, got %d", calls.Load())
	}
	rows, err := o.List(ctx, a, "")
	if err != nil || len(rows) != 1 || rows[0].State != "submitted" {
		t.Fatal("persisted result missing", rows, err)
	}
	if err = o.Cancel(ctx, a, first[0].ID); err == nil {
		t.Fatal("submitted intent cancelled")
	}
	_, err = c.Exec(ctx, "UPDATE relay_outbox SET state='submitting',updated_at=now()-interval '3 minutes' WHERE account=$1", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = o.process(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err = o.List(ctx, a, "")
	if err != nil || rows[0].State != "uncertain" || calls.Load() != 2 {
		t.Fatal("interrupted send retried", rows, err)
	}
	eventStore := PostgresEvents{URL: scoped}
	receipt := []byte(fmt.Sprintf(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"wamid.reconciled","status":"delivered","timestamp":"100","biz_opaque_callback_data":"relay-outbox:%s"}]}}]}]}`, first[0].ID))
	if err = eventStore.Save(ctx, a.ID, receipt); err != nil {
		t.Fatal(err)
	}
	rows, err = o.List(ctx, a, "")
	if err != nil || rows[0].State != "submitted" || rows[0].DeliveryStatus != "delivered" {
		t.Fatal("receipt failed to reconcile uncertain outcome", rows, err)
	}
	item.Key = "cancel-me"
	queued, err := o.Enqueue(ctx, a, []Submission{item})
	if err != nil {
		t.Fatal(err)
	}
	if err = o.Cancel(ctx, a, queued[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = o.process(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("cancelled intent sent")
	}
	// Run the actual adaptive workers against a burst, then verify they drain and
	// return to the idle minimum. No provider network leaves this test server.
	poolService, _ := New([]Account{a, b})
	poolService.base = provider.URL
	adaptive, err := NewOutbox(ctx, scoped, poolService, workers)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	defer adaptive.Close()
	defer stop()
	started := time.Now()
	for chunk := 0; chunk < burstSize/50; chunk++ {
		batch := []Submission{}
		for i := 0; i < 50; i++ {
			m := item
			m.Key = fmt.Sprintf("burst-%d-%d", chunk, i)
			batch = append(batch, m)
		}
		if _, err = adaptive.Enqueue(ctx, a, batch); err != nil {
			t.Fatal(err)
		}
	}
	adaptive.Run(workerCtx)
	peak := int32(0)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		active := adaptive.active.Load()
		if active > peak {
			peak = active
		}
		var queued int
		if err = c.QueryRow(ctx, "SELECT count(*) FROM relay_outbox WHERE state IN ('queued','submitting')").Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if queued == 0 && adaptive.pending.Load() == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if peak != int32(workers) || adaptive.active.Load() != 1 || calls.Load() != int32(burstSize+2) {
		t.Fatalf("adaptive drain failed: peak=%d active=%d calls=%d", peak, adaptive.active.Load(), calls.Load())
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("burst=%d rate_limit=%d elapsed=%s peak_workers=%d idle_workers=%d heap_alloc_bytes=%d goroutines=%d", burstSize, rate, time.Since(started), peak, adaptive.active.Load(), memory.Alloc, runtime.NumGoroutine())
	// Replaying a completed typed request reads its receipt without provider IO.
	before := calls.Load()
	replay := item.Message
	replay.RequestKey = "burst-0-0"
	result, err := adaptive.Submit(ctx, a, replay)
	if err != nil || result["messages"] == nil || calls.Load() != before {
		t.Fatal("typed replay sent again", result, err)
	}
	if _, err := adaptive.Lookup(ctx, b, "burst-0-0"); err == nil {
		t.Fatal("cross-account lookup allowed")
	}
	replay.RequestKey = "typed-new"
	result, err = adaptive.Submit(ctx, a, replay)
	if err != nil || result["messages"] == nil || calls.Load() != before+1 {
		t.Fatal("typed submission did not drain", result, err)
	}
	d, err := adaptive.Lookup(ctx, a, "typed-new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, "UPDATE relay_outbox SET state='uncertain' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(d.ID, 10, 64)
	if err = adaptive.Resolve(ctx, b, id, "submitted", "Checked provider receipt", "wamid.verified"); err == nil {
		t.Fatal("cross-account resolution allowed")
	}
	if err = adaptive.Resolve(ctx, a, id, "submitted", "Checked provider receipt", ""); err == nil {
		t.Fatal("accepted message without provider ID")
	}
	if err = adaptive.Resolve(ctx, a, id, "submitted", "Checked provider receipt", "wamid.verified"); err != nil {
		t.Fatal(err)
	}
	if err = adaptive.Resolve(ctx, a, id, "rejected", "Changed provider evidence", ""); err == nil {
		t.Fatal("resolution overwritten")
	}
	if calls.Load() != before+1 {
		t.Fatal("resolution submitted provider request")
	}
	replay.Body = "changed"
	if _, err := adaptive.Submit(ctx, a, replay); err == nil {
		t.Fatal("typed request key accepted changed content")
	}
	stop()

}
