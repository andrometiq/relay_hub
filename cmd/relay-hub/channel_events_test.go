package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
)

func TestChannelCallbackViewsFollowDeliveryState(t *testing.T) {
	for _, channel := range []string{"Telegram", "Email"} {
		t.Run(channel, func(t *testing.T) {
			ctx, db := adminTestDatabase(t)
			var id int64
			if err := db.QueryRow(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,body,payload) VALUES('fixture',$1,'inbound','local-callback','Local recovery fixture','{}') RETURNING id`, channel).Scan(&id); err != nil {
				t.Fatal(err)
			}
			session := func(*http.Request) (auth.User, error) { return auth.User{ID: 1, Role: "admin"}, nil }
			inspector := eventInspector(postgresEventReader{db: db}, session)
			events := channelEventsAdmin(db, session)
			check := func(wantState, wantError string) {
				t.Helper()
				query := "?account=fixture&channel=" + channel
				w := httptest.NewRecorder()
				inspector.ServeHTTP(w, httptest.NewRequest("GET", "/api/event-inspector"+query, nil))
				var page eventPage
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != id || page.Items[0].Callback != wantState {
					t.Fatalf("inspector want %s: %s", wantState, w.Body.String())
				}
				w = httptest.NewRecorder()
				events.ServeHTTP(w, httptest.NewRequest("GET", "/api/channel-events"+query, nil))
				var result struct {
					Events []struct {
						ID    int64  `json:"id"`
						Error string `json:"callback_error"`
					} `json:"events"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Events) != 1 || result.Events[0].ID != id || result.Events[0].Error != wantError {
					t.Fatalf("events want error %q: %s", wantError, w.Body.String())
				}
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			// Retired cursor values must not claim that a new event was acknowledged.
			exec(`INSERT INTO relay_channel_callback_cursor(account,channel,last_id) VALUES('fixture',$1,$2)`, channel, id)
			check("Pending", "")
			exec(`UPDATE relay_channel_callback_delivery SET failures=1,error='Core temporarily unavailable' WHERE event_id=$1`, id)
			check("Retrying", "Core temporarily unavailable")
			cfg := db.Config().ConnConfig
			dsn := cfg.ConnString()
			if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
				u, err := url.Parse(dsn)
				if err != nil {
					t.Fatal("invalid test database URL")
				}
				q := u.Query()
				q.Set("search_path", cfg.RuntimeParams["search_path"])
				u.RawQuery = q.Encode()
				dsn = u.String()
			} else {
				dsn += " search_path=" + cfg.RuntimeParams["search_path"]
			}
			dispatcher := channels.CallbackDispatcher{URL: dsn}
			if err := dispatcher.RecoverCallback(ctx, "fixture", channel, id, 1, "quarantine", "Local operator quarantine evidence"); err != nil {
				t.Fatal(err)
			}
			check("Quarantined", "Core temporarily unavailable")
			// Imported legacy quarantine remains unchanged after replay and ACK.
			exec(`INSERT INTO relay_channel_callback_quarantine(event_id,account,channel,reason) VALUES($1,'fixture',$2,'stale legacy reason')`, id, channel)
			if err := dispatcher.RecoverCallback(ctx, "fixture", channel, id, 1, "replay", "Local operator replay evidence"); err != nil {
				t.Fatal(err)
			}
			check("Retrying", "Core temporarily unavailable")
			// Hold replay until its Retrying state has been inspected, then use the
			// real callback worker and a local Core acknowledgement to complete it.
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var batch struct {
					Events []struct {
						ID int64 `json:"id"`
					} `json:"events"`
				}
				if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch.Events) != 1 || batch.Events[0].ID != id {
					t.Error("unexpected callback batch", err)
					w.WriteHeader(400)
					return
				}
				fmt.Fprint(w, `{"message":{"accepted":1}}`)
			}))
			defer core.Close()
			workerCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			dispatcher.Run(workerCtx, []channels.RuntimeCallback{{Account: "fixture", Site: core.URL}})
			deadline := time.Now().Add(5 * time.Second)
			for {
				var acknowledged bool
				if err := db.QueryRow(ctx, `SELECT acknowledged FROM relay_channel_callback_delivery WHERE event_id=$1`, id).Scan(&acknowledged); err != nil {
					t.Fatal(err)
				}
				if acknowledged {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("callback worker did not acknowledge replay")
				}
				time.Sleep(10 * time.Millisecond)
			}
			check("Forwarded", "")
			var legacyUnresolved bool
			if err := db.QueryRow(ctx, `SELECT resolved_at IS NULL FROM relay_channel_callback_quarantine WHERE event_id=$1`, id).Scan(&legacyUnresolved); err != nil || !legacyUnresolved {
				t.Fatal("legacy fixture unexpectedly changed", err)
			}
		})
	}
}
