package hubmcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func readPricingHandler(t *testing.T, scopes []string, calls *atomic.Int32) http.Handler {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/v25.0/111/messages":
			io.WriteString(w, `{"success":true}`)
		case "/v25.0/222":
			io.WriteString(w, `{"pricing_analytics":{"data":[{"data_points":[{"volume":3,"cost":0.2}]}]}}`)
		default:
			t.Error("unexpected provider route", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(provider.Close)
	endpoint, _ := url.Parse(provider.URL)
	client := &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host, copy.Host = endpoint.Scheme, endpoint.Host, endpoint.Host
		return http.DefaultTransport.RoundTrip(copy)
	})}
	wa, err := whatsapp.NewWithHTTPClient([]whatsapp.Account{{ID: "site-a", Site: "https://a.example", AccessToken: "provider-secret", Version: "v25.0", PhoneID: "111", WABAID: "222"}}, client)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAuthenticated(func(context.Context) error { return nil }, wa, func(ctx context.Context, token string) (auth.Principal, error) {
		return auth.Principal{UserID: 1, Site: "site-a", Scopes: scopes}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func callExecute(handler http.Handler, arguments map[string]any) (bool, map[string]any, string) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "whatsapp.execute", "arguments": arguments}})
	request := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(string(raw)))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var envelope struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	ok := json.Unmarshal(response.Body.Bytes(), &envelope) == nil && envelope.Result.Structured != nil && !envelope.Result.IsError
	return ok, envelope.Result.Structured, response.Body.String()
}

func TestMessagesReadRunsDirectlyWithoutRequestKey(t *testing.T) {
	var calls atomic.Int32
	handler := readPricingHandler(t, []string{"messages:send"}, &calls)
	ok, result, body := callExecute(handler, map[string]any{"operation": "messages.read", "message_id": "wamid.SYNTHETIC", "typing": true})
	if !ok || result["success"] != true || calls.Load() != 1 {
		t.Fatal("read receipt", body)
	}
}

func TestBillingPricingNeedsAccountRead(t *testing.T) {
	var calls atomic.Int32
	arguments := map[string]any{"operation": "billing.pricing", "start": 1, "end": 2, "granularity": "DAILY"}
	ok, result, body := callExecute(readPricingHandler(t, []string{"account:read"}, &calls), arguments)
	if points, _ := result["data_points"].([]any); !ok || len(points) != 1 {
		t.Fatal("pricing", body)
	}
	if _, _, body = callExecute(readPricingHandler(t, []string{"messages:send"}, &calls), arguments); !strings.Contains(body, "scope does not permit") {
		t.Fatal("pricing scope bypass", body)
	}
	if _, _, body = callExecute(readPricingHandler(t, []string{"account:read"}, &calls), map[string]any{"operation": "messages.read", "message_id": "wamid.SYNTHETIC"}); !strings.Contains(body, "scope does not permit") {
		t.Fatal("read scope bypass", body)
	}
	if calls.Load() != 1 {
		t.Fatal("denied calls reached the provider", calls.Load())
	}
}
