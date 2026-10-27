package main

import (
	"encoding/json"
	"errors"
	"github.com/andrometiq/relay_hub/internal/channels"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestChannelHealthPopulatedScopeAndRedaction(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	for _, channel := range []string{"Telegram", "Email"} {
		var id int64
		if err := db.QueryRow(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,body,payload) VALUES('health-local',$1,'inbound','health-update','Local fixture','{}') RETURNING id`, channel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE relay_channel_callback_delivery SET failures=2,error='private-provider-secret' WHERE event_id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ account, operation, key, state string }{
		{"health-local", "telegram.message", "tg-retry", "queued"},
		{"health-local", "telegram.message", "tg-uncertain", "uncertain"},
		{"health-local", "email.send", "mail-pending", "queued"},
		{"other-local", "telegram.message", "other", "uncertain"},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO relay_channel_outbox(account,operation,request_key,payload_hash,payload,state,attempts,next_attempt,provider_error) VALUES($1,$2,$3,'hash','{}',$4,2,now()+interval '1 hour','{"ErrorCode":429}')`, row.account, row.operation, row.key, row.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO relay_mailbox_cursor(account,mailbox,uid_validity,last_uid,reset_uid_next,last_sync,last_error,failures,next_attempt) VALUES('health-local','local-mailbox',123,7,20,now(),'private-provider-secret',3,now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	h := accountHealth(db, func(*http.Request) (auth.User, error) { return auth.User{Role: "admin"}, nil })
	for _, channel := range []string{"Telegram", "Email"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/accounts/health?account=health-local&channel="+channel, nil))
		var result channels.Health
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("health: %d %s", w.Code, w.Body.String())
		}
		if result.LastUpdate == "" || result.Pending != 1 || result.Failed != 1 || result.DeliveryPending != 1 || result.NextAttempt == "" || strings.Contains(w.Body.String(), "private-provider-secret") {
			t.Fatalf("unexpected health: %s", w.Body.String())
		}
		if channel == "Telegram" && (result.Uncertain != 1 || result.RateLimited != 1 || len(result.Mailboxes) != 0) {
			t.Fatal("cross-channel health")
		}
		if channel == "Email" && (result.Uncertain != 0 || len(result.Mailboxes) != 1 || result.Mailboxes[0].UIDValidity != 123 || !result.Mailboxes[0].Backoff) {
			t.Fatal("missing mailbox evidence")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/accounts/health?account=health-local&channel=SMS", nil))
	if w.Code != 400 {
		t.Fatal("unsupported channel accepted")
	}
}
