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
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSimulatedCallbackRecoveryAndIsolation(t *testing.T) {
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("relay_channel_sim_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, name := range []string{"0013_channel_events.sql", "0014_channel_callback_cursor.sql", "0027_channel_callback_quarantine.sql"} {
		raw, err := os.ReadFile("../database/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	store := Store{DB: pool}
	for _, account := range []string{"alpha", "other"} {
		for i := 0; i < 53; i++ {
			e := Event{Account: account, Channel: "Telegram", Direction: "inbound", ProviderID: fmt.Sprint(i), Sender: "42", Body: "synthetic", OccurredAt: time.Now(), Payload: json.RawMessage(`{}`)}
			if err = store.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
			if err = store.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM relay_channel_events").Scan(&count); err != nil || count != 106 {
		t.Fatalf("dedupe count=%d err=%v", count, err)
	}
	reject := true
	invalidAck := true
	sizes := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if r.Header.Get("X-Relay-Signature") != hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("Authorization") != "token key:secret" {
			t.Error("invalid authentication")
		}
		var body struct {
			Account string `json:"account"`
			Events  []struct {
				Payload struct {
					Channel string `json:"channel"`
				} `json:"payload"`
			} `json:"events"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		if body.Account != "alpha" {
			t.Error("cross-account callback")
		}
		for _, e := range body.Events {
			if e.Payload.Channel != "Telegram" {
				t.Error("wrong channel")
			}
		}
		sizes = append(sizes, len(body.Events))
		if reject {
			w.WriteHeader(503)
			return
		}
		if invalidAck {
			w.Write([]byte(`{"message":{"accepted":0}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]int{"accepted": len(body.Events)}})
	}))
	defer server.Close()
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
	d := CallbackDispatcher{URL: scoped}
	a := RuntimeCallback{Account: "alpha", Site: server.URL, APIKey: "key", APISecret: "secret", CallbackSecret: "test-secret"}
	d.deliver(ctx, a)
	var cursor int64
	if err = pool.QueryRow(ctx, "SELECT last_id FROM relay_channel_callback_cursor WHERE account='alpha' AND channel='Telegram'").Scan(&cursor); err != nil || cursor != 0 {
		t.Fatalf("failed send advanced cursor: %d %v", cursor, err)
	}
	reject = false
	if _, err = pool.Exec(ctx, "UPDATE relay_channel_callback_cursor SET next_attempt=now()"); err != nil {
		t.Fatal(err)
	}
	d.deliver(ctx, a)
	if err = pool.QueryRow(ctx, "SELECT last_id FROM relay_channel_callback_cursor WHERE account='alpha' AND channel='Telegram'").Scan(&cursor); err != nil || cursor != 0 {
		t.Fatalf("invalid acknowledgement advanced cursor: %d %v", cursor, err)
	}
	invalidAck = false
	if _, err = pool.Exec(ctx, "UPDATE relay_channel_callback_cursor SET next_attempt=now()"); err != nil {
		t.Fatal(err)
	}
	d.deliver(ctx, a)
	// Restart dispatcher: recovery must use persisted cursor.
	d = CallbackDispatcher{URL: scoped}
	d.deliver(ctx, a)
	d.deliver(ctx, a)
	if fmt.Sprint(sizes) != "[50 50 50 3]" {
		t.Fatalf("unexpected batches %v", sizes)
	}
	// The public replay path must use the same bounded envelope as production.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err = pool.QueryRow(ctx, "SELECT max(id) FROM relay_channel_events WHERE account='alpha'").Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = store.Save(ctx, Event{Account: "alpha", Channel: "Telegram", Direction: "inbound", ProviderID: fmt.Sprintf("large-%d", i), Body: strings.Repeat("x", 800000), OccurredAt: time.Now(), Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(sizes)
	last, err := d.Deliver(ctx, conn.Conn(), a, cursor)
	if err != nil || last <= cursor {
		t.Fatalf("bounded replay failed %d %v", last, err)
	}
	last, err = d.Deliver(ctx, conn.Conn(), a, last)
	if err != nil || fmt.Sprint(sizes[before:]) != "[2 1]" {
		t.Fatalf("byte bound not applied %v %v", sizes[before:], err)
	}
	if err = store.Save(ctx, Event{Account: "alpha", Channel: "Telegram", Direction: "inbound", ProviderID: "oversized", Body: strings.Repeat("x", 3<<20), OccurredAt: time.Now(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	before = len(sizes)
	next, err := d.Deliver(ctx, conn.Conn(), a, last)
	if err == nil || next != last || len(sizes) != before {
		t.Fatal("oversized event skipped or transmitted")
	}
	// Production cursor quarantines the oversized row atomically, then resumes.
	if _, err = pool.Exec(ctx, "UPDATE relay_channel_callback_cursor SET last_id=$1,next_attempt=now() WHERE account='alpha' AND channel='Telegram'", last); err != nil {
		t.Fatal(err)
	}
	d.deliver(ctx, a)
	var quarantined int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM relay_channel_callback_quarantine WHERE account='alpha' AND reason='payload_too_large'").Scan(&quarantined); err != nil || quarantined != 1 {
		t.Fatalf("quarantine missing %d %v", quarantined, err)
	}
	if err = store.Save(ctx, Event{Account: "alpha", Channel: "Telegram", Direction: "inbound", ProviderID: "after-poison", Body: "next message", OccurredAt: time.Now(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	before = len(sizes)
	d.deliver(ctx, a)
	if len(sizes) != before+1 || sizes[len(sizes)-1] != 1 {
		t.Fatalf("poison event blocked later messages: %v", sizes)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM relay_channel_events WHERE account='alpha' AND provider_id='oversized'").Scan(&quarantined); err != nil || quarantined != 1 {
		t.Fatal("original event was lost")
	}

	d.Update([]RuntimeCallback{a})
	var poisonID int64
	if err = pool.QueryRow(ctx, "SELECT id FROM relay_channel_events WHERE account='alpha' AND provider_id='oversized'").Scan(&poisonID); err != nil {
		t.Fatal(err)
	}
	if err = d.RetryQuarantined(ctx, a.Account, "Telegram", poisonID); err == nil {
		t.Fatal("oversized retry was accepted")
	}
	// A small retained event models retry after a delivery/encoding correction.
	var recoverID int64
	if err = pool.QueryRow(ctx, "SELECT id FROM relay_channel_events WHERE account='alpha' AND provider_id='after-poison'").Scan(&recoverID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO relay_channel_callback_quarantine(event_id,account,channel,reason) VALUES($1,'alpha','Telegram','payload_too_large')", recoverID); err != nil {
		t.Fatal(err)
	}
	before = len(sizes)
	if err = d.RetryQuarantined(ctx, a.Account, "Email", recoverID); err == nil {
		t.Fatal("cross-channel recovery allowed")
	}
	reject = true
	if err = d.RetryQuarantined(ctx, a.Account, "Telegram", recoverID); err == nil {
		t.Fatal("failed recovery cleared")
	}
	reject = false
	if err = d.RetryQuarantined(ctx, a.Account, "Telegram", recoverID); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes[before:]) != "[1 1]" {
		t.Fatalf("replayed adjacent events %v", sizes[before:])
	}
	var resolved bool
	if err = pool.QueryRow(ctx, "SELECT resolved_at IS NOT NULL FROM relay_channel_callback_quarantine WHERE event_id=$1", recoverID).Scan(&resolved); err != nil || !resolved {
		t.Fatal("recovery not recorded")
	}
	if err = d.RetryQuarantined(ctx, a.Account, "Telegram", recoverID); err == nil {
		t.Fatal("already resolved event replayed")
	}

}
