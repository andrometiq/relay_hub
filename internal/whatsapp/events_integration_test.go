package whatsapp

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDurableEventDeduplicationAndAccountIsolation(t *testing.T) {
	connstr := os.Getenv("RELAY_TEST_DATABASE_URL")
	if connstr == "" {
		t.Skip("requires isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, e := pgx.Connect(ctx, connstr)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	schema := fmt.Sprintf("relay_events_test_%d", time.Now().UnixNano())
	if _, e = c.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	_, e = c.Exec(ctx, "CREATE TABLE "+schema+".relay_whatsapp_events(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,account text,digest text,payload jsonb,raw_payload jsonb,lane text NOT NULL DEFAULT 'realtime',UNIQUE(account,digest))")
	if e != nil {
		t.Fatal(e)
	}
	scoped := connstr + " search_path=" + schema
	if strings.HasPrefix(connstr, "postgres") {
		u, _ := url.Parse(connstr)
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	statusSQL, err := os.ReadFile("../database/migrations/0004_delivery_status.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, string(statusSQL)); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"0003_outbox.sql", "0037_whatsapp_bulk.sql"} {
		raw, err := os.ReadFile("../database/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.Exec(ctx, string(raw)); err != nil {
			t.Fatal(file, err)
		}
	}
	store := PostgresEvents{URL: scoped}
	body := []byte(`{"event":"sample"}`)
	for _, account := range []string{"a", "a", "b"} {
		if e = store.Save(ctx, account, body); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := store.List(ctx, "a", 0)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one deduplicated account event, got %d", len(rows))
	}
	rows, e = store.List(ctx, "a", rows[0].ID)
	if e != nil || len(rows) != 0 {
		t.Fatal("cursor did not advance")
	}
	for _, status := range []string{"read", "sent", "delivered", "failed"} {
		body := []byte(fmt.Sprintf(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"wamid.test","status":"%s","timestamp":"100"}]}}]}]}`, status))
		if err := store.Save(ctx, "a", body); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	if err := c.QueryRow(ctx, "SELECT status FROM relay_whatsapp_status WHERE account='a' AND provider_id='wamid.test'").Scan(&status); err != nil || status != "read" {
		t.Fatal("status regressed", status, err)
	}
}
