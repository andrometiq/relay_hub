package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PostgreSQL integration required")
	}
	ctx := context.Background()
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "relay_auth_test_" + Secret()[:16]
	if _, e = db.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pool.Close(); db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); db.Close() })
	entries, readErr := os.ReadDir("../database/migrations")
	if readErr != nil {
		t.Fatal(readErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, migration := range names {
		sql, readErr := os.ReadFile("../database/migrations/" + migration)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, e = pool.Exec(ctx, string(sql)); e != nil {
			t.Fatalf("migration %s: %v", migration, e)
		}
	}
	return &Store{DB: pool}
}
func TestIdentitiesTokensAndSessions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	pass := "a-long-test-password-42"
	if e := s.Bootstrap(ctx, "admin", pass); e != nil {
		t.Fatal(e)
	}
	if e := s.Bootstrap(ctx, "other", pass); e == nil {
		t.Fatal("bootstrap repeated")
	}
	at, e := s.Login(ctx, "admin", pass, "test")
	if e != nil {
		t.Fatal(e)
	}
	admin, e := s.Session(ctx, at)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetEnabled(ctx, admin, admin.ID, false); e == nil {
		t.Fatal("last admin disabled")
	}
	id, e := s.CreateUser(ctx, admin, "member", pass, "member")
	if e != nil {
		t.Fatal(e)
	}
	mt, e := s.Login(ctx, "member", pass, "test")
	if e != nil {
		t.Fatal(e)
	}
	member, _ := s.Session(ctx, mt)
	if _, e = s.CreateUser(ctx, member, "escalation", pass, "admin"); e == nil {
		t.Fatal("member created administrator")
	}
	if _, e = s.Issue(ctx, member, "unauthorized", "site-a", 30); e == nil {
		t.Fatal("ungranted token")
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES('site-a','https://fixture.invalid') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if e = s.Grant(ctx, admin, id, "site-a", true); e != nil {
		t.Fatal(e)
	}
	token, e := s.Issue(ctx, member, "core", "site-a", 30)
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Authenticate(ctx, token)
	if e != nil || p.Site != "site-a" {
		t.Fatal("token not bound to its site", e)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES('site-b','https://b.invalid') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, e = s.Issue(ctx, member, "foreign", "site-b", 30); e == nil {
		t.Fatal("token issued for a site the user was not granted")
	}

	for _, enabled := range []bool{false, true} {
		if _, err := s.DB.Exec(ctx, "UPDATE relay_sites SET enabled=$1 WHERE site_id='site-a'", enabled); err != nil {
			t.Fatal(err)
		}
		restarted := &Store{DB: s.DB}
		if _, err := restarted.Authenticate(ctx, token); (err == nil) != enabled {
			t.Fatal("account enablement ignored", enabled, err)
		}
	}
	if _, e = s.Authenticate(ctx, at); e == nil {
		t.Fatal("session accepted as API token")
	}
	if _, e = s.Session(ctx, token); e == nil {
		t.Fatal("API token accepted as browser session")
	}
	var stored string
	s.DB.QueryRow(ctx, "SELECT token_hash FROM relay_api_tokens WHERE user_id=$1", id).Scan(&stored)
	if stored == token || stored != Hash(token) {
		t.Fatal("plaintext token stored")
	}
	if e = s.Grant(ctx, admin, id, "site-a", false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, token); e == nil {
		t.Fatal("removed grant remains valid")
	}
	s.Grant(ctx, admin, id, "site-a", true)
	if _, e = s.Authenticate(ctx, token); e == nil {
		t.Fatal("regrant revived token")
	}
	token, e = s.Issue(ctx, member, "core2", "site-a", 1)
	if e != nil {
		t.Fatal(e)
	}
	s.DB.Exec(ctx, "UPDATE relay_api_tokens SET expires_at=now()-interval '1 second' WHERE token_hash=$1", Hash(token))
	if _, e = s.Authenticate(ctx, token); e == nil {
		t.Fatal("expired token accepted")
	}
	token, _ = s.Issue(ctx, member, "core3", "site-a", 30)
	if e = s.ChangePassword(ctx, member, id, "wrong", pass+"new"); e == nil {
		t.Fatal("password changed without old password")
	}
	if e = s.ChangePassword(ctx, member, id, pass, pass+"new"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, token); e == nil {
		t.Fatal("password change retained token")
	}
	if _, e = s.Session(ctx, mt); e == nil {
		t.Fatal("password change retained session")
	}
	if _, e = s.Login(ctx, "member", pass, "test"); e == nil {
		t.Fatal("old password accepted")
	}
	mt, e = s.Login(ctx, "member", pass+"new", "test")
	if e != nil {
		t.Fatal(e)
	}
	member, _ = s.Session(ctx, mt)
	token, _ = s.Issue(ctx, member, "core4", "site-a", 30)
	if e = s.SetEnabled(ctx, admin, id, false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, token); e == nil {
		t.Fatal("disabled user token")
	}
	if _, e = s.Session(ctx, mt); e == nil {
		t.Fatal("disabled user session")
	}
	if _, e = s.Login(ctx, "member", pass+"new", "test"); e == nil {
		t.Fatal("disabled user login")
	}
	s.DB.Exec(ctx, "UPDATE relay_auth_sessions SET last_seen=now()-interval '31 minutes' WHERE token_hash=$1", Hash(at))
	if _, e = s.Session(ctx, at); e != nil {
		t.Fatal("persistent session rejected after idle", e)
	}
	at, _ = s.Login(ctx, "admin", pass, "test")
	s.DB.Exec(ctx, "UPDATE relay_auth_sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1", Hash(at))
	if _, e = s.Session(ctx, at); e == nil {
		t.Fatal("absolute timeout")
	}
}
func TestHTTPBoundaries(t *testing.T) {
	s := testStore(t)
	s.Bootstrap(context.Background(), "admin", "a-long-test-password-42")
	h, e := NewHTTP(s, "https://hub.test", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest("GET", "https://hub.test/auth/login", nil))
	c := get.Result().Cookies()[0]
	form := url.Values{"csrf": {CSRF(c.Value)}, "username": {"admin"}, "password": {"a-long-test-password-42"}}
	request := func(origin string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://hub.test/auth/login", strings.NewReader(form.Encode()))
		r.AddCookie(c)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("https://evil.test", form); w.Code != 403 {
		t.Fatal("origin bypass")
	}
	bad := url.Values{"csrf": {"bad"}}
	if w := request("https://hub.test", bad); w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	w := request("https://hub.test", form)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	session := w.Result().Cookies()[0]
	if !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie flags")
	}
	r := httptest.NewRequest("GET", "https://hub.test/auth/users", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "New user") {
		t.Fatal("user page", w.Body.String())
	}
	for _, page := range []string{"tokens", "sessions", "password"} {
		r := httptest.NewRequest("GET", "https://hub.test/auth/"+page, nil)
		r.AddCookie(session)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "</html>") {
			t.Fatal("page rendering", page, w.Body.String())
		}
	}
}

func TestLoginChallengeSurvivesSecondTab(t *testing.T) {
	h, e := NewHTTP(nil, "https://hub.test", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", "https://hub.test/auth/login", nil))
	challenge := first.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "https://hub.test/auth/login", nil)
	r.AddCookie(challenge)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, r)
	if second.Result().Cookies()[0].Value != challenge.Value {
		t.Fatal("second tab invalidated first login")
	}
	if !strings.Contains(first.Body.String(), CSRF(challenge.Value)) || !strings.Contains(second.Body.String(), CSRF(challenge.Value)) {
		t.Fatal("challenge mismatch")
	}
}

func TestJSONPaginationAndFuzzySearch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.Bootstrap(ctx, "admin", "a-long-test-password-42")
	token, e := s.Login(ctx, "admin", "a-long-test-password-42", "test")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(ctx, `INSERT INTO relay_users(username,password_salt,password_hash,role) SELECT 'sample-' || lpad(i::text,4,'0'),'disabled-test-hash','disabled-test-hash','member' FROM generate_series(1,123) i`)
	if e != nil {
		t.Fatal(e)
	}
	h, _ := NewHTTP(s, "https://hub.test", "", []string{"private-account"})
	request := func(query string) map[string]any {
		r := httptest.NewRequest("GET", "https://hub.test/auth/users?"+query, nil)
		r.Header.Set("Accept", "application/json")
		r.AddCookie(&http.Cookie{Name: Cookie, Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result map[string]any
		if json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Body.String())
		}
		return result
	}
	first := request("")
	rows := first["Users"].([]any)
	if len(rows) != 50 || first["More"] != true {
		t.Fatal("unbounded list")
	}
	cursor := int64(rows[49].(map[string]any)["id"].(float64))
	second := request(fmt.Sprintf("cursor=%d", cursor))
	next := second["Users"].([]any)
	if len(next) != 50 || next[0].(map[string]any)["id"].(float64) <= float64(cursor) {
		t.Fatal("cursor repeated")
	}
	previous := request(fmt.Sprintf("cursor=%d&direction=before", int64(next[0].(map[string]any)["id"].(float64))))
	if len(previous["Users"].([]any)) != 50 {
		t.Fatal("reverse pagination")
	}
	fuzzy := request("q=smpl-0123")
	if len(fuzzy["Users"].([]any)) != 1 {
		t.Fatal("fuzzy search missing match", fuzzy)
	}
}
