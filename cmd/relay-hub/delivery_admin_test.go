package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDeliveryBackend struct {
	reads, changes int
	account        string
	change         deliveryChange
}

func (b *fakeDeliveryBackend) List(_ context.Context, a, c string, n int64) (deliveryPage, error) {
	b.reads++
	b.account = a
	return deliveryPage{Rows: []map[string]string{{"id": "1", "state": "queued"}}}, nil
}
func (b *fakeDeliveryBackend) Change(_ context.Context, in deliveryChange) error {
	b.changes++
	b.change = in
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

func TestDeliveryAdminCarriesChannelEvidence(t *testing.T) {
	b := &fakeDeliveryBackend{}
	h := deliveryAdmin(b, func(*http.Request) (auth.User, error) { return auth.User{ID: 42, Role: "admin"}, nil }, "https://hub.test")
	r := httptest.NewRequest("POST", "/api/delivery-queue", strings.NewReader(`{"account":"fixture","channel":"Telegram and email","id":"7","action":"resolve","outcome":"submitted","note":"Verified against provider history","provider_id":"22","chat_id":-123}`))
	r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
	r.Header.Set("Origin", "https://hub.test")
	r.Header.Set("X-Relay-CSRF", auth.CSRF("session"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || b.changes != 1 || b.change.ProviderID != "22" || b.change.ChatID != -123 || b.change.Note != "Verified against provider history" {
		t.Fatalf("lost evidence: %d %+v", w.Code, b.change)
	}
}

func TestChannelDeliveryAdminPersistsProviderEvidence(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	var id int64
	if err := db.QueryRow(ctx, `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash,state) VALUES('fixture','manual','telegram.message','{"operation":"text","chat_id":-123,"text":"fixture"}','synthetic','uncertain') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h := deliveryAdmin(hubDeliveryBackend{channels: channels.Outbox{DB: db}}, func(*http.Request) (auth.User, error) { return auth.User{Role: "admin"}, nil }, "https://hub.test")
	r := httptest.NewRequest("POST", "/api/delivery-queue", strings.NewReader(fmt.Sprintf(`{"account":"fixture","channel":"Telegram and email","id":"%d","action":"resolve","outcome":"submitted","note":"Verified provider message receipt","provider_id":"22","chat_id":-123}`, id)))
	r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
	r.Header.Set("Origin", "https://hub.test")
	r.Header.Set("X-Relay-CSRF", auth.CSRF("session"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt bool
	if err := db.QueryRow(ctx, `SELECT receipt->>'message_id'='22' AND receipt->>'chat_id'='-123' FROM relay_channel_status_journal WHERE delivery_id=$1 AND state='submitted'`, id).Scan(&receipt); err != nil || !receipt {
		t.Fatal("resolution did not journal provider identifiers", err)
	}
}
