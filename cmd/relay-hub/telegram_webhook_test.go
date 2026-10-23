package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

func TestTelegramWebhookRegistrationSettingsAuthorization(t *testing.T) {
	botToken := "123456:" + strings.Repeat("a", 24)
	bot, err := telegram.New(telegram.Config{AccountID: "a", BotToken: botToken, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	registry := &channels.Registry{Telegram: map[string]*telegram.Client{"a": bot}}
	master := strings.Repeat("synthetic-master-", 3)
	for _, tc := range []struct {
		name, role, account, key string
		want                     int
	}{
		{"no session", "", "a", master, 401},
		{"operator", "operator", "a", master, 403},
		{"unknown account", "admin", "missing", master, 503},
		{"unconfigured master", "admin", "a", "", 503},
		{"admin", "admin", "a", master, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := telegramWebhookSettings(registry, tc.key, func(*http.Request) (auth.User, error) {
				if tc.role == "" {
					return auth.User{}, errors.New("invalid session")
				}
				return auth.User{Role: tc.role}, nil
			})
			mux := http.NewServeMux()
			mux.Handle("GET /api/accounts/{account}/telegram/webhook", h)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/accounts/"+tc.account+"/telegram/webhook", nil))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if strings.Contains(w.Body.String(), master) || strings.Contains(w.Body.String(), botToken) {
				t.Fatal("master/bot credential exposed")
			}
			if tc.want != 200 {
				if strings.Contains(w.Body.String(), "secret_token") {
					t.Fatal("registration token exposed without authorization")
				}
				return
			}
			var body struct {
				Path  string `json:"path"`
				Token string `json:"secret_token"`
			}
			want, e := registry.TelegramWebhookSecret("a", master)
			if e != nil || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Token != want || body.Path != "/webhooks/telegram/a" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("invalid registration metadata")
			}
		})
	}
}
