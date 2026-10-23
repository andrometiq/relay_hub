package hubmcp

import (
	"context"
	"encoding/json"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTypingAccountAndScope(t *testing.T) {
	calls := 0
	client, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: "a", BotToken: "123456:" + strings.Repeat("a", 24), Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		account, scope string
		fail           bool
	}{{"a", "messages:send", false}, {"a", "messages:read", true}, {"other", "messages:send", true}} {
		h, err := NewAuthenticatedChannels(func(context.Context) error { return nil }, nil, func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{Account: tc.account, Scopes: []string{tc.scope}}, nil
		}, &channels.Registry{Telegram: map[string]*telegram.Client{"a": client}})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"telegram.typing","arguments":{"chat_id":123456}}}`))
		r.Header.Set("Authorization", "Bearer synthetic")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var response struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Result.IsError != tc.fail {
			t.Fatalf("wrong authorization result for %s/%s: %s", tc.account, tc.scope, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("unauthorized provider calls: %d", calls)
	}
}
