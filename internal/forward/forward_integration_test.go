package forward

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) (context.Context, Store) {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("relay_forward_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO relay_containers(container_id,name) VALUES('main','Main')`); err != nil {
		t.Fatal(err)
	}
	return ctx, Store{DB: db, Wake: make(chan struct{}, 1)}
}

// received records what a target got.
type received struct {
	mu       sync.Mutex
	bodies   [][]byte
	headers  []http.Header
	statuses []int
}

func (r *received) server(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.bodies, r.headers = append(r.bodies, body), append(r.headers, req.Header.Clone())
		status := 200
		if len(r.statuses) > 0 {
			status, r.statuses = r.statuses[0], r.statuses[1:]
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server
}

func record(t *testing.T, ctx context.Context, store Store) Record {
	t.Helper()
	records, _, err := store.Records(ctx, Scope{"WhatsApp", "main"}, 0, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records %v %v", records, err)
	}
	return records[0]
}

func makeDue(t *testing.T, ctx context.Context, store Store) {
	if _, err := store.DB.Exec(ctx, `UPDATE relay_forwards SET next_attempt_at=now()`); err != nil {
		t.Fatal(err)
	}
}

func TestForwardSendsExactBytesAndHeaders(t *testing.T) {
	ctx, store := testStore(t)
	target := &received{}
	server := target.server(t)
	scope := Scope{"WhatsApp", "main"}
	if _, err := store.AddTarget(ctx, scope, server.URL+"/webhooks/meta?k=1"); err != nil {
		t.Fatal(err)
	}
	body := []byte("{\"object\":\"whatsapp_business_account\",  \"entry\":[{\"id\":\"1\",\"x\":\"é\"}]}\n")
	mac := hmac.New(sha256.New, []byte("app-secret"))
	mac.Write(body)
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", "facebookexternalua")
	header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	header.Set("Cookie", "not-forwarded")
	if err := store.Capture(ctx, scope, header, body); err != nil {
		t.Fatal(err)
	}
	worker := Worker{DB: store.DB, Client: NewClient()}
	if worker.DeliverDue(ctx) != 1 {
		t.Fatal("forward not claimed")
	}
	got := target.headers[0]
	check := hmac.New(sha256.New, []byte("app-secret"))
	check.Write(target.bodies[0])
	if string(target.bodies[0]) != string(body) || got.Get("X-Hub-Signature-256") != "sha256="+hex.EncodeToString(check.Sum(nil)) {
		t.Fatal("target cannot verify the Meta signature")
	}
	if got.Get("User-Agent") != "facebookexternalua" || got.Get("Content-Type") != "application/json" || got.Get("Cookie") != "" {
		t.Fatalf("headers %v", got)
	}
	if r := record(t, ctx, store); r.State != "delivered" || r.Attempts != 1 || r.StatusCode != 200 || r.DeliveredAt == nil {
		t.Fatalf("record %+v", r)
	}
}

func TestForwardRetriesThenGivesUp(t *testing.T) {
	ctx, store := testStore(t)
	target := &received{statuses: []int{503, 302}}
	server := target.server(t)
	scope := Scope{"WhatsApp", "main"}
	store.AddTarget(ctx, scope, server.URL)
	store.Capture(ctx, scope, http.Header{}, []byte(`{}`))
	worker := Worker{DB: store.DB, Client: NewClient()}

	worker.DeliverDue(ctx)
	if r := record(t, ctx, store); r.State != "pending" || r.Attempts != 1 || r.Error != "HTTP 503" {
		t.Fatalf("first try %+v", r)
	}
	if worker.DeliverDue(ctx) != 0 {
		t.Fatal("retry ran before its delay")
	}
	makeDue(t, ctx, store)
	worker.DeliverDue(ctx)
	if r := record(t, ctx, store); r.State != "pending" || r.StatusCode != 302 {
		t.Fatalf("redirect was followed or counted as success: %+v", r)
	}
	makeDue(t, ctx, store)
	worker.DeliverDue(ctx)
	if r := record(t, ctx, store); r.State != "delivered" || r.Attempts != 3 {
		t.Fatalf("third try %+v", r)
	}

	server.Close()
	store.Capture(ctx, scope, http.Header{}, []byte(`{"second":true}`))
	for range Attempts {
		makeDue(t, ctx, store)
		worker.DeliverDue(ctx)
	}
	records, counts, _ := store.Records(ctx, scope, 0, 10)
	if records[0].State != "failed" || records[0].Attempts != Attempts || records[0].Error == "" || counts["failed"] != 1 || counts["delivered"] != 1 {
		t.Fatalf("down target %+v %v", records[0], counts)
	}
}

func TestForwardTargetsAndPruning(t *testing.T) {
	ctx, store := testStore(t)
	scope := Scope{"WhatsApp", "main"}
	if err := store.Capture(ctx, scope, http.Header{}, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var rows int
	store.DB.QueryRow(ctx, `SELECT count(*) FROM relay_forwards`).Scan(&rows)
	if rows != 0 {
		t.Fatal("captured without targets")
	}
	if _, err := store.AddTarget(ctx, Scope{"WhatsApp", "missing"}, "https://example.test/x"); err != ErrInvalidScope {
		t.Fatalf("unknown container: %v", err)
	}
	if _, err := store.AddTarget(ctx, scope, "http://example.test/x"); err != ErrInvalidURL {
		t.Fatalf("public http: %v", err)
	}
	for i := range MaxTargets {
		if _, err := store.AddTarget(ctx, scope, fmt.Sprintf("https://example.test/%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AddTarget(ctx, scope, "https://example.test/0"); err != nil {
		t.Fatal("adding the same URL again failed")
	}
	if _, err := store.AddTarget(ctx, scope, "https://example.test/extra"); err != ErrTooMany {
		t.Fatalf("limit: %v", err)
	}
	store.Capture(ctx, scope, http.Header{}, []byte(`{}`))
	store.DB.Exec(ctx, `UPDATE relay_forwards SET created_at=now()-interval '8 days' WHERE id IN (SELECT min(id) FROM relay_forwards)`)
	if err := (Worker{DB: store.DB}).Prune(ctx); err != nil {
		t.Fatal(err)
	}
	store.DB.QueryRow(ctx, `SELECT count(*) FROM relay_forwards`).Scan(&rows)
	if rows != MaxTargets-1 {
		t.Fatalf("after prune %d rows", rows)
	}
	targets, _ := store.Targets(ctx, scope)
	if err := store.RemoveTarget(ctx, scope, targets[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveTarget(ctx, Scope{"Telegram", "main"}, targets[1].ID); err == nil {
		t.Fatal("removed a target of another scope")
	}
}

func TestValidateURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://whatsapp.example.com/webhooks/meta": true,
		"http://127.0.0.1:8000/hook":                 true,
		"http://localhost/hook":                      true,
		"http://100.64.0.20/hook":                    true,
		"http://192.168.1.5/hook":                    true,
		"http://example.com/hook":                    false,
		"http://100.128.0.1/hook":                    false,
		"https://user:pass@example.com/":             false,
		"ftp://example.com/":                         false,
		"https:///nohost":                            false,
	} {
		if (ValidateURL(raw) == nil) != ok {
			t.Errorf("%s: want ok=%v", raw, ok)
		}
	}
}
