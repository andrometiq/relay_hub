package accounts

import (
	"bytes"
	"context"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func TestRotationPreservesPlaintextAndRejectsOldKey(t *testing.T) {
	old, _ := New(nil, "rotation-old-fixture")
	next, _ := New(nil, "rotation-new-fixture")
	for _, plain := range []string{"provider-token", `{"core_api_secret":"private-fixture"}`, ""} {
		original, nonce, _ := old.box(plain)
		changed, newNonce, state, err := rotateField(old, next, original, nonce)
		if err != nil || state != "rotated" {
			t.Fatal("rotation failed", state, err)
		}
		recovered, err := next.unbox(changed, newNonce)
		if err != nil || recovered != plain {
			t.Fatal("new key lost plaintext")
		}
		if _, err = old.unbox(changed, newNonce); err == nil {
			t.Fatal("old key still decrypts rotated value")
		}
		replay, replayNonce, state, err := rotateField(old, next, changed, newNonce)
		if err != nil || state != "current" || !bytes.Equal(replay, changed) || !bytes.Equal(replayNonce, newNonce) {
			t.Fatal("retry changed an already rotated field")
		}
	}
}
func TestRotationRetainsUnreadableFields(t *testing.T) {
	old, _ := New(nil, "rotation-old-fixture")
	next, _ := New(nil, "rotation-new-fixture")
	for _, value := range []struct{ cipher, nonce []byte }{{[]byte("unreadable"), []byte("bad-nonce")}, {[]byte{}, []byte{}}} {
		boxed, nonce, state, err := rotateField(old, next, value.cipher, value.nonce)
		if err != nil || !bytes.Equal(boxed, value.cipher) || !bytes.Equal(nonce, value.nonce) {
			t.Fatal("unreadable credential was changed")
		}
		if len(value.cipher) > 0 && state != "unreadable" {
			t.Fatal("corruption was not reported")
		}
	}
}

func TestRotationDatabaseTransactionAndUnreadablePreservation(t *testing.T) {
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
	schema := fmt.Sprintf("relay_rotation_test_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	old, _ := New(db, "rotation-old-fixture")
	account := Account{ID: "rotation-fixture", Site: "https://example.invalid", Enabled: true, Channels: []Channel{{Channel: "Telegram", Enabled: true, Config: map[string]any{"core_api_secret": "private-fixture"}}, {Channel: "Email", Enabled: false}}}
	if err = old.Save(ctx, account, map[string]string{"Telegram": "bot-fixture", "Email": "unreadable-fixture"}); err != nil {
		t.Fatal(err)
	}
	broken := []byte("retained-invalid-ciphertext")
	if _, err = db.Exec(ctx, `UPDATE relay_channel_credentials SET secret_ciphertext=$1 WHERE channel='Email'`, broken); err != nil {
		t.Fatal(err)
	}
	report, err := old.RotateKey(ctx, "rotation-new-fixture", false)
	if err != nil || report.Rows != 2 || report.RotatedFields != 3 || len(report.Unreadable) != 1 {
		t.Fatal("dry run classification", report, err)
	}
	if value, err := old.Runtime(ctx); err != nil || len(value) != 1 || value[0].Secret != "bot-fixture" {
		t.Fatal("dry run changed credential")
	}
	report, err = old.RotateKey(ctx, "rotation-new-fixture", true)
	if err != nil || report.DryRun {
		t.Fatal("apply", report, err)
	}
	next, _ := New(db, "rotation-new-fixture")
	if value, err := next.Runtime(ctx); err != nil || len(value) != 1 || value[0].Secret != "bot-fixture" {
		t.Fatal("new key cannot read rotated token")
	}
	var cipher, nonce []byte
	if err = db.QueryRow(ctx, `SELECT config_ciphertext,config_nonce FROM relay_channel_credentials WHERE channel='Telegram'`).Scan(&cipher, &nonce); err != nil {
		t.Fatal(err)
	}
	values, err := next.configSecrets(cipher, nonce)
	if err != nil || values["core_api_secret"] != "private-fixture" {
		t.Fatal("private config lost")
	}
	if err = db.QueryRow(ctx, `SELECT secret_ciphertext FROM relay_channel_credentials WHERE channel='Email'`).Scan(&cipher); err != nil || !bytes.Equal(cipher, broken) {
		t.Fatal("unreadable field replaced")
	}
	report, err = old.RotateKey(ctx, "rotation-new-fixture", true)
	if err != nil || report.RotatedFields != 0 || report.AlreadyUsingNewKey != 3 || len(report.Unreadable) != 1 {
		t.Fatal("rotation replay", report, err)
	}
}
