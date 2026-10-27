package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migratedPool gives a pool on a fresh migrated schema of the test database.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RELAY_TEST_DATABASE_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("relay_waba_test_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err = database.Migrate(ctx, conn.Conn()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestNumbersOfOneWABASyncTemplatesOnce(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)
	first, second := testAccount(), testAccount()
	first.SyncMetadata, second.SyncMetadata = true, true
	second.ID, second.PhoneID, second.CoreToken = "site-a-second", "333", strings.Repeat("b", 64)
	s, err := New([]Account{first, second})
	if err != nil {
		t.Fatal(err)
	}
	var lists atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message_templates") {
			lists.Add(1)
			fmt.Fprintf(w, `{"data":[{"id":"t%d","name":"order_update"}]}`, lists.Load())
			return
		}
		fmt.Fprint(w, `{"id":"profile","verified_name":"Fixture"}`)
	}))
	defer server.Close()
	s.base = server.URL
	m := &MetadataSync{DB: pool, Service: s}
	s.Metadata = m

	for _, a := range []Account{first, second} {
		if err := m.Sync(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if lists.Load() != 1 {
		t.Fatalf("template lists for one WABA: %d", lists.Load())
	}
	for _, a := range []Account{first, second} {
		value, err := m.Read(ctx, a.ID)
		if templates, _ := value["templates"].([]any); err != nil || len(templates) != 1 {
			t.Fatalf("%s templates: %v %v", a.ID, value, err)
		}
	}

	var webhook webhookEntry
	json.Unmarshal([]byte(`{"id":"222","changes":[{"field":"message_template_status_update"}]}`), &webhook)
	if !templateChange(webhook) {
		t.Fatal("template status webhook not seen as a template change")
	}
	if _, err := s.Execute(ctx, first, Input{Operation: "templates.delete", Name: "order_update"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Account{second, first} {
		if err := m.Sync(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	value, _ := m.Read(ctx, first.ID)
	templates, _ := value["templates"].([]any)
	if lists.Load() != 2 || len(templates) != 1 || templates[0].(map[string]any)["id"] != "t2" {
		t.Fatalf("stale WABA refresh: lists=%d %v", lists.Load(), value)
	}
}
