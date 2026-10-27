package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSelfPasswordRotationTokenChoice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := "rotation-fixture-password-42"
	next := old + "new"
	if err := s.Bootstrap(ctx, "admin", old); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, "admin", old, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_accounts(account_id,site) VALUES('test-account','https://fixture.invalid') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err = s.Grant(ctx, user, user.ID, "test-account", true); err != nil {
		t.Fatal(err)
	}
	token, err := s.Issue(ctx, user, "integration", "test-account", []string{"messages:send"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, "https://hub.test", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(current, password, revoke string) *httptest.ResponseRecorder {
		body := url.Values{"csrf": {CSRF(session)}, "old_password": {current}, "password": {password}, "revoke_tokens": {revoke}}
		r := httptest.NewRequest("POST", "https://hub.test/auth/password", strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://hub.test")
		r.Header.Set("Accept", "application/json")
		r.AddCookie(&http.Cookie{Name: Cookie, Value: session})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post("incorrect", next, "false"); w.Code == 200 {
		t.Fatal("wrong current password accepted")
	}
	if _, err = s.Authenticate(ctx, token); err != nil {
		t.Fatal("failed rotation revoked token", err)
	}
	if w := post(old, next, "false"); w.Code != 200 {
		t.Fatal("self rotation failed", w.Code, w.Body.String())
	}
	if _, err = s.Session(ctx, session); err == nil {
		t.Fatal("rotation retained browser session")
	}
	if _, err = s.Authenticate(ctx, token); err != nil {
		t.Fatal("rotation disconnected integration", err)
	}
	session, err = s.Login(ctx, "admin", next, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if w := post(next, old, "true"); w.Code != 200 {
		t.Fatal("explicit revocation failed", w.Code)
	}
	if _, err = s.Authenticate(ctx, token); err == nil {
		t.Fatal("explicit token revocation ignored")
	}
}

func TestAdminPasswordResetCannotPreserveTokens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	password := "reset-fixture-password-42"
	if err := s.Bootstrap(ctx, "admin", password); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, "admin", password, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateUser(ctx, admin, "member", password, "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_accounts(account_id,site) VALUES('test-account','https://fixture.invalid') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err = s.Grant(ctx, admin, id, "test-account", true); err != nil {
		t.Fatal(err)
	}
	memberSession, err := s.Login(ctx, "member", password, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.Session(ctx, memberSession)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Issue(ctx, member, "integration", "test-account", []string{"messages:send"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePasswordWithTokenRevocation(ctx, admin, id, "", password+"new", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, token); err == nil {
		t.Fatal("admin reset preserved token")
	}
	if _, err = s.Session(ctx, memberSession); err == nil {
		t.Fatal("admin reset preserved session")
	}
}
