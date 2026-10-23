package hubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
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

func TestInvalidRequestsDoNotRunHealthCheck(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"malformed", `{`},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"relay.unknown"}`},
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"messages.send","arguments":{}}}`},
		{"oversized", strings.Repeat("x", 65537)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h, err := New(strings.Repeat("a", 64), func(context.Context) error { called = true; return nil })
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
			r.Header.Set("Accept", "application/json, text/event-stream")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("MCP-Protocol-Version", "2025-06-18")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if called {
				t.Fatal("invalid request ran health check")
			}
			if w.Code < 400 && !strings.Contains(w.Body.String(), `"error"`) && !strings.Contains(w.Body.String(), `"isError":true`) {
				t.Fatalf("invalid request accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWhatsAppAccountScope(t *testing.T) {
	account := whatsapp.Account{ID: "tenant-a", Site: "http://yrp.site:8002", CoreToken: strings.Repeat("b", 64), AccessToken: "secret", Version: "v25.0", PhoneID: "111", WABAID: "222", Templates: true}
	service, err := whatsapp.New([]whatsapp.Account{account})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewWithWhatsApp(strings.Repeat("a", 64), func(context.Context) error { return nil }, service)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token   string
		allowed bool
	}{{strings.Repeat("b", 64), true}, {strings.Repeat("a", 64), false}} {
		req := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whatsapp.execute","arguments":{"operation":"capabilities"}}}`))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if tc.allowed && !strings.Contains(out.Body.String(), `"account":"tenant-a"`) {
			t.Fatalf("missing account capabilities: %s", out.Body.String())
		}
		if !tc.allowed && !strings.Contains(out.Body.String(), `"isError":true`) {
			t.Fatal("global health token received business access")
		}
	}
}

func TestOutboxToolsRequireAccountCredential(t *testing.T) {
	account := whatsapp.Account{ID: "tenant-a", Site: "http://yrp.site:8002", CoreToken: strings.Repeat("b", 64), AccessToken: "secret", Version: "v25.0", PhoneID: "111", WABAID: "222"}
	service, err := whatsapp.New([]whatsapp.Account{account})
	if err != nil {
		t.Fatal(err)
	}
	service.Outbox = &whatsapp.Outbox{URL: "unused", Service: service}
	handler, err := NewWithWhatsApp(strings.Repeat("a", 64), func(context.Context) error { return nil }, service)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"whatsapp.send", "whatsapp.deliveries"} {
		args := map[string]any{}
		if tool == "whatsapp.send" {
			args["message"] = map[string]any{"key": "fixture", "message": map[string]any{"operation": "messages.send"}}
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if !strings.Contains(out.Body.String(), "account credential required") {
			t.Fatalf("global token was not rejected: %s", out.Body.String())
		}
	}
}

func TestProvisionedTokensEnforceAccountAndScopes(t *testing.T) {
	accounts := []whatsapp.Account{{ID: "site-a", Site: "https://a.example", AccessToken: "provider-secret", Version: "v25.0", PhoneID: "111", WABAID: "222", Templates: true}, {ID: "site-b", Site: "https://b.example", AccessToken: "other-secret", Version: "v25.0", PhoneID: "333", WABAID: "444"}}
	wa, e := whatsapp.New(accounts)
	if e != nil {
		t.Fatal(e)
	}
	revoked := false
	h, e := NewAuthenticated(func(context.Context) error { return nil }, wa, func(ctx context.Context, token string) (auth.Principal, error) {
		if token != "test-token" || revoked {
			return auth.Principal{}, auth.ErrDenied
		}
		return auth.Principal{UserID: 1, Account: "site-a", Scopes: []string{"account:read"}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	request := func(token, op string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whatsapp.execute","arguments":{"operation":"`+op+`"}}}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("test-token", "capabilities")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://a.example") || strings.Contains(w.Body.String(), "https://b.example") || strings.Contains(w.Body.String(), "provider-secret") {
		t.Fatal("account isolation", w.Body.String())
	}
	for _, operation := range []string{"templates.create", "templates.media_upload"} {
		w = request("test-token", operation)
		if !strings.Contains(w.Body.String(), "scope does not permit") {
			t.Fatal("scope bypass", operation, w.Body.String())
		}
	}
	if request(strings.Repeat("a", 64), "capabilities").Code != 401 {
		t.Fatal("legacy token accepted")
	}
	revoked = true
	if request("test-token", "capabilities").Code != 401 {
		t.Fatal("revoked token accepted")
	}
}
