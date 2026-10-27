package hubmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func TestWhatsAppToolErrorsCarryMetaDetail(t *testing.T) {
	wa, err := whatsapp.NewWithHTTPClient([]whatsapp.Account{{ID: "a", Site: "https://core.example.test", AccessToken: "synthetic", PhoneID: "110", WABAID: "220", Version: "v25.0", Templates: true}}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"code":100,"error_subcode":2388042,"error_user_title":"Invalid header example","error_user_msg":"Add a sample image for the header.","fbtrace_id":"trace-secret"}}`))
		return w.Result(), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewAuthenticated(func(context.Context) error { return nil }, wa, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{Site: "a"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whatsapp.execute","arguments":{"operation":"templates.list"}}}`))
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-06-18")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var reply struct {
		Result struct {
			IsError    bool                    `json:"isError"`
			Content    []struct{ Text string } `json:"content"`
			Structured struct {
				Provider string `json:"provider"`
				Error    struct {
					Code    int    `json:"code"`
					Title   string `json:"title"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || !reply.Result.IsError {
		t.Fatal("expected an MCP tool error", w.Code, w.Body.String())
	}
	got := reply.Result.Structured
	if got.Provider != "WhatsApp" || got.Error.Code != 100 || got.Error.Title != "Invalid header example" || got.Error.Message != "Add a sample image for the header." {
		t.Fatal("structured Meta detail missing", w.Body.String())
	}
	if len(reply.Result.Content) == 0 || !strings.Contains(reply.Result.Content[0].Text, "Invalid header example") || strings.Contains(w.Body.String(), "trace-secret") {
		t.Fatal("tool error text lacks safe detail or leaks the trace", w.Body.String())
	}
}
