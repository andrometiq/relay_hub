package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/onboarding"
)

func TestEnrolmentAdminIsAdminOnlyAndShowsSecretsOnce(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	store, _ := containers.New(db, "enrolment-admin-test-key")
	service, err := onboarding.New(onboarding.Config{DB: db, Store: store, Auth: &auth.Store{DB: db}, PublicOrigin: "https://hub.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES('shop','https://shop.test')`); err != nil {
		t.Fatal(err)
	}
	role, signedIn := "admin", true
	session := func(*http.Request) (auth.User, error) {
		if !signedIn {
			return auth.User{}, errors.New("no session")
		}
		return auth.User{Role: role}, nil
	}
	admin := directoryAdmin{origin: "https://hub.test", session: session}
	mux := http.NewServeMux()
	enrolmentAdmin{backend: service, session: session}.register(mux, func(h http.Handler) http.Handler { return h }, admin.admin)
	call := func(method, path string, body any, csrfToken string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
		r.Header.Set("Origin", "https://hub.test")
		r.Header.Set("X-Relay-CSRF", csrfToken)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	token := auth.CSRF("session")
	bench := map[string]any{"name": "Bench one", "kind": "bench", "origin_pattern": "https://*.example.com"}
	if w := call("POST", "/api/enrolment-tokens", bench, ""); w.Code != 403 {
		t.Fatalf("create without CSRF: %d", w.Code)
	}
	if w := call("POST", "/api/enrolment-tokens", map[string]any{"name": "x", "kind": "bench"}, token); w.Code != 400 {
		t.Fatalf("bench token without pattern: %d", w.Code)
	}
	w := call("POST", "/api/enrolment-tokens", bench, token)
	var created struct {
		Token  onboarding.EnrolmentToken `json:"token"`
		Secret string                    `json:"secret"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || !strings.HasPrefix(created.Secret, "relay_enrol_") || created.Token.ExpiresAt != nil {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	w = call("POST", "/api/enrolment-tokens", map[string]any{"name": "Shop code", "kind": "setup_code"}, token)
	var setup struct {
		Secret, Link, Message string
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &setup) != nil || setup.Link != "https://hub.test/enrol?code="+setup.Secret ||
		!strings.HasSuffix(setup.Message, "paste this setup link: "+setup.Link) {
		t.Fatalf("setup code %d %s", w.Code, w.Body)
	}
	w = call("GET", "/api/enrolment-tokens", nil, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), created.Secret) || strings.Count(w.Body.String(), `"status":"active"`) != 2 {
		t.Fatalf("list %d %s", w.Code, w.Body)
	}
	path := "/api/enrolment-tokens/" + strconv.FormatInt(created.Token.ID, 10) + "/revoke"
	if w = call("POST", path, nil, token); w.Code != 204 {
		t.Fatalf("revoke %d", w.Code)
	}
	if w = call("POST", path, nil, token); w.Code != 404 {
		t.Fatalf("second revoke %d", w.Code)
	}
	if w = call("GET", "/api/sites/shop/signups", nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"signups":[]`) {
		t.Fatalf("signups %d %s", w.Code, w.Body)
	}
	if w = call("GET", "/api/sites/none/signups", nil, ""); w.Code != 404 {
		t.Fatalf("unknown site %d", w.Code)
	}
	role = "member"
	if w = call("GET", "/api/enrolment-tokens", nil, ""); w.Code != 403 {
		t.Fatalf("member list %d", w.Code)
	}
	signedIn = false
	if w = call("GET", "/api/enrolment-tokens", nil, ""); w.Code != 401 {
		t.Fatalf("anonymous list %d", w.Code)
	}
}
