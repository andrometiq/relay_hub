package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/requestlog"
)

func TestRequestLogStoresAndPagesRequests(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	writer := requestlog.NewWriter(db)
	app := http.NewServeMux()
	app.HandleFunc("POST /webhooks/telegram/{account}", func(w http.ResponseWriter, r *http.Request) {
		requestlog.Tag(r.Context(), "site-a", r.PathValue("account"))
		w.WriteHeader(204)
	})
	app.HandleFunc("GET /api/broken", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) })
	logged := requestlog.Middleware(writer, app)
	send := func(method, target string) {
		r := httptest.NewRequest(method, target, strings.NewReader("{}"))
		r.RemoteAddr = "192.0.2.7:4000"
		r.Header.Set("User-Agent", "agent\x00\xff")
		logged.ServeHTTP(httptest.NewRecorder(), r)
	}
	send("POST", "/webhooks/telegram/bot1?secret=x")
	send("GET", "/api/broken")
	send("GET", "/unknown/%00/%ff/"+strings.Repeat("p", 600))
	if err := writer.Close(ctx); err != nil {
		t.Fatal(err)
	}

	role := "admin"
	admin := directoryAdmin{origin: "https://hub.test", session: func(*http.Request) (auth.User, error) { return auth.User{ID: 1, Role: role}, nil }}
	mux := http.NewServeMux()
	requestLogAdmin{db: db}.register(mux, func(h http.Handler) http.Handler { return h }, admin.admin)
	list := func(query string) (int, []requestLogRow, bool) {
		r := httptest.NewRequest("GET", "/api/request-log"+query, nil)
		r.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "session"})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var page struct {
			Entries []requestLogRow `json:"entries"`
			More    bool            `json:"more"`
		}
		json.Unmarshal(w.Body.Bytes(), &page)
		return w.Code, page.Entries, page.More
	}

	code, rows, more := list("")
	if code != 200 || len(rows) != 3 || more {
		t.Fatalf("code %d rows %d more %v", code, len(rows), more)
	}
	webhook := rows[2]
	if webhook.Path != "/webhooks/telegram/bot1" || *webhook.Route != "POST /webhooks/telegram/{account}" || webhook.Status != 204 ||
		*webhook.SiteID != "site-a" || *webhook.Account != "bot1" || *webhook.RemoteIP != "192.0.2.7" || webhook.UserAgent != "agent" ||
		webhook.RequestBytes != 2 || webhook.RequestID == "" || webhook.Error != nil {
		t.Fatalf("webhook row %+v", webhook)
	}
	if rows[0].Route != nil || len(rows[0].Path) != 500 || rows[0].Status != 404 {
		t.Fatalf("unknown row %+v", rows[0])
	}
	if code, rows, _ = list("?status=503"); code != 200 || len(rows) != 1 || *rows[0].Error != "unavailable" {
		t.Fatalf("status filter %d %+v", code, rows)
	}
	if _, rows, _ = list("?site=site-a"); len(rows) != 1 || rows[0].ID != webhook.ID {
		t.Fatalf("site filter %+v", rows)
	}
	if _, rows, more = list("?limit=1"); len(rows) != 1 || !more {
		t.Fatalf("limit %d more %v", len(rows), more)
	}
	if _, rows, _ = list("?before=" + strconv.FormatInt(rows[0].ID, 10)); len(rows) != 2 {
		t.Fatalf("cursor %d", len(rows))
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, rows, _ = list("?from=" + future); len(rows) != 0 {
		t.Fatalf("from filter %d", len(rows))
	}
	for _, bad := range []string{"?status=abc", "?status=42", "?limit=500", "?before=-1", "?from=yesterday"} {
		if code, _, _ = list(bad); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	role = "user"
	if code, _, _ = list(""); code != 403 {
		t.Fatalf("non-admin got %d", code)
	}
	var stored int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM relay_request_log WHERE path LIKE '%secret%'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("query string stored: %d %v", stored, err)
	}
}
