package main

import (
	"context"
	"errors"
	"github.com/andrometiq/relay_hub/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallbackRetryAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, role, origin, csrf, body string
		want                           int
	}{
		{"operator", "operator", "https://hub.test", "valid", `{"account":"site","channel":"Telegram","id":"1"}`, 403},
		{"origin", "admin", "https://other.test", "valid", `{"account":"site","channel":"Telegram","id":"1"}`, 403},
		{"csrf", "admin", "https://hub.test", "bad", `{"account":"site","channel":"Telegram","id":"1"}`, 403},
		{"unknown field", "admin", "https://hub.test", "valid", `{"account":"site","channel":"Telegram","id":"1","url":"bad"}`, 400},
		{"invalid channel", "admin", "https://hub.test", "valid", `{"account":"site","channel":"WhatsApp","id":"1"}`, 400},
		{"valid", "admin", "https://hub.test", "valid", `{"account":"site","channel":"Telegram","id":"1"}`, 204},
		{"retry failure", "admin", "https://hub.test", "valid", `{"account":"site","channel":"Telegram","id":"2"}`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := callbackRetryAdmin(func(_ context.Context, a, c string, id int64) error {
				calls++
				if a != "site" || c != "Telegram" {
					t.Fatal("wrong scope")
				}
				if id == 2 {
					return errors.New("oversized")
				}
				return nil
			}, func(*http.Request) (auth.User, error) { return auth.User{Role: tc.role}, nil }, "https://hub.test")
			r := httptest.NewRequest("POST", "/api/callback-failures/retry", strings.NewReader(tc.body))
			r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
			r.Header.Set("Origin", tc.origin)
			csrf := tc.csrf
			if csrf == "valid" {
				csrf = auth.CSRF("session")
			}
			r.Header.Set("X-Relay-CSRF", csrf)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d want %d", w.Code, tc.want)
			}
			if tc.want < 400 || tc.want == 409 {
				if calls != 1 {
					t.Fatal("retry not called")
				}
			} else if calls != 0 {
				t.Fatal("unauthorized retry")
			}
		})
	}
}
