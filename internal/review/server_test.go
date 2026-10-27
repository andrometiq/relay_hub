package review

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL integration database")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := "relay_review_test_" + random()[:16]
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	sql, e := os.ReadFile("../database/migrations/0008_review_sessions.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, string(sql)); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	os.WriteFile(dir+"/index.html", []byte("preview"), 0600)
	salt := strings.Repeat("a", 32)
	sb, _ := hex.DecodeString(salt)
	hash, _ := pbkdf2.Key(sha256.New, "test-password", sb, 600000, 32)
	s, e := New(Config{Origin: "https://review.test", Username: "review", Salt: salt, PasswordHash: hex.EncodeToString(hash), HubDir: dir, CoreDir: dir}, db)
	if e != nil {
		t.Fatal(e)
	}
	s.limit = rate.NewLimiter(rate.Inf, 100)
	req := func(method, p string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://review.test"+p, strings.NewReader(form.Encode()))
		r.Header.Set("Origin", "https://review.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	login := func() *http.Cookie {
		w := req("GET", "/login", nil)
		c := w.Result().Cookies()[0]
		w = req("POST", "/login", url.Values{"username": {"review"}, "password": {"test-password"}, "csrf": {csrf(c.Value)}}, c)
		if w.Code != 303 {
			t.Fatalf("login %d %s", w.Code, w.Body.String())
		}
		for _, v := range w.Result().Cookies() {
			if v.Name == cookieName {
				if !v.Secure || !v.HttpOnly || v.Path != "/" {
					t.Fatal("unsafe cookie")
				}
				return v
			}
		}
		t.Fatal("no session cookie")
		return nil
	}
	if w := req("GET", "/core/", nil); w.Code != 303 {
		t.Fatal("unauthenticated access")
	}
	a, b := login(), login()
	if a.Value == b.Value {
		t.Fatal("token reused")
	}
	if w := req("GET", "/core/", nil, a); w.Code != 200 || w.Body.String() != "preview" {
		t.Fatal("static preview")
	}
	if w := req("GET", "/mcp", nil, a); w.Code != 404 {
		t.Fatal("API exposed")
	}
	if w := req("POST", "/logout", nil, a); w.Code != 403 {
		t.Fatal("CSRF not enforced")
	}
	req("POST", "/sessions", url.Values{"csrf": {csrf(a.Value)}, "session": {"others"}}, a)
	if w := req("GET", "/", nil, b); w.Code != 303 {
		t.Fatal("revoked session accepted")
	}
	if w := req("GET", "/", nil, a); w.Code != 200 {
		t.Fatal("current session revoked")
	}
	// Restart preserves sessions, while absolute and idle expiry are server-enforced.
	s2, e := New(s.cfg, db)
	if e != nil {
		t.Fatal(e)
	}
	s = s2
	s.limit = rate.NewLimiter(rate.Inf, 100)
	if w := req("GET", "/", nil, a); w.Code != 200 {
		t.Fatal("restart lost session")
	}
	db.Exec(ctx, "UPDATE relay_review_sessions SET last_seen=now()-interval '31 minutes'")
	if w := req("GET", "/", nil, a); w.Code != 303 {
		t.Fatal("idle session accepted")
	}
	a = login()
	db.Exec(ctx, "UPDATE relay_review_sessions SET expires_at=now()-interval '1 second'")
	if w := req("GET", "/", nil, a); w.Code != 303 {
		t.Fatal("expired session accepted")
	}
	a = login()
	if w := req("POST", "/logout", url.Values{"csrf": {csrf(a.Value)}}, a); w.Code != 303 {
		t.Fatal("logout failed")
	}
	if w := req("GET", "/", nil, a); w.Code != 303 {
		t.Fatal("logout token accepted")
	}
	c := req("GET", "/login", nil).Result().Cookies()[0]
	if w := req("POST", "/login", url.Values{"username": {"review"}, "password": {"wrong"}, "csrf": {csrf(c.Value)}}, c); w.Code != 401 {
		t.Fatal("bad password accepted")
	}
	s.limit = rate.NewLimiter(rate.Every(time.Hour), 0)
	if w := req("POST", "/login", url.Values{"csrf": {csrf(c.Value)}}, c); w.Code != 429 {
		t.Fatal("rate limit failed")
	}
}

func TestReviewLoginChallengeSurvivesSecondTab(t *testing.T) {
	s := &Server{}
	first := httptest.NewRecorder()
	s.login(first, httptest.NewRequest("GET", "https://review.test/login", nil))
	challenge := first.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "https://review.test/login", nil)
	r.AddCookie(challenge)
	second := httptest.NewRecorder()
	s.login(second, r)
	if second.Result().Cookies()[0].Value != challenge.Value {
		t.Fatal("second tab invalidated first login")
	}
}
