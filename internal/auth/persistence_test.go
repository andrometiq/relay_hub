package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPersistentSessionAndRevocableNoExpiryToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "admin", "persistent-test-password-42"); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, "admin", "persistent-test-password-42", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE relay_auth_sessions SET last_seen=now()-interval '40 days',created_at=now()-interval '40 days' WHERE token_hash=$1`, Hash(session)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, session); err != nil {
		t.Fatal("idle session unexpectedly expired", err)
	}
	if err = s.Grant(ctx, user, user.ID, "test-account", true); err != nil {
		t.Fatal(err)
	}
	token, err := s.Issue(ctx, user, "persistent integration", "test-account", []string{"messages:send"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	var never bool
	if err = s.DB.QueryRow(ctx, `SELECT id,expires_at='infinity'::timestamptz FROM relay_api_tokens WHERE token_hash=$1`, Hash(token)).Scan(&id, &never); err != nil || !never {
		t.Fatal("not non-expiring", err)
	}
	if _, err = s.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err = s.Revoke(ctx, user, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, token); err == nil {
		t.Fatal("revoked token accepted")
	}
	// Infrastructure failures must not destroy the browser's valid login cookie.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Session(cancelled, session); err == nil || errors.Is(err, ErrDenied) {
		t.Fatal("outage mislabeled as invalid credentials", err)
	}
	h, err := NewHTTP(s, "https://hub.example.test", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://hub.example.test/auth/tokens", nil).WithContext(cancelled)
	r.AddCookie(&http.Cookie{Name: Cookie, Value: session})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || len(w.Result().Cookies()) != 0 {
		t.Fatal("temporary outage logged user out", w.Code, w.Header())
	}
	if _, err = s.DB.Exec(ctx, "DELETE FROM relay_auth_sessions WHERE token_hash=$1", Hash(session)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, session); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked session accepted", err)
	}
}
