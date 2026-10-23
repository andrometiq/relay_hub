package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type fakeEventReader struct{ calls int }

func (b *fakeEventReader) List(_ context.Context, a, c string, n int64) (eventPage, error) {
	b.calls++
	return eventPage{Items: []inspectedEvent{}}, nil
}
func (b *fakeEventReader) Detail(_ context.Context, a, c string, n int64) (eventDetail, error) {
	b.calls++
	if a != "fixture" || c != "Telegram" || n != 1 {
		return eventDetail{}, errors.New("wrong scope")
	}
	return eventDetail{Raw: "{}"}, nil
}
func TestEventInspectorAuthorization(t *testing.T) {
	for _, tc := range []struct {
		role, query string
		status      int
	}{{"guest", "?account=fixture&channel=Telegram", 401}, {"operator", "?account=fixture&channel=Telegram", 403}, {"admin", "?channel=Telegram", 400}, {"admin", "?account=fixture&channel=Other", 400}, {"admin", "?account=fixture&channel=Telegram&id=-1", 400}, {"admin", "?account=fixture&channel=Telegram&id=1&before=2", 400}, {"admin", "?account=fixture&channel=Telegram&id=1", 200}} {
		b := &fakeEventReader{}
		h := eventInspector(b, func(*http.Request) (auth.User, error) {
			if tc.role == "guest" {
				return auth.User{}, errors.New("unauthenticated")
			}
			return auth.User{Role: tc.role}, nil
		})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/event-inspector"+tc.query, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.query, w.Code)
		}
		if tc.status != 200 && b.calls != 0 {
			t.Fatal("invalid request reached backend")
		}
	}
}
func TestEventInspectorDatabase(t *testing.T) {
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
	schema := fmt.Sprintf("relay_inspect_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, c.Conn())
	c.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,body,payload) SELECT 'fixture','Telegram','inbound',n::text,repeat('x',500),jsonb_build_object('text',repeat('x',70000)) FROM generate_series(1,51) n`); err != nil {
		t.Fatal(err)
	}
	b := postgresEventReader{db: db}
	page, err := b.List(ctx, "fixture", "Telegram", 0)
	if err != nil || len(page.Items) != 50 || !page.More {
		t.Fatalf("pagination %v %v", page, err)
	}
	for _, r := range page.Items {
		if len(r.Preview) > 240 || r.Callback != "Pending" {
			t.Fatal("unbounded/incorrect summary")
		}
	}
	next, err := b.List(ctx, "fixture", "Telegram", page.Items[49].ID)
	if err != nil || len(next.Items) != 1 || next.More {
		t.Fatal("keyset pagination failed", err)
	}
	detail, err := b.Detail(ctx, "fixture", "Telegram", page.Items[0].ID)
	if err != nil || len(detail.Raw) != 65536 || !detail.Truncated {
		t.Fatal("payload limit", err)
	}
	if _, err = b.Detail(ctx, "other", "Telegram", page.Items[0].ID); err == nil {
		t.Fatal("cross account payload")
	}
	if _, err = b.Detail(ctx, "fixture", "Email", page.Items[0].ID); err == nil {
		t.Fatal("cross channel payload")
	}
	if _, err = db.Exec(ctx, `INSERT INTO relay_channel_callback_quarantine(event_id,account,channel,reason) VALUES($1,'fixture','Telegram','payload_too_large')`, page.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	page, err = b.List(ctx, "fixture", "Telegram", 0)
	if err != nil || page.Items[0].Callback != "Quarantined" {
		t.Fatal("missing quarantine state")
	}
	var id int64
	if err = db.QueryRow(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane) VALUES('fixture','digest','{}','status') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO relay_callback_cursor(account,lane,last_id,status_offset) VALUES('fixture','status',$1,1)`, id); err != nil {
		t.Fatal(err)
	}
	page, err = b.List(ctx, "fixture", "WhatsApp", 0)
	if err != nil || page.Items[0].Callback != "Pending" {
		t.Fatal("partial status marked forwarded")
	}
	if _, err = db.Exec(ctx, `UPDATE relay_callback_cursor SET status_offset=0 WHERE account='fixture'`); err != nil {
		t.Fatal(err)
	}
	page, err = b.List(ctx, "fixture", "WhatsApp", 0)
	if err != nil || page.Items[0].Callback != "Forwarded" {
		t.Fatal("acknowledgement not shown")
	}
}
