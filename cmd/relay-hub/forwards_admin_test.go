package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/forward"
)

func TestForwardsAdminIsAdminOnly(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	if _, err := db.Exec(ctx, `INSERT INTO relay_containers(container_id) VALUES('main')`); err != nil {
		t.Fatal(err)
	}
	role := "admin"
	admin := directoryAdmin{origin: "https://hub.test", session: func(*http.Request) (auth.User, error) { return auth.User{ID: 1, Role: role}, nil }}
	mux := http.NewServeMux()
	forwardsAdmin{store: forward.Store{DB: db}}.register(mux, func(h http.Handler) http.Handler { return h }, admin.admin)
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
	for _, tc := range []struct {
		method, path string
		body         any
		csrf         string
		status       int
	}{
		{"POST", "/api/forwards/whatsapp/main", map[string]string{"url": "https://old.example.test/webhooks/meta"}, "", 403},
		{"POST", "/api/forwards/whatsapp/main", map[string]string{"url": "http://old.example.test/webhooks/meta"}, token, 400},
		{"POST", "/api/forwards/sms/main", map[string]string{"url": "https://old.example.test/a"}, token, 404},
		{"POST", "/api/forwards/telegram/nobot", map[string]string{"url": "https://old.example.test/a"}, token, 404},
		{"POST", "/api/forwards/whatsapp/main", map[string]string{"url": "https://old.example.test/webhooks/meta"}, token, 201},
		{"GET", "/api/forwards/whatsapp/main?before=x", nil, "", 400},
	} {
		if w := call(tc.method, tc.path, tc.body, tc.csrf); w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	var listed struct {
		Targets []forward.Target `json:"targets"`
		Counts  map[string]int64 `json:"counts"`
	}
	w := call("GET", "/api/forwards/whatsapp/main", nil, "")
	if json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Targets) != 1 || listed.Counts["pending"] != 0 {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	role = "user"
	if w := call("GET", "/api/forwards/whatsapp/main", nil, ""); w.Code != 403 {
		t.Fatalf("non-admin list: %d", w.Code)
	}
	if w := call("DELETE", "/api/forwards/whatsapp/main/1", nil, token); w.Code != 403 {
		t.Fatalf("non-admin delete: %d", w.Code)
	}
	role = "admin"
	path := "/api/forwards/whatsapp/main/" + strconv.FormatInt(listed.Targets[0].ID, 10)
	if w := call("DELETE", path, nil, token); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := call("DELETE", path, nil, token); w.Code != 404 {
		t.Fatalf("second delete: %d", w.Code)
	}
}
