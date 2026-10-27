package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMetadataDatabaseRefresh(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)
	a := testAccount()
	a.SyncMetadata = true
	s, err := New([]Account{a})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var fail, revoke atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":{"message":"provider-secret"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/111") {
			fmt.Fprint(w, `{"id":"111","verified_name":"Fixture","access_token":"provider-secret"}`)
			if revoke.Load() {
				_ = s.ReplaceAccounts(nil)
			}
			return
		}
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `{"data":[{"id":"one","name":"first"}],"paging":{"cursors":{"after":"second"},"next":"https://example.test/?access_token=provider-secret"}}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"two","name":"second"}]}`)
		}
	}))
	defer server.Close()
	s.base = server.URL
	m := &MetadataSync{DB: pool, Service: s}
	disabled := a
	disabled.SyncMetadata = false
	if err = m.Sync(ctx, disabled); err != nil || calls.Load() != 0 {
		t.Fatal("opt out called provider", err)
	}
	if err = m.Sync(ctx, a); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("pagination requests: %d", calls.Load())
	}
	value, err := m.Read(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if strings.Contains(string(raw), "provider-secret") || value["status"] != "ready" || len(value["templates"].([]any)) != 2 {
		t.Fatalf("invalid cache: %s", raw)
	}
	if err = m.Sync(ctx, a); err != nil || calls.Load() != 3 {
		t.Fatal("hourly gate failed", err)
	}
	foreign, err := m.Read(ctx, "other")
	if err != nil || foreign["status"] != "not_synced" {
		t.Fatal("account isolation", err)
	}
	due := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE relay_whatsapp_metadata SET next_attempt=now()-interval '1 second' WHERE account=$1`, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	due()
	fail.Store(true)
	if m.Sync(ctx, a) == nil {
		t.Fatal("provider failure accepted")
	}
	value, err = m.Read(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(value)
	if value["status"] != "failed" || value["last_success"] == nil || len(value["templates"].([]any)) != 2 || strings.Contains(string(raw), "provider-secret") {
		t.Fatal("failure lost cache or exposed provider details")
	}
	changed := a
	changed.AccessToken = "replacement-fixture-token"
	if err = s.ReplaceAccounts([]Account{changed}); err != nil {
		t.Fatal(err)
	}
	hidden, err := m.Read(ctx, a.ID)
	if err != nil || hidden["status"] != "not_synced" {
		t.Fatal("old account metadata exposed after credential change")
	}
	if err = s.ReplaceAccounts([]Account{a}); err != nil {
		t.Fatal(err)
	}
	due()
	fail.Store(false)
	revoke.Store(true)
	before := calls.Load()
	if m.Sync(ctx, a) == nil {
		t.Fatal("account revocation accepted")
	}
	if calls.Load() != before+1 {
		t.Fatal("provider requests continued after revocation")
	}
}
