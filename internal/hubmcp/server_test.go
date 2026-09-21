package hubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticationAndOrigin(t *testing.T) {
	h, e := New(strings.Repeat("a", 64), func(context.Context) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		auth, origin string
		want         int
	}{{"", "", 401}, {"Bearer wrong", "", 401}, {"Bearer " + strings.Repeat("a", 64), "https://other.example", 403}} {
		r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{}`))
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d expected %d", w.Code, tc.want)
		}
	}
}
func TestHealthTool(t *testing.T) {
	for _, ready := range []bool{true, false} {
		h, e := New(strings.Repeat("a", 64), func(context.Context) error {
			if ready {
				return nil
			}
			return errors.New("private database error")
		})
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"health.get","arguments":{}}}`))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
		var payload struct {
			Result struct {
				IsError    bool   `json:"isError"`
				Structured Health `json:"structuredContent"`
			} `json:"result"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &payload); e != nil {
			t.Fatal(e)
		}
		if ready && payload.Result.Structured.Status != "ready" {
			t.Fatal("missing structured health")
		}
		if !ready && !payload.Result.IsError {
			t.Fatal("database failure hidden")
		}
		if strings.Contains(w.Body.String(), "private database error") {
			t.Fatal("leaked internal error")
		}
	}
}
func TestShortTokenRejected(t *testing.T) {
	if _, e := New("short", nil); e == nil {
		t.Fatal("short token accepted")
	}
}
