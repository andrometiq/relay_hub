// relay-keyrotate rotates encrypted channel secrets without exposing plaintext.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/accounts"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"time"
)

func main() {
	apply := flag.Bool("apply", false, "commit rotation; default is a read-only preview")
	offline := flag.Bool("service-stopped", false, "confirm the Hub and credential writers are stopped")
	flag.Parse()
	if *apply && !*offline {
		fmt.Fprintln(os.Stderr, "Apply requires --service-stopped after stopping the Hub and credential writers.")
		os.Exit(2)
	}
	oldKey, newKey := os.Getenv("RELAY_CREDENTIAL_KEY"), os.Getenv("RELAY_NEW_CREDENTIAL_KEY")
	if oldKey == "" || newKey == "" || os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "Set DATABASE_URL, RELAY_CREDENTIAL_KEY and RELAY_NEW_CREDENTIAL_KEY through the process environment.")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not open credential database.")
		os.Exit(1)
	}
	defer db.Close()
	store, err := accounts.New(db, oldKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid credential key.")
		os.Exit(1)
	}
	report, err := store.RotateKey(ctx, newKey, *apply)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(report); err != nil {
		os.Exit(1)
	}
}
