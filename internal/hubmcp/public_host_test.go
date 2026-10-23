package hubmcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicOriginLocalHTTPBoundary(t *testing.T) {
	for _, tc := range []struct {
		origin string
		valid  bool
	}{
		{"http://localhost:18081", true}, {"http://127.0.0.1:18081", true}, {"http://[::1]:18081", true},
		{"http://evil.test", false}, {"http://localhost.evil.test", false}, {"http://192.168.1.2", false},
	} {
		t.Setenv("RELAY_PUBLIC_ORIGIN", tc.origin)
		_, err := New(strings.Repeat("x", 32), func(context.Context) error { return nil })
		if (err == nil) != tc.valid {
			t.Fatalf("origin %s: %v", tc.origin, err)
		}
	}
}

func TestPublicProxyHostAndRebindingProtection(t *testing.T) {
	t.Setenv("RELAY_PUBLIC_ORIGIN", "https://hub.example.test")
	token := strings.Repeat("x", 32)
	h, err := New(token, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host, origin string
		code         int
	}{
		{"hub.example.test", "", 200}, {"127.0.0.1:18081", "", 200}, {"localhost", "", 200},
		{"evil.test", "", 403}, {"hub.example.test.evil.test", "", 403}, {"hub.example.test", "https://hub.example.test", 403},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"health.get","arguments":{}}}`))
		r.Host = tc.host
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s origin=%q got %d: %s", tc.host, tc.origin, w.Code, w.Body.String())
		}
	}
}
