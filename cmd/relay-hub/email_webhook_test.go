package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/email"
)

func TestEmailWebhookSettingsAuthorization(t *testing.T) {
	client, err := email.New(email.Config{AccountID: "mail", Host: "smtp.invalid", Port: 587, From: "inbox@example.invalid", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	registry := &channels.Registry{Email: map[string]*email.Client{"mail": client}}
	master := strings.Repeat("m", 32)
	for _, role := range []string{"operator", "admin"} {
		handler := channelWebhookSettings("email", registry.EmailWebhookSecret, registry, master, func(*http.Request) (auth.User, error) { return auth.User{Role: role}, nil })
		req := httptest.NewRequest("GET", "/api/accounts/mail/email/webhook", nil)
		req.SetPathValue("account", "mail")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if role == "operator" {
			if response.Code != 403 {
				t.Fatal("operator received credential")
			}
			continue
		}
		var body struct {
			Path  string `json:"path"`
			Token string `json:"secret_token"`
		}
		expected, _ := registry.EmailWebhookSecret("mail", master)
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || body.Token != expected || body.Path != "/webhooks/email/mail" || strings.Contains(response.Body.String(), master) || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("invalid scoped credential response", err)
		}
	}
}
