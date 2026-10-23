package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		{"invalid channel", "admin", "https://hub.test", "valid", `{"account":"site","channel":"SMS","id":"1"}`, 400},
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

func TestWhatsAppCallbackRecoveryCarriesAuditIdentity(t *testing.T) {
	calls := 0
	h := callbackRetryAdmin(nil, func(*http.Request) (auth.User, error) { return auth.User{ID: 42, Role: "admin"}, nil }, "https://hub.test",
		func(_ context.Context, account, lane string, id, actor int64, action, note string) error {
			calls++
			if account != "site" || lane != "status" || id != 9 || actor != 42 || action != "quarantine" || note != "Verified malformed provider receipt" {
				t.Fatal("lost recovery evidence")
			}
			return nil
		})
	r := httptest.NewRequest("POST", "/api/callback-failures/retry", strings.NewReader(`{"account":"site","channel":"WhatsApp","id":"9","lane":"status","action":"quarantine","note":"Verified malformed provider receipt"}`))
	r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
	r.Header.Set("Origin", "https://hub.test")
	r.Header.Set("X-Relay-CSRF", auth.CSRF("session"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || calls != 1 {
		t.Fatal(w.Code, calls, w.Body.String())
	}
}

func TestCallbackFailuresAuthorization(t *testing.T) {
	for _, tc := range []struct {
		role, query string
		status      int
	}{
		{"guest", "?account=fixture&channel=WhatsApp", 401},
		{"operator", "?account=fixture&channel=WhatsApp", 403},
		{"admin", "?channel=WhatsApp", 400},
		{"admin", "?account=fixture&channel=Other", 400},
		{"admin", "?account=fixture&channel=WhatsApp&before=-1", 400},
		{"admin", "?account=fixture&channel=WhatsApp&before=bad", 400},
	} {
		h := callbackFailuresAdmin(nil, func(*http.Request) (auth.User, error) {
			if tc.role == "guest" {
				return auth.User{}, errors.New("unauthenticated")
			}
			return auth.User{Role: tc.role}, nil
		})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/callback-failures"+tc.query, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: status %d want %d", tc.role, tc.query, w.Code, tc.status)
		}
	}
}

func TestWhatsAppCallbackFailurePaginationKeepsLanesTogether(t *testing.T) {
	for _, tc := range []struct {
		name                string
		newer, older, first int
	}{
		{"pair ends at limit", 48, 1, 50},
		{"pair crosses limit with no more", 49, 0, 51},
		{"pair crosses limit with more", 49, 1, 51},
		{"pair begins on next page", 50, 1, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db := adminTestDatabase(t)
			total := tc.older + tc.newer + 1
			if _, err := db.Exec(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane)
 SELECT 'fixture',n::text,'{}','realtime' FROM generate_series(1,$1::int) n ORDER BY n`, total); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES($1,'status')`, tc.older+1); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `UPDATE relay_whatsapp_callback_delivery SET failures=1,error='Synthetic callback failure'`); err != nil {
				t.Fatal(err)
			}
			h := callbackFailuresAdmin(db, func(*http.Request) (auth.User, error) { return auth.User{Role: "admin"}, nil })
			before := int64(0)
			seen := map[string]bool{}
			for pageIndex := 0; pageIndex < 3; pageIndex++ {
				query := "/api/callback-failures?account=fixture&channel=WhatsApp"
				if before > 0 {
					query += fmt.Sprintf("&before=%d", before)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", query, nil))
				var page struct {
					Items []struct {
						ID   int64  `json:"id"`
						Lane string `json:"lane"`
					} `json:"items"`
					More bool `json:"more"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
					t.Fatal(w.Code, w.Body.String())
				}
				if pageIndex == 0 && len(page.Items) != tc.first {
					t.Fatalf("first page has %d rows, want %d", len(page.Items), tc.first)
				}
				for _, item := range page.Items {
					key := fmt.Sprintf("%d:%s", item.ID, item.Lane)
					if seen[key] || (before > 0 && item.ID >= before) {
						t.Fatal("pagination repeated a failure", key)
					}
					seen[key] = true
				}
				if page.More != (len(seen) < total+1) {
					t.Fatal("incorrect more flag", page.More, len(seen))
				}
				if !page.More {
					break
				}
				if len(page.Items) == 0 {
					t.Fatal("empty page with more failures")
				}
				before = page.Items[len(page.Items)-1].ID
			}
			if len(seen) != total+1 || !seen[fmt.Sprintf("%d:status", tc.older+1)] || !seen[fmt.Sprintf("%d:realtime", tc.older+1)] {
				t.Fatal("pagination hid a failed lane", seen)
			}
		})
	}
}
