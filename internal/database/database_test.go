package database

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"testing"
	"time"
)

func testConnection(t *testing.T) (*pgx.Conn, context.Context) {
	t.Helper()
	url := os.Getenv("RELAY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set RELAY_TEST_DATABASE_URL for real PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("cannot connect to test database")
	}
	schema := fmt.Sprintf("relay_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		conn.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(cleanup)
	})
	return conn, ctx
}
func TestMigrationRepeatAndReady(t *testing.T) {
	c, ctx := testConnection(t)
	if Ready(ctx, c) == nil {
		t.Fatal("unmigrated database reported ready")
	}
	if e := Migrate(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e := Migrate(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e := Ready(ctx, c); e != nil {
		t.Fatal(e)
	}
	var count int
	c.QueryRow(ctx, "SELECT count(*) FROM relay_schema_migrations").Scan(&count)
	if count != 1 {
		t.Fatalf("expected one applied migration, got %d", count)
	}
}
func TestChecksumDriftRejected(t *testing.T) {
	c, ctx := testConnection(t)
	if e := Migrate(ctx, c); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Exec(ctx, "UPDATE relay_schema_migrations SET checksum='edited'"); e != nil {
		t.Fatal(e)
	}
	if Migrate(ctx, c) == nil {
		t.Fatal("edited migration accepted")
	}
	if Ready(ctx, c) == nil {
		t.Fatal("drifted schema ready")
	}
}
func TestNewerSchemaRejected(t *testing.T) {
	c, ctx := testConnection(t)
	if e := Migrate(ctx, c); e != nil {
		t.Fatal(e)
	}
	c.Exec(ctx, "INSERT INTO relay_schema_migrations(name,checksum) VALUES('9999_future.sql','future')")
	if Migrate(ctx, c) == nil {
		t.Fatal("older binary accepted newer schema")
	}
}
func TestFailedMigrationRollsBackLedger(t *testing.T) {
	c, ctx := testConnection(t)
	// Existing incompatible table makes migration fail after ledger creation.
	if _, e := c.Exec(ctx, "CREATE TABLE relay_hub_metadata (other text)"); e != nil {
		t.Fatal(e)
	}
	if Migrate(ctx, c) == nil {
		t.Fatal("expected migration failure")
	}
	var ledger *string
	if e := c.QueryRow(ctx, "SELECT to_regclass('relay_schema_migrations')::text").Scan(&ledger); e != nil {
		t.Fatal(e)
	}
	if ledger != nil {
		t.Fatal("failed migration left a committed ledger")
	}
}
