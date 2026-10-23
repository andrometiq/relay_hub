package channels

import (
	"context"
	"github.com/andrometiq/relay_hub/internal/accounts"
	"github.com/andrometiq/relay_hub/internal/email"
	"testing"
)

func TestLoadRegistry(t *testing.T) {
	r, err := Load([]accounts.RuntimeCredential{{AccountID: "tg", Channel: "Telegram", Secret: "123456:abcdefghijklmnopqrstuvwxyzABCDE"}, {AccountID: "mail", Channel: "Email", Secret: "pw", Config: map[string]any{"host": "smtp.example.com", "port": float64(587), "from": "relay@example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Status()
	if s.Telegram != 1 || s.Email != 1 {
		t.Fatalf("unexpected status %#v", s)
	}
}
func TestRejectBadEmailRuntime(t *testing.T) {
	if _, err := Load([]accounts.RuntimeCredential{{AccountID: "mail", Channel: "Email", Secret: "pw", Config: map[string]any{"host": "smtp.example.com", "port": float64(587)}}}); err == nil {
		t.Fatal("bad email accepted")
	}
}
func TestEmailWebhookRejectsInvalidSecretAndPayload(t *testing.T) {
	r := &Registry{Email: map[string]*email.Client{}}
	if err := r.EmailWebhook(context.Background(), Store{}, "mail", "secret", []byte("wrong"), []byte(`{}`)); err == nil {
		t.Fatal("invalid email webhook accepted")
	}
}
