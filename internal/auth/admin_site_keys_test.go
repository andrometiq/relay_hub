package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAdministratorsIssueKeysForAnySite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if e := s.Bootstrap(ctx, "admin", "synthetic-long-password-42"); e != nil {
		t.Fatal(e)
	}
	session, e := s.Login(ctx, "admin", "synthetic-long-password-42", "test")
	if e != nil {
		t.Fatal(e)
	}
	admin, e := s.Session(ctx, session)
	if e != nil {
		t.Fatal(e)
	}
	memberID, e := s.CreateUser(ctx, admin, "member", "synthetic-long-password-43", "member")
	if e != nil {
		t.Fatal(e)
	}
	member := User{ID: memberID, Role: "member", Version: 1}
	if _, e = s.DB.Exec(ctx, `INSERT INTO relay_sites(site_id,name,origin) VALUES('yrp','YRP','https://yrp.example.test'),('other','','https://other.example.test')`); e != nil {
		t.Fatal(e)
	}

	key, e := s.Issue(ctx, admin, "core", "yrp", 0)
	if e != nil {
		t.Fatal("administrator could not issue a key for an ungranted site", e)
	}
	if p, e := s.Authenticate(ctx, key); e != nil || p.Site != "yrp" {
		t.Fatal("administrator key for an ungranted site does not authenticate", e)
	}
	if _, e = s.Issue(ctx, admin, "core", "missing", 0); !errors.Is(e, ErrDenied) {
		t.Fatal("administrator issued a key for a missing site", e)
	}
	if _, e = s.Issue(ctx, member, "core", "yrp", 0); !errors.Is(e, ErrDenied) {
		t.Fatal("member issued a key for an ungranted site", e)
	}
	if e = s.Grant(ctx, admin, memberID, "yrp", true); e != nil {
		t.Fatal(e)
	}
	memberKey, e := s.Issue(ctx, member, "core", "yrp", 0)
	if e != nil {
		t.Fatal("member could not issue a key for a granted site", e)
	}
	if _, e = s.Authenticate(ctx, memberKey); e != nil {
		t.Fatal("member key does not authenticate", e)
	}
	if e = s.Grant(ctx, admin, memberID, "yrp", false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, memberKey); !errors.Is(e, ErrDenied) {
		t.Fatal("member key still works after the grant was removed")
	}

	handler, e := NewHTTP(s, "https://hub.example.test", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	request := func(method, path string, form url.Values) (int, map[string]any) {
		r := httptest.NewRequest(method, "https://hub.example.test"+path, strings.NewReader(form.Encode()))
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Origin", "https://hub.example.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: Cookie, Value: session})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	code, body := request("GET", "/auth/options?kind=grants", nil)
	items, _ := body["items"].([]any)
	if code != 200 || len(items) != 2 {
		t.Fatal("administrator key picker must list every site", code, body)
	}
	labels := map[string]any{}
	for _, item := range items {
		option := item.(map[string]any)
		labels[option["value"].(string)] = option["label"]
	}
	if labels["yrp"] != "YRP (yrp)" || labels["other"] != "other" {
		t.Fatal("site options must show name and ID", labels)
	}
	form := url.Values{"name": {"core"}, "account": {"other"}, "days": {"0"}, "csrf": {CSRF(session)}}
	code, body = request("POST", "/auth/tokens", form)
	if token, _ := body["Token"].(string); code != 200 || !strings.HasPrefix(token, "relay_") {
		t.Fatal("administrator key request failed", code, body["Error"])
	}
	form.Set("account", "missing")
	if code, body = request("POST", "/auth/tokens", form); code != 400 || body["Token"] != nil {
		t.Fatal("key for a missing site accepted", code)
	}
}
