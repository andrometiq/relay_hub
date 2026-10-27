package hubmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func TestWhatsAppV1Surface(t *testing.T) {
	calls := 0
	wa, err := whatsapp.NewWithHTTPClient([]whatsapp.Account{{ID: "a", Site: "https://core.example.test", AccessToken: "synthetic", PhoneID: "110", WABAID: "220", Version: "v25.0", Groups: true, Calls: true}}, &http.Client{Transport: simulationTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("unexpected provider request")
	})})
	if err != nil {
		t.Fatal(err)
	}
	wa.Outbox = &whatsapp.Outbox{Service: wa}
	h, err := NewAuthenticated(func(context.Context) error { return nil }, wa, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{Site: "a", Scopes: []string{"messages:send", "groups:read", "groups:write", "calls:read", "calls:write", "account:read"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string) string {
		r := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer synthetic")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	listed := request(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(listed, "whatsapp.enqueue") || !strings.Contains(listed, "whatsapp.bulk") {
		t.Fatal("v1 must advertise the template-only whatsapp.bulk tool and no raw enqueue", listed)
	}
	if body := request(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whatsapp.enqueue","arguments":{"messages":[]}}}`); !strings.Contains(body, `"error"`) {
		t.Fatal("raw enqueue tool still callable", body)
	}
	for _, op := range []string{"groups.list", "groups.send", "groups.create", "calls.action", "calls.permission.get", "calls.permission.request"} {
		for _, key := range []string{"", "durable-key"} {
			body := request(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whatsapp.execute","arguments":{"operation":%q,"request_key":%q}}}`, op, key))
			var reply struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if json.Unmarshal([]byte(body), &reply) != nil || !reply.Result.IsError {
				t.Fatal("v1 allowed", op, body)
			}
		}
	}
	body := request(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whatsapp.send","arguments":{"message":{"key":"group","message":{"operation":"groups.send","to":"room","body":"blocked"}}}}}`)
	if !strings.Contains(body, `"isError":true`) {
		t.Fatal(body)
	}
	if calls != 0 {
		t.Fatal("disabled routes contacted provider")
	}
}
