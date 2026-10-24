package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func TestDeliveryAdminChannelHTTPRecovery(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	h := deliveryAdmin(hubDeliveryBackend{channels: channels.Outbox{DB: db}}, func(*http.Request) (auth.User, error) { return auth.User{Role: "admin"}, nil }, "https://hub.test")
	ids := map[string]int64{}
	for _, channel := range []string{"Telegram", "Email"} {
		operation, payload := "telegram.message", `{"operation":"text","chat_id":-123,"text":"local fixture"}`
		if channel == "Email" {
			operation, payload = "email.send", `{"to":["local@example.invalid"],"subject":"local fixture","body":"test"}`
		}
		var id int64
		if err := db.QueryRow(ctx, `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash,state) VALUES('fixture',$1,$2,$3,'synthetic','queued') RETURNING id`, channel, operation, []byte(payload)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[channel] = id
	}
	for _, channel := range []string{"Telegram", "Email", "Telegram and email"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/delivery-queue?"+url.Values{"account": {"fixture"}, "channel": {channel}}.Encode(), nil))
		var page struct {
			Rows []channels.Delivery `json:"deliveries"`
			More bool                `json:"more"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.More {
			t.Fatal(w.Code, w.Body.String())
		}
		want := 1
		if channel == "Telegram and email" {
			want = 2
		}
		if len(page.Rows) != want {
			t.Fatalf("%s: %+v", channel, page)
		}
		if want == 1 && page.Rows[0].ID != ids[channel] {
			t.Fatal("channel filter leaked another queue")
		}
	}
	for _, channel := range []string{"Telegram", "Email"} {
		for _, action := range []string{"cancel", "resolve"} {
			if action == "resolve" {
				if _, err := db.Exec(ctx, `UPDATE relay_channel_outbox SET state='uncertain' WHERE id=$1`, ids[channel]); err != nil {
					t.Fatal(err)
				}
			}
			provider := "22"
			if channel == "Email" {
				provider = "<local@example.invalid>"
			}
			body, err := json.Marshal(deliveryChange{Account: "fixture", Channel: channel, Action: action, ID: strconv.FormatInt(ids[channel], 10), Outcome: "submitted", Note: "Verified local provider acceptance", ProviderID: provider, ChatID: -123})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/delivery-queue", bytes.NewReader(body))
			r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
			r.Header.Set("Origin", "https://hub.test")
			r.Header.Set("X-Relay-CSRF", auth.CSRF("session"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
				t.Fatal(channel, action, w.Code, w.Body.String())
			}
			var state, messageID string
			if err := db.QueryRow(ctx, `SELECT state,coalesce(result->>'message_id','') FROM relay_channel_outbox WHERE id=$1`, ids[channel]).Scan(&state, &messageID); err != nil {
				t.Fatal(err)
			}
			if action == "cancel" && state != "cancelled" || action == "resolve" && (state != "submitted" || messageID != provider) {
				t.Fatal(channel, action, state, messageID)
			}
		}
	}
}

func TestDeliveryAdminAcceptsEachChannelAndRejectsUnauthorizedChanges(t *testing.T) {
	for _, channel := range []string{"WhatsApp", "Telegram", "Email", "Telegram and email"} {
		for _, role := range []string{"operator", "admin"} {
			b := &fakeDeliveryBackend{}
			h := deliveryAdmin(b, func(*http.Request) (auth.User, error) { return auth.User{Role: role}, nil }, "https://hub.test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/delivery-queue?"+url.Values{"account": {"fixture"}, "channel": {channel}}.Encode(), nil))
			want := 200
			if role != "admin" {
				want = 403
			}
			if w.Code != want {
				t.Fatal(channel, role, w.Code)
			}
			for _, action := range []string{"cancel", "resolve"} {
				body, _ := json.Marshal(deliveryChange{Account: "fixture", Channel: channel, Action: action, ID: "1"})
				r := httptest.NewRequest("POST", "/api/delivery-queue", bytes.NewReader(body))
				r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
				r.Header.Set("Origin", "https://hub.test")
				r.Header.Set("X-Relay-CSRF", auth.CSRF("session"))
				w = httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want = 204
				if role != "admin" {
					want = 403
				}
				if w.Code != want || role == "operator" && b.changes != 0 {
					t.Fatal(channel, role, action, w.Code)
				}
				if role == "admin" && (b.change.Channel != channel || b.change.Action != action) {
					t.Fatal("lost selection")
				}
			}
		}
	}
}
