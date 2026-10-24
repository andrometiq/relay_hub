package channels

import (
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func channelTestDB(t *testing.T) (*pgxpool.Pool, string) {
	return channelTestDBWithMigration(t, database.Migrate)
}

func channelTestDBWithMigration(t *testing.T, migrate func(context.Context, *pgx.Conn) error) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if cfg.ConnConfig.Database == "channel_hub" || cfg.ConnConfig.Database == "relay_hub_sim" {
		t.Fatal("refusing protected database")
	}
	ctx := t.Context()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("relay_channel_v15_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer base.Close()
		_, err := base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	scoped := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	return pool, scoped
}

func saveChannelEvent(t *testing.T, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, account, channel, pid, body string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(t.Context(), `INSERT INTO relay_channel_events(account,channel,direction,provider_id,sender,recipient,body,payload,occurred_at) VALUES($1,$2,'inbound',$3,'42','123',$4,'{}',now()) RETURNING id`, account, channel, pid, body).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestChannelCallbacksCommitOrderRestartAndIsolation(t *testing.T) {
	pool, dsn := channelTestDB(t)
	ctx := t.Context()
	var mu sync.Mutex
	reject, invalidAck := false, false
	poison := int64(0)
	seen := map[int64]int{}
	batches := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if r.Header.Get("X-Relay-Signature") != hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("Authorization") != "token key:secret" {
			t.Error("invalid authentication")
		}
		var body struct {
			Account string
			Events  []struct {
				ID      int64
				Payload struct{ Channel string }
			}
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		if body.Account != "alpha" {
			t.Error("cross-account callback")
		}
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, len(body.Events))
		for _, e := range body.Events {
			if e.Payload.Channel != "Telegram" {
				t.Error("cross-channel callback")
			}
			if e.ID == poison {
				w.WriteHeader(422)
				return
			}
		}
		if reject {
			w.WriteHeader(503)
			return
		}
		for _, e := range body.Events {
			seen[e.ID]++
		}
		n := len(body.Events)
		if invalidAck {
			n = 0
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]int{"accepted": n}})
	}))
	defer server.Close()
	a := RuntimeCallback{Account: "alpha", Site: server.URL, APIKey: "key", APISecret: "secret", CallbackSecret: "test-secret"}
	d := &CallbackDispatcher{URL: dsn}
	countACK := func(want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM relay_channel_callback_delivery WHERE acknowledged").Scan(&got); err != nil || got != want {
			t.Fatalf("ACK count %d want %d: %v", got, want, err)
		}
	}
	due := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, "UPDATE relay_channel_callback_delivery SET next_attempt=now()"); err != nil {
			t.Fatal(err)
		}
	}
	// Allocate the lower sequence in a transaction that commits after the newer ACK.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	lower := saveChannelEvent(t, tx, "alpha", "Telegram", "lower", "delayed commit")
	higher := saveChannelEvent(t, pool, "alpha", "Telegram", "higher", "committed first")
	saveChannelEvent(t, pool, "other", "Telegram", "other", "private")
	saveChannelEvent(t, pool, "alpha", "Email", "other-channel", "private")
	d.deliverOne(ctx, a, "Telegram")
	countACK(1)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	d = &CallbackDispatcher{URL: dsn} // Real reconnect, no in-memory progress.
	d.deliverOne(ctx, a, "Telegram")
	countACK(2)
	c, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = acknowledgeChannelEvents(ctx, c.Conn(), "alpha", "Telegram", []int64{lower, higher}); err != nil {
		t.Fatal(err)
	}
	c.Release()
	d.deliverOne(ctx, a, "Telegram")
	countACK(2)
	mu.Lock()
	if seen[lower] != 1 || seen[higher] != 1 {
		t.Error("late commit lost or ACK duplicated")
	}
	reject = true
	mu.Unlock()
	failed := saveChannelEvent(t, pool, "alpha", "Telegram", "retry", "lost ACK")
	d.deliverOne(ctx, a, "Telegram")
	countACK(2)
	mu.Lock()
	reject = false
	invalidAck = true
	mu.Unlock()
	due()
	d.deliverOne(ctx, a, "Telegram")
	countACK(2)
	mu.Lock()
	invalidAck = false
	mu.Unlock()
	due()
	d = &CallbackDispatcher{URL: dsn}
	d.deliverOne(ctx, a, "Telegram")
	countACK(3)
	var failures int
	if err = pool.QueryRow(ctx, "SELECT failures FROM relay_channel_callback_delivery WHERE event_id=$1", failed).Scan(&failures); err != nil || failures != 0 {
		t.Fatal("failure did not clear", err)
	}
	// Core rejects a malformed event in a batch. Retries isolate it, then back
	// off while valid following messages continue through the real HTTP path.
	bad := saveChannelEvent(t, pool, "alpha", "Telegram", "malformed", "syntactically JSON but semantically invalid")
	good := saveChannelEvent(t, pool, "alpha", "Telegram", "following", "valid message")
	mu.Lock()
	poison = bad
	mu.Unlock()
	d.deliverOne(ctx, a, "Telegram")
	countACK(3)
	due()
	d.deliverOne(ctx, a, "Telegram")
	d.deliverOne(ctx, a, "Telegram")
	countACK(4)
	var pending bool
	if err = pool.QueryRow(ctx, "SELECT NOT acknowledged AND failures=2 AND next_attempt>now() FROM relay_channel_callback_delivery WHERE event_id=$1", bad).Scan(&pending); err != nil || !pending {
		t.Fatal("poison not backed off", err)
	}
	mu.Lock()
	if seen[good] != 1 {
		t.Error("following message blocked")
	}
	mu.Unlock()
	// Recovery is scoped, audited, and schedules only this stable event identity.
	for _, scope := range [][2]string{{"other", "Telegram"}, {"alpha", "Email"}} {
		if d.RecoverCallback(ctx, scope[0], scope[1], bad, 42, "quarantine", "Verified malformed event") == nil {
			t.Fatal("cross-scope recovery")
		}
	}
	if err = d.RecoverCallback(ctx, "alpha", "Telegram", bad, 42, "quarantine", "Verified malformed event"); err != nil {
		t.Fatal(err)
	}
	if err = d.RecoverCallback(ctx, "alpha", "Telegram", bad, 42, "replay", "Core validation corrected"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	poison = 0
	mu.Unlock()
	d.deliverOne(ctx, a, "Telegram")
	countACK(5)
	if d.RecoverCallback(ctx, "alpha", "Telegram", bad, 42, "replay", "Already acknowledged event") == nil {
		t.Fatal("ACK replay allowed")
	}
	var audited int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM relay_channel_callback_audit WHERE event_id=$1 AND actor=42", bad).Scan(&audited); err != nil || audited != 2 {
		t.Fatal("missing audit", err)
	}
	oversized := saveChannelEvent(t, pool, "alpha", "Telegram", "oversized", strings.Repeat("x", 3<<20))
	next := saveChannelEvent(t, pool, "alpha", "Telegram", "after-oversized", "valid")
	d.deliverOne(ctx, a, "Telegram")
	d.deliverOne(ctx, a, "Telegram")
	countACK(6)
	if err = pool.QueryRow(ctx, "SELECT quarantined AND error='payload_too_large' FROM relay_channel_callback_delivery WHERE event_id=$1", oversized).Scan(&pending); err != nil || !pending {
		t.Fatal("oversize not preserved", err)
	}
	mu.Lock()
	if seen[next] != 1 {
		t.Error("oversize blocked following event")
	}
	mu.Unlock()
	// Byte-bound remains in force for normal delivery.
	for i := 0; i < 3; i++ {
		saveChannelEvent(t, pool, "alpha", "Telegram", fmt.Sprint("large-", i), strings.Repeat("x", 800000))
	}
	mu.Lock()
	before := len(batches)
	mu.Unlock()
	d.deliverOne(ctx, a, "Telegram")
	d.deliverOne(ctx, a, "Telegram")
	countACK(9)
	mu.Lock()
	if fmt.Sprint(batches[before:]) != "[2 1]" {
		t.Fatalf("unbounded batches %v", batches[before:])
	}
	mu.Unlock()
	exhausted := saveChannelEvent(t, pool, "alpha", "Telegram", "permanent-rejection", "invalid Core event")
	mu.Lock()
	poison = exhausted
	mu.Unlock()
	for range 8 {
		due()
		d.deliverOne(ctx, a, "Telegram")
	}
	if err = pool.QueryRow(ctx, "SELECT quarantined AND failures=8 AND NOT acknowledged FROM relay_channel_callback_delivery WHERE event_id=$1", exhausted).Scan(&pending); err != nil || !pending {
		t.Fatal("callback retry budget not bounded", err)
	}
	// PostgreSQL timestamps beyond JSON's range exercise local encoding failure.
	malformed := saveChannelEvent(t, pool, "alpha", "Telegram", "unencodable", "invalid timestamp")
	if _, err = pool.Exec(ctx, "UPDATE relay_channel_events SET occurred_at='10000-01-01' WHERE id=$1", malformed); err != nil {
		t.Fatal(err)
	}
	saveChannelEvent(t, pool, "alpha", "Telegram", "after-unencodable", "valid")
	d.deliverOne(ctx, a, "Telegram")
	d.deliverOne(ctx, a, "Telegram")
	countACK(10)
	if err = pool.QueryRow(ctx, "SELECT failures=1 AND error='invalid_event_encoding' AND next_attempt>now() FROM relay_channel_callback_delivery WHERE event_id=$1", malformed).Scan(&pending); err != nil || !pending {
		t.Fatal("encoding failure not isolated", err)
	}

}

func TestIngressWakeIsCommittedAndFailureDoesNotWake(t *testing.T) {
	pool, _ := channelTestDB(t)
	wake := make(chan struct{}, 1)
	store := Store{DB: pool, Wake: wake}
	event := Event{Account: "wake", Channel: "Telegram", Direction: "inbound", ProviderID: "1", Sender: "42", Body: "Amount 125.50", OccurredAt: time.Now(), Payload: json.RawMessage(`{}`)}
	if err := store.Save(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("committed inbound did not wake")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_callback_delivery`).Scan(&count); err != nil || count != 1 {
		t.Fatal("wake preceded durable delivery", count, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.Save(cancelled, event); err == nil {
		t.Fatal("expected failed write")
	}
	select {
	case <-wake:
		t.Fatal("failed write woke dispatcher")
	default:
	}
}
