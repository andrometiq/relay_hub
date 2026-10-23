package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/database"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type outboxTransport func(*http.Request) (*http.Response, error)

func (f outboxTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDurableChannelOutbox(t *testing.T) {
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RELAY_TEST_DATABASE_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("relay_channel_queue_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	client, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: "fixture", BotToken: "123456:synthetic_fixture_token_never_used", Enabled: true}, &http.Client{Transport: outboxTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":99,"chat":{"id":123}}}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	queue := Outbox{DB: pool, Registry: &Registry{Telegram: map[string]*telegram.Client{"fixture": client}}}
	input := telegram.MessageInput{Operation: "text", ChatID: 123, Text: "Synthetic"}
	first, err := queue.Submit(ctx, "fixture", "stable", "telegram.message", input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := queue.Submit(ctx, "fixture", "stable", "telegram.message", input)
	if err != nil || fmt.Sprint(first["delivery_id"]) != fmt.Sprint(again["delivery_id"]) || requests.Load() != 1 {
		t.Fatal("retry submitted twice", err)
	}
	receipt, err := queue.Lookup(ctx, "fixture", "stable")
	if err != nil || receipt["state"] != "submitted" || receipt["key"] != "stable" || fmt.Sprint(receipt["id"]) != fmt.Sprint(first["delivery_id"]) {
		t.Fatal("receipt lookup failed", err)
	}
	if requests.Load() != 1 {
		t.Fatal("receipt lookup sent again")
	}
	for _, pair := range [][2]string{{"other-account", "stable"}, {"fixture", "absent"}} {
		value, err := queue.Lookup(ctx, pair[0], pair[1])
		if err != nil || value != nil {
			t.Fatal("missing or other-account key disclosed", err)
		}
	}
	if _, err := queue.Lookup(ctx, "fixture", "invalid key"); err == nil {
		t.Fatal("invalid lookup key accepted")
	}
	changed := input
	changed.Text = "Different"
	if _, err = queue.Submit(ctx, "fixture", "stable", "telegram.message", changed); err == nil {
		t.Fatal("key reuse changed payload")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); queue.Submit(ctx, "fixture", "concurrent", "telegram.message", input) }()
	}
	wg.Wait()
	if requests.Load() != 2 {
		t.Fatalf("concurrent duplicate sends: %d", requests.Load())
	}
	rows, err := queue.List(ctx, "other-account", 0)
	if err != nil || len(rows) != 0 {
		t.Fatal("cross-account disclosure", err)
	}
	raw, _ := json.Marshal(input)
	var id int64
	err = pool.QueryRow(ctx, `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash) VALUES('fixture','recovery','telegram.message',$1,'fixture') RETURNING id`, raw).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queue.dispatch(ctx, id, "fixture"); err != nil {
		t.Fatal("queued recovery", err)
	}
	if requests.Load() != 3 {
		t.Fatal("queued send not recovered")
	}
	_, err = pool.Exec(ctx, `UPDATE relay_channel_outbox SET state='dispatching',updated_at=now()-interval '3 minutes' WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = queue.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = queue.dispatch(ctx, id, "fixture"); err == nil {
		t.Fatal("uncertain send was replayed")
	}
	if requests.Load() != 3 {
		t.Fatal("recovery duplicated external effect")
	}
	uncertain, err := queue.Lookup(ctx, "fixture", "recovery")
	if err != nil || uncertain["state"] != "uncertain" || requests.Load() != 3 {
		t.Fatal("uncertain receipt lookup replayed send", err)
	}

	if err = queue.Cancel(ctx, "other-account", id); err == nil {
		t.Fatal("cross-account cancellation")
	}
	if err = queue.Resolve(ctx, "other-account", id, "submitted", "Verified in provider console", Resolution{ProviderID: "99", ChatID: 123}); err == nil {
		t.Fatal("cross-account resolution")
	}
	if err = queue.Resolve(ctx, "fixture", id, "queued", "Retry this message again"); err == nil {
		t.Fatal("resolution permitted replay")
	}
	if err = queue.Resolve(ctx, "fixture", id, "submitted", "Verified in provider console", Resolution{ProviderID: "99", ChatID: 123}); err != nil {
		t.Fatal(err)
	}
	if err = queue.Resolve(ctx, "fixture", id, "rejected", "Conflicting second resolution"); err == nil {
		t.Fatal("resolved outcome changed")
	}
	if _, err = queue.dispatch(ctx, id, "fixture"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatal("resolution resent delivery")
	}
	var note string
	if err = pool.QueryRow(ctx, "SELECT resolution_note FROM relay_channel_outbox WHERE id=$1", id).Scan(&note); err != nil || note != "Verified in provider console" {
		t.Fatal("missing resolution evidence", err)
	}

	store := Store{DB: pool}
	if err = store.Save(ctx, Event{Account: "fixture", Channel: "Telegram", Direction: "inbound", ProviderID: "media-event", OccurredAt: time.Now(), Payload: json.RawMessage(`{"message":{"photo":[{"file_id":"photo-small"},{"file_id":"photo-large"}],"document":{"file_id":"document"}}}`)}); err != nil {
		t.Fatal(err)
	}
	for _, fileID := range []string{"photo-small", "photo-large", "document"} {
		known, e := store.TelegramMediaKnown(ctx, "fixture", fileID)
		if e != nil || !known {
			t.Fatal("journaled media not found", fileID, e)
		}
		known, e = store.TelegramMediaKnown(ctx, "other-account", fileID)
		if e != nil || known {
			t.Fatal("cross-account media lookup", e)
		}
	}
	known, e := store.TelegramMediaKnown(ctx, "fixture", "unknown")
	if e != nil || known {
		t.Fatal("unknown media accepted", e)
	}

	poller := MailboxPoller{DB: pool}
	mailbox := &syntheticMailbox{fail: true}
	if err = poller.Poll(ctx, "fixture", mailbox); err == nil {
		t.Fatal("mailbox failure not surfaced")
	}
	var uid int
	if err = pool.QueryRow(ctx, `SELECT last_uid FROM relay_mailbox_cursor WHERE account='fixture' AND mailbox='synthetic'`).Scan(&uid); err != nil || uid != 1 {
		t.Fatal("durable mailbox cursor lost", uid, err)
	}
	mailbox.fail = false
	if err = poller.Poll(ctx, "fixture", mailbox); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM relay_channel_events WHERE account='fixture' AND channel='Email'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("mailbox replay duplicated event", count, err)
	}
	var content []byte
	if err = pool.QueryRow(ctx, `SELECT content FROM relay_email_attachments WHERE account='fixture'`).Scan(&content); err != nil || string(content) != "private bytes" {
		t.Fatal("private mailbox attachment lost", err)
	}

}

type syntheticMailbox struct{ fail bool }

func (*syntheticMailbox) MailboxKey() string { return "synthetic" }
func (s *syntheticMailbox) FetchMailbox(ctx context.Context, cursor relayemail.MailboxCursor, consume func(uint32, uint32, *relayemail.Received, string) error) (relayemail.MailboxCursor, error) {
	// Deliberately replay even after a checkpoint to verify database deduplication.
	message := &relayemail.Received{MessageID: "<fixture@example.invalid>", From: "sender@example.invalid", To: []string{"inbox@example.invalid"}, Subject: "Synthetic", Body: "Body", Attachments: []relayemail.Attachment{{Filename: "a.txt", ContentType: "text/plain", ContentBase64: base64.StdEncoding.EncodeToString([]byte("private bytes"))}}}
	if err := consume(1, 1, message, ""); err != nil {
		return cursor, err
	}
	if s.fail {
		return cursor, errors.New("synthetic disconnect")
	}
	return relayemail.MailboxCursor{Validity: 1, UID: 1}, nil
}
