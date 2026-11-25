package requestlog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryRecorder struct {
	mu      sync.Mutex
	entries []Entry
}

func (m *memoryRecorder) Record(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

func (m *memoryRecorder) only(t *testing.T) Entry {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(m.entries))
	}
	return m.entries[0]
}

func TestMiddlewareCapturesRequest(t *testing.T) {
	recorder := &memoryRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/telegram/{account}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		Tag(r.Context(), "site-a", r.PathValue("account"))
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
		w.Write(body[:3])
		w.Write([]byte("!!"))
	})
	r := httptest.NewRequest("POST", "/webhooks/telegram/bot1?token=secret&x=1", strings.NewReader("hello world"))
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.9")
	r.Header.Set("User-Agent", strings.Repeat("a", 300))
	w := httptest.NewRecorder()
	Middleware(recorder, mux).ServeHTTP(w, r)

	e := recorder.only(t)
	if e.Path != "/webhooks/telegram/bot1" || strings.Contains(e.Path, "secret") {
		t.Fatalf("path %q", e.Path)
	}
	if e.Route != "POST /webhooks/telegram/{account}" || e.Method != "POST" || e.Status != 202 {
		t.Fatalf("route %q method %q status %d", e.Route, e.Method, e.Status)
	}
	if e.RequestBytes != 11 || e.ResponseBytes != 5 || e.DurationMS < 5 {
		t.Fatalf("bytes %d/%d duration %v", e.RequestBytes, e.ResponseBytes, e.DurationMS)
	}
	if e.SiteID != "site-a" || e.Account != "bot1" || e.RemoteIP.String() != "203.0.113.9" || len(e.UserAgent) != 200 {
		t.Fatalf("tags %+v", e)
	}
	if e.RequestID == "" || w.Header().Get("X-Request-ID") != e.RequestID || e.Error != "" {
		t.Fatalf("request id %q header %q error %q", e.RequestID, w.Header().Get("X-Request-ID"), e.Error)
	}
}

func TestMiddlewareKeepsValidProxyRequestID(t *testing.T) {
	for id, kept := range map[string]bool{"0123456789abcdef": true, "short": false, "bad id with spaces": false} {
		recorder := &memoryRecorder{}
		r := httptest.NewRequest("GET", "/api/channels", nil)
		r.Header.Set("X-Request-ID", id)
		Middleware(recorder, http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), r)
		if got := recorder.only(t).RequestID; (got == id) != kept {
			t.Fatalf("%q: stored %q", id, got)
		}
	}
}

func TestMiddlewareDefaultsAndErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		status  int
		error   string
	}{
		{"no write", func(http.ResponseWriter, *http.Request) {}, 200, ""},
		{"body only", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }, 200, ""},
		{"client error text is not kept", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "invalid body", 413) }, 413, ""},
		{"server error text is kept", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "event persistence failed", 503) }, 503, "event persistence failed"},
		{"long server error is cut", func(w http.ResponseWriter, r *http.Request) { http.Error(w, strings.Repeat("x", 500), 500) }, 500, strings.Repeat("x", 200)},
		{"json 5xx body is not kept", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			w.Write([]byte(`{"token":"x"}`))
		}, 503, ""},
	}
	for _, tc := range cases {
		recorder := &memoryRecorder{}
		Middleware(recorder, tc.handler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/mcp", nil))
		if e := recorder.only(t); e.Status != tc.status || e.Error != tc.error {
			t.Fatalf("%s: status %d error %q", tc.name, e.Status, e.Error)
		}
	}
}

func TestMiddlewareRecordsPanicAsServerError(t *testing.T) {
	recorder := &memoryRecorder{}
	handler := Middleware(recorder, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		panic("boom")
	}))
	func() {
		defer func() {
			if recover() != "boom" {
				t.Fatal("panic must reach the server unchanged")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/accounts", nil))
	}()
	if e := recorder.only(t); e.Status != 500 || e.Error != "panic" {
		t.Fatalf("status %d error %q", e.Status, e.Error)
	}
}

func TestMiddlewareSkipsNoise(t *testing.T) {
	recorder := &memoryRecorder{}
	served := 0
	handler := Middleware(recorder, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served++ }))
	for _, path := range []string{"/assets/app.js", "/healthz", "/readyz", "/socket.io/?EIO=4"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	if served != 4 || len(recorder.entries) != 0 {
		t.Fatalf("served %d logged %d", served, len(recorder.entries))
	}
}

func TestTagWithoutMiddlewareIsSafe(t *testing.T) {
	Tag(context.Background(), "site", "account")
}

func TestCleanKeepsValidText(t *testing.T) {
	if got := clean("a\x00b\xffc", 10); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := clean("ééé", 5); got != "éé" {
		t.Fatalf("got %q", got)
	}
}

func TestMiddlewareFlushReachesServerWriter(t *testing.T) {
	recorder := &memoryRecorder{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: one\n\n"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
	})
	w := httptest.NewRecorder()
	Middleware(recorder, handler).ServeHTTP(w, httptest.NewRequest("GET", "/api/events", nil))

	if !w.Flushed {
		t.Fatal("flush did not reach the server writer")
	}
	if e := recorder.only(t); e.Status != 200 || e.ResponseBytes != 11 {
		t.Fatalf("status %d bytes %d", e.Status, e.ResponseBytes)
	}
}
