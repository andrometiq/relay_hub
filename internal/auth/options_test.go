package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestRemoteOptionsBoundedAndScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "admin", "synthetic-password-long-42"); err != nil {
		t.Fatal(err)
	}
	token, err := s.Login(ctx, "admin", "synthetic-password-long-42", "test")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	memberID, err := s.CreateUser(ctx, admin, "member", "synthetic-password-long-43", "member")
	if err != nil {
		t.Fatal(err)
	}
	count := 2000
	if os.Getenv("RELAY_TEST_OPTIONS_SCALE") == "2000000" {
		count = 2000000
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO relay_accounts(account_id,site) SELECT 'record-' || lpad(n::text,7,'0'),'https://fixture.invalid' FROM generate_series(1,$1) n`, count); err != nil {
		t.Fatal(err)
	}
	member := User{ID: memberID, Role: "member"}
	h, err := NewHTTP(s, "https://hub.example.test", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(user User, path string) (int, map[string]any) {
		r := httptest.NewRequest("GET", "https://hub.example.test/auth/options?"+path, nil)
		w := httptest.NewRecorder()
		h.searchOptions(w, r, user)
		result := map[string]any{}
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, result
	}
	started := time.Now()
	code, result := request(admin, "kind=accounts")
	if code != 200 || len(result["items"].([]any)) != 30 || result["more"] != true {
		t.Fatal("unbounded search", code, result)
	}
	code, result = request(admin, "kind=accounts&query=record-0002000")
	if code != 200 || len(result["items"].([]any)) != 1 {
		t.Fatal("search did not reach beyond first page")
	}
	if code, _ = request(member, "kind=accounts"); code != http.StatusForbidden {
		t.Fatal("member read full account directory")
	}
	if err = s.Grant(ctx, admin, member.ID, "record-0002000", true); err != nil {
		t.Fatal(err)
	}
	code, result = request(member, "kind=grants")
	if code != 200 || len(result["items"].([]any)) != 1 {
		t.Fatal("grant boundary")
	}
	if err = s.Grant(ctx, admin, member.ID, "record-0002000", false); err != nil {
		t.Fatal(err)
	}
	_, result = request(member, "kind=grants")
	if len(result["items"].([]any)) != 0 {
		t.Fatal("revoked grant leaked")
	}
	_, result = request(admin, "kind=accounts&query=%25")
	if len(result["items"].([]any)) != 0 {
		t.Fatal("wildcard expanded")
	}
	t.Logf("option checks with %d account rows completed in %s", count, time.Since(started))
	if code, _ = request(admin, "kind=unknown"); code != 400 {
		t.Fatal("unknown kind")
	}
}
