package channels

import (
	"context"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/email"
	"strings"
	"testing"
)

func TestLoadRegistry(t *testing.T) {
	r, err := Load([]containers.RuntimeCredential{{Key: "tg", Channel: "Telegram", Secret: "123456:abcdefghijklmnopqrstuvwxyzABCDE"}, {Key: "mail", Channel: "Email", Secret: "pw", Config: map[string]any{"host": "smtp.example.com", "port": float64(587), "from": "relay@example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Status()
	if s.Telegram != 1 || s.Email != 1 {
		t.Fatalf("unexpected status %#v", s)
	}
}
func TestRejectBadEmailRuntime(t *testing.T) {
	if _, err := Load([]containers.RuntimeCredential{{Key: "mail", Channel: "Email", Secret: "pw", Config: map[string]any{"host": "smtp.example.com", "port": float64(587)}}}); err == nil {
		t.Fatal("bad email accepted")
	}
}
func TestEmailWebhookRejectsInvalidSecretAndPayload(t *testing.T) {
	r := &Registry{Email: map[string]*email.Client{}}
	if err := r.EmailWebhook(context.Background(), Store{}, "mail", "secret", []byte("wrong"), []byte(`{}`)); err == nil {
		t.Fatal("invalid email webhook accepted")
	}
}

func TestEmailWebhookAccountIsolationAndPayloadClaims(t *testing.T) {
	pool, _ := channelTestDB(t)
	r, err := Load([]containers.RuntimeCredential{
		{Key: "alpha", Channel: "Email", Config: map[string]any{"host": "smtp.invalid", "port": float64(587), "from": "alpha@example.invalid"}},
		{Key: "beta", Channel: "Email", Config: map[string]any{"host": "smtp.invalid", "port": float64(587), "from": "beta@example.invalid"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	master := strings.Repeat("m", 32)
	alpha, err := r.EmailWebhookSecret("alpha", master)
	if err != nil {
		t.Fatal(err)
	}
	beta, _ := r.EmailWebhookSecret("beta", master)
	if alpha == beta || alpha == master {
		t.Fatal("unscoped email secret")
	}
	body := []byte(`{"id":"fixture","sender":"Sender@example.invalid","recipient":"beta@example.invalid","body":"Body","payload":{"authentication":{"status":"pass"},"delivery_statuses":[{"state":"delivered"}]}}`)
	for _, secret := range []string{alpha, master, ""} {
		if r.EmailWebhook(t.Context(), Store{DB: pool}, "beta", master, []byte(secret), body) == nil {
			t.Fatal("cross-account/master secret accepted")
		}
	}
	for range 2 {
		if err = r.EmailWebhook(t.Context(), Store{DB: pool}, "alpha", master, []byte(alpha), body); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var status string
	err = pool.QueryRow(t.Context(), `SELECT count(*),min(payload#>>'{authentication,status}') FROM relay_channel_events WHERE account='alpha'`).Scan(&count, &status)
	if err != nil || count != 1 || status != "unverified" {
		t.Fatal(count, status, err)
	}
	if _, err = r.EmailWebhookSecret("missing", master); err == nil {
		t.Fatal("unknown account enabled")
	}
	if _, err = r.EmailWebhookSecret("alpha", "short"); err == nil {
		t.Fatal("short master accepted")
	}
}
