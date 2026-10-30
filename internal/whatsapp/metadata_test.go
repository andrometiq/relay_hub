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

func TestAccountChangeRefreshesMetadata(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)
	a := testAccount()
	a.SyncMetadata = true
	other := testAccount()
	other.ID, other.PhoneID, other.WABAID, other.CoreToken = "site-b", "333", "444", strings.Repeat("b", 64)
	s, err := New([]Account{a, other})
	if err != nil {
		t.Fatal(err)
	}
	m := &MetadataSync{DB: pool, Service: s}
	for _, id := range []string{a.ID, other.ID} {
		if _, err = pool.Exec(ctx, `INSERT INTO relay_whatsapp_metadata(account,source_hash,next_attempt) VALUES($1,'x',now()+interval '1 hour')`, id); err != nil {
			t.Fatal(err)
		}
	}
	quality := webhookEntry{ID: a.WABAID, Changes: []webhookChange{{Field: "phone_number_quality_update"}}}
	if !accountChange(quality) || accountChange(webhookEntry{ID: a.WABAID, Changes: []webhookChange{{Field: "messages"}}}) {
		t.Fatal("account webhook classification")
	}
	if err = m.RefreshAccounts(ctx, a.WABAID); err != nil {
		t.Fatal(err)
	}
	var due, untouched bool
	if err = pool.QueryRow(ctx, `SELECT bool_or(next_attempt<=now()) FILTER (WHERE account=$1), bool_or(next_attempt<=now()) FILTER (WHERE account=$2) FROM relay_whatsapp_metadata`, a.ID, other.ID).Scan(&due, &untouched); err != nil {
		t.Fatal(err)
	}
	if !due || untouched {
		t.Fatal("quality webhook must make only the WABA's numbers due")
	}
}

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
	var fail, revoke, wabaFail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":{"message":"provider-secret"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/111") {
			if r.URL.Query().Get("fields") != phoneProfileFields {
				t.Error("profile fields not requested")
			}
			fmt.Fprint(w, `{"id":"111","verified_name":"Fixture","quality_rating":"YELLOW","whatsapp_business_manager_messaging_limit":"TIER_2K","status":"FLAGGED","name_status":"APPROVED","throughput":{"level":"STANDARD"},"access_token":"provider-secret"}`)
			if revoke.Load() {
				_ = s.ReplaceAccounts(nil)
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/222") && wabaFail.Load() {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"message":"missing permission"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/222") {
			if r.URL.Query().Get("fields") != businessProfileFields {
				t.Error("business fields not requested")
			}
			fmt.Fprint(w, `{"id":"222","name":"Fixture WABA","account_review_status":"APPROVED","business_verification_status":"verified","whatsapp_business_manager_messaging_limit":"TIER_2K"}`)
			return
		}
		if r.URL.Query().Get("fields") != templateListFields {
			t.Error("template quality not requested")
		}
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `{"data":[{"id":"one","name":"first"}],"paging":{"cursors":{"after":"second"},"next":"https://example.test/?access_token=provider-secret"}}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"two","name":"second","quality_score":{"score":"RED","date":1}}]}`)
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
	if calls.Load() != 4 {
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
	profile := value["profile"].(map[string]any)
	business := profile["business"].(map[string]any)
	if profile["quality_rating"] != "YELLOW" || profile["status"] != "FLAGGED" || profile["throughput_level"] != "STANDARD" || profile["whatsapp_business_manager_messaging_limit"] != "TIER_2K" || profile["throughput"] != nil {
		t.Fatalf("number health not cached: %s", raw)
	}
	if business["account_review_status"] != "APPROVED" || business["business_verification_status"] != "verified" || business["name"] != "Fixture WABA" {
		t.Fatalf("account health not cached: %s", raw)
	}
	if quality := value["templates"].([]any)[1].(map[string]any)["quality_score"].(map[string]any)["score"]; quality != "RED" {
		t.Fatalf("template quality not cached: %s", raw)
	}
	if err = m.Sync(ctx, a); err != nil || calls.Load() != 4 {
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
	wabaFail.Store(true)
	if err = m.Sync(ctx, a); err != nil {
		t.Fatal("unreadable WABA must not fail the number sync", err)
	}
	value, err = m.Read(ctx, a.ID)
	if err != nil || value["status"] != "ready" || value["profile"].(map[string]any)["quality_rating"] != "YELLOW" || len(value["profile"].(map[string]any)["business"].(map[string]any)) != 0 {
		t.Fatalf("number health lost when the WABA is unreadable: %v", value)
	}
	wabaFail.Store(false)
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
