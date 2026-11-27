package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

// A Meta 500 left a bulk send uncertain and the next send to that number
// queued for good: the uncertain predecessor blocked the recipient forever.
func TestUncertainBulkSendReachesCoreAndReleasesTheRecipient(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if text, _ := body["text"].(map[string]any); text["body"] == "first" {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":{"code":131000,"message":"Something went wrong"}}`)
			return
		}
		fmt.Fprint(w, `{"messages":[{"id":"wamid.second"}]}`)
	}))
	defer provider.Close()
	a := testAccount()
	a.SiteID = a.ID
	svc, _ := New([]Account{a})
	svc.base = provider.URL
	o := &Outbox{URL: dsn, Service: svc}
	batch := []Submission{
		{Key: "poll-first", Message: Input{Operation: "messages.send", To: "919999999991", Body: "first"}},
		{Key: "poll-second", Message: Input{Operation: "messages.send", To: "919999999991", Body: "second"}},
	}
	rows, _, err := o.EnqueueBulk(ctx, a, batch)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if err = o.process(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := o.Lookup(ctx, a, "poll-first")
	if err != nil || first.State != "uncertain" {
		t.Fatal("Meta 500 after submission must be uncertain", first, err)
	}

	updates, err := pendingBulkUpdates(ctx, db, a.ID)
	if err != nil || len(updates) != 1 || updates[0].Key != "poll-first" || updates[0].State != "uncertain" || string(updates[0].Error) != `{"code":131000,"title":"","message":""}` {
		t.Fatal("uncertain bulk outcome is not reported to Core", updates, err)
	}

	if err = o.process(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := o.Lookup(ctx, a, "poll-second")
	if err != nil || second.State != "queued" || calls.Load() != 1 {
		t.Fatal("a fresh uncertain send must still hold the recipient for its receipt", second, err)
	}
	id, _ := strconv.ParseInt(first.ID, 10, 64)
	if _, err = db.Exec(ctx, "UPDATE relay_outbox SET updated_at=now()-$2::interval WHERE id=$1", id, (uncertainHold + 1e9).String()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "UPDATE relay_account_send_rate SET next_at=now()"); err != nil {
		t.Fatal(err)
	}
	if err = o.process(ctx); err != nil {
		t.Fatal(err)
	}
	second, err = o.Lookup(ctx, a, "poll-second")
	if err != nil || second.State != "submitted" || calls.Load() != 2 {
		t.Fatal("an old uncertain send still blocks the recipient", second, err)
	}
	first, err = o.Lookup(ctx, a, "poll-first")
	if err != nil || first.State != "uncertain" {
		t.Fatal("the uncertain send was sent again", first, err)
	}
}
