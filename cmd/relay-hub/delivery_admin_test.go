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

type fakeDeliveryBackend struct {
	reads, changes int
	account        string
}

func (b *fakeDeliveryBackend) List(_ context.Context, a, c string, n int64) (deliveryPage, error) {
	b.reads++
	b.account = a
	return deliveryPage{Rows: []map[string]string{{"id": "1", "state": "queued"}}}, nil
}
func (b *fakeDeliveryBackend) Change(_ context.Context, in deliveryChange) error {
	b.changes++
	b.account = in.Account
	return nil
}
func TestDeliveryAdminAuthorization(t *testing.T) {
	b := &fakeDeliveryBackend{}
	for _, role := range []string{"guest", "operator", "admin"} {
		t.Run(role, func(t *testing.T) {
			h := deliveryAdmin(b, func(*http.Request) (auth.User, error) {
				if role == "guest" {
					return auth.User{}, errors.New("no session")
				}
				return auth.User{Role: role}, nil
			}, "https://hub.test")
			r := httptest.NewRequest("GET", "/api/delivery-queue?account=fixture&channel=WhatsApp", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 200
			if role == "guest" {
				want = 401
			}
			if role == "operator" {
				want = 403
			}
			if w.Code != want {
				t.Fatal(w.Code, want)
			}
		})
	}
	if b.reads != 1 || b.account != "fixture" {
		t.Fatal("unauthorized read")
	}
	h := deliveryAdmin(b, func(*http.Request) (auth.User, error) { return auth.User{Role: "admin"}, nil }, "https://hub.test")
	for _, valid := range []bool{false, true} {
		r := httptest.NewRequest("POST", "/api/delivery-queue", strings.NewReader(`{"account":"fixture","channel":"WhatsApp","id":"1","action":"cancel"}`))
		r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "synthetic-session"})
		r.Header.Set("Origin", "https://hub.test")
		if valid {
			r.Header.Set("X-Relay-CSRF", auth.CSRF("synthetic-session"))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 403
		if valid {
			want = 204
		}
		if w.Code != want {
			t.Fatal(w.Code, want)
		}
	}
	if b.changes != 1 {
		t.Fatal("mutation bypassed CSRF")
	}
	for _, path := range []string{"?account=fixture&channel=other", "?account=fixture&channel=WhatsApp&after=-1", "?channel=WhatsApp"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/delivery-queue"+path, nil))
		if w.Code != 400 {
			t.Fatal("invalid filter accepted")
		}
	}
}
