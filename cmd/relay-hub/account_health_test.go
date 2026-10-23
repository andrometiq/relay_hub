package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
)

func TestAccountHealthRejectsUnauthorizedAndInvalidScope(t *testing.T) {
	for _, tc := range []struct {
		name, role, account string
		denied              bool
		status              int
	}{
		{"signed out", "", "local-only", true, 401},
		{"member", "member", "local-only", false, 403},
		{"missing account", "admin", "", false, 400},
		{"invalid account", "admin", "../other", false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := accountHealth(nil, func(*http.Request) (auth.User, error) {
				if tc.denied {
					return auth.User{}, errors.New("no session")
				}
				return auth.User{Role: tc.role}, nil
			})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/accounts/health?account="+tc.account, nil))
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
		})
	}
}
