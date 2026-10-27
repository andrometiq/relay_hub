package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAccountGrantsSeeNewNonWhatsAppAccounts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if e := s.Bootstrap(ctx, "admin", "synthetic-long-password-42"); e != nil {
		t.Fatal(e)
	}
	token, e := s.Login(ctx, "admin", "synthetic-long-password-42", "test")
	if e != nil {
		t.Fatal(e)
	}
	admin, e := s.Session(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	handler, e := NewHTTP(s, "https://hub.example.test", "", nil)
	if e != nil {
		t.Fatal(e)
	}

	// Insert after handler construction: the old startup snapshot missed these.
	for _, id := range []string{"telegram-only", "email-only"} {
		if _, e = s.DB.Exec(ctx, `INSERT INTO relay_sites(site_id,origin)VALUES($1,'https://core.example.test')`, id); e != nil {
			t.Fatal(e)
		}
	}
	request := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://hub.example.test"+path, strings.NewReader(form.Encode()))
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Origin", "https://hub.example.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: Cookie, Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/auth/users", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]any
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body["Accounts"].([]any)) != 0 {
		t.Fatal("page must not preload accounts")
	}
	w = request("GET", "/auth/options?kind=accounts&query=telegram", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body["items"].([]any)) != 1 {
		t.Fatal("bounded account search failed", w.Code, w.Body.String())
	}
	for _, account := range []string{"telegram-only", "email-only"} {
		form := url.Values{"action": {"grant"}, "id": {"1"}, "account": {account}, "allow": {"true"}, "csrf": {CSRF(token)}}
		w = request("POST", "/auth/users", form)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if _, e = s.Issue(ctx, admin, "test", account, []string{"messages:send"}, 1); e != nil {
			t.Fatal("new account grant not usable", e)
		}
	}
	w = request("POST", "/auth/users", url.Values{"action": {"grant"}, "id": {"1"}, "account": {"unknown"}, "allow": {"true"}, "csrf": {CSRF(token)}})
	if w.Code != 400 {
		t.Fatal("unknown account grant accepted", w.Code)
	}
}
