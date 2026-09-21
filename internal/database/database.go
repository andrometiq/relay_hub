package database

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
)

//go:embed migrations/*.sql
var Files embed.FS

func CheckVersion(ctx context.Context, conn *pgx.Conn) error {
	var version int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		return fmt.Errorf("cannot read PostgreSQL version")
	}
	if version/10000 != 18 {
		return fmt.Errorf("PostgreSQL 18.x required; detected major %d", version/10000)
	}
	return nil
}

func Migrate(ctx context.Context, conn *pgx.Conn) error {
	if err := CheckVersion(ctx, conn); err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351009)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS relay_schema_migrations (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := Files.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	expected := map[string]string{}
	for _, entry := range entries {
		body, _ := Files.ReadFile("migrations/" + entry.Name())
		expected[entry.Name()] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	rows, err := tx.Query(ctx, "SELECT name, checksum FROM relay_schema_migrations")
	if err != nil {
		return err
	}
	applied := map[string]string{}
	for rows.Next() {
		var n, c string
		if err = rows.Scan(&n, &c); err != nil {
			rows.Close()
			return err
		}
		applied[n] = c
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	for n, c := range applied {
		if expected[n] != c {
			return fmt.Errorf("migration drift or newer database: %s", n)
		}
	}
	for _, entry := range entries {
		n := entry.Name()
		if _, ok := applied[n]; ok {
			continue
		}
		body, _ := Files.ReadFile("migrations/" + n)
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s failed: %w", n, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO relay_schema_migrations(name,checksum) VALUES ($1,$2)", n, expected[n]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func Ready(ctx context.Context, conn *pgx.Conn) error {
	if err := CheckVersion(ctx, conn); err != nil {
		return err
	}
	rows, err := conn.Query(ctx, "SELECT name, checksum FROM relay_schema_migrations")
	if err != nil {
		return fmt.Errorf("schema not ready; run migrate")
	}
	actual := map[string]string{}
	for rows.Next() {
		var n, c string
		if err = rows.Scan(&n, &c); err != nil {
			rows.Close()
			return err
		}
		actual[n] = c
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	entries, _ := Files.ReadDir("migrations")
	if len(actual) != len(entries) {
		return fmt.Errorf("schema version mismatch; run migrate")
	}
	for _, entry := range entries {
		body, _ := Files.ReadFile("migrations/" + entry.Name())
		if actual[entry.Name()] != fmt.Sprintf("%x", sha256.Sum256(body)) {
			return fmt.Errorf("schema checksum mismatch")
		}
	}
	return nil
}
