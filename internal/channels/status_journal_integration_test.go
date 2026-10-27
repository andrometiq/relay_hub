package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChannelStatusJournal(t *testing.T) {
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("relay_status_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	c, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	if err = database.Migrate(ctx, c.Conn()); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash) SELECT 'alpha','key-'||n,'telegram.message','{}','hash' FROM generate_series(1,1003) n`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE relay_channel_outbox SET state='submitted',result='{"message_id":99,"chat_id":123,"secret":"must-not-leak"}' WHERE request_key NOT IN ('key-1002','key-1003')`); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{500, 500, 1} {
		batch, err := PendingStatusReceipts(ctx, c.Conn(), "alpha")
		if err != nil || len(batch) != want {
			t.Fatalf("batch %d want %d: %v", len(batch), want, err)
		}
		ids := []int64{}
		for _, r := range batch {
			ids = append(ids, r.ID)
			if string(r.Receipt) != "{\"chat_id\": 123, \"message_id\": 99}" {
				t.Fatalf("unexpected receipt %s", r.Receipt)
			}
		}
		if err = AcknowledgeStatusReceipts(ctx, c.Conn(), "other", ids); err != nil {
			t.Fatal(err)
		}
		unchanged, _ := PendingStatusReceipts(ctx, c.Conn(), "alpha")
		if len(unchanged) != want {
			t.Fatal("cross account acknowledgement")
		}
		if err = AcknowledgeStatusReceipts(ctx, c.Conn(), "alpha", ids); err != nil {
			t.Fatal(err)
		}
	}
	// A low ID allocated in a slow transaction must survive a later ID's ACK.
	slow, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Rollback(ctx)
	if _, err = slow.Exec(ctx, `UPDATE relay_channel_outbox SET state='rejected' WHERE request_key='key-1002'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE relay_channel_outbox SET state='uncertain' WHERE request_key='key-1003'`); err != nil {
		t.Fatal(err)
	}
	batch, err := PendingStatusReceipts(ctx, c.Conn(), "alpha")
	if err != nil || len(batch) != 1 || batch[0].RequestKey != "key-1003" {
		t.Fatalf("fast transaction %v %v", batch, err)
	}
	if err = AcknowledgeStatusReceipts(ctx, c.Conn(), "alpha", []int64{batch[0].ID}); err != nil {
		t.Fatal(err)
	}
	if err = slow.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err = PendingStatusReceipts(ctx, c.Conn(), "alpha")
	if err != nil || len(batch) != 1 || batch[0].RequestKey != "key-1002" {
		t.Fatal("late commit status lost", err)
	}
	// Repeating the same state must not create more callback work.
	if _, err = pool.Exec(ctx, `UPDATE relay_channel_outbox SET state='rejected' WHERE request_key='key-1002'`); err != nil {
		t.Fatal(err)
	}
	batch, err = PendingStatusReceipts(ctx, c.Conn(), "alpha")
	if err != nil || len(batch) != 1 {
		t.Fatal("duplicate transition journaled")
	}
	// A rejected status ACK retains pending rows without blocking inbound callbacks.
	acceptStatus := false
	incomingCalls := 0
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/method/relay_core.inbound.receive_channel_statuses" {
			statusCalls++
			if !acceptStatus {
				w.WriteHeader(503)
				return
			}
			var payload struct {
				Statuses []StatusReceipt `json:"statuses"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil {
				t.Error("invalid status body")
			}
			fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(payload.Statuses))
			return
		}
		incomingCalls++
		fmt.Fprint(w, `{"message":{"accepted":1}}`)
	}))
	defer server.Close()
	a := RuntimeCallback{Account: "alpha", Site: server.URL, APIKey: "key", APISecret: "secret", CallbackSecret: "secret"}
	scoped := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres") {
		u, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	d := CallbackDispatcher{URL: scoped}
	if err = d.deliverStatuses(ctx, a); err == nil {
		t.Fatal("rejected ACK succeeded")
	}
	batch, err = PendingStatusReceipts(ctx, c.Conn(), "alpha")
	if err != nil || len(batch) != 1 || statusCalls != 1 {
		t.Fatal("failed ACK not exercised or pending receipt lost")
	}
	if err = (Store{DB: pool}).Save(ctx, Event{Account: "alpha", Channel: "Telegram", Direction: "inbound", ProviderID: "new-incoming", Sender: "123", Body: "incoming", OccurredAt: time.Now(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	d.deliverOne(ctx, a, "Telegram")
	if incomingCalls != 1 {
		t.Fatal("status failure blocked inbound callback")
	}
	if _, err = pool.Exec(ctx, "UPDATE relay_channel_status_schedule SET next_attempt=now() WHERE account='alpha'"); err != nil {
		t.Fatal(err)
	}
	acceptStatus = true
	if err = d.deliverStatuses(ctx, a); err != nil {
		t.Fatal(err)
	}
	batch, err = PendingStatusReceipts(ctx, c.Conn(), "alpha")
	if err != nil || len(batch) != 0 {
		t.Fatal("successful ACK did not clear pending batch")
	}

}
