package hubmcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/attachment"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func TestMediaUploadNeedsNoRequestKeyAndReturnsID(t *testing.T) {
	response, _ := uploadThroughMCP(t, []byte("%PDF-1.4\nsynthetic fixture"))
	var envelope struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Result.IsError || envelope.Result.Structured["id"] != "555" {
		t.Fatal("media upload", response.Body.String())
	}
}

func TestMediaUploadCarriesTheLargestAttachment(t *testing.T) {
	raw := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), attachment.MaxBytes-9)...)
	response, received := uploadThroughMCP(t, raw)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"555"`) || received < attachment.MaxBytes {
		t.Fatal("largest attachment did not reach the provider", response.Code, received, response.Body.String()[:min(300, response.Body.Len())])
	}
}

// uploadThroughMCP sends a PDF through the authenticated MCP handler and returns the response and the provider body size.
func uploadThroughMCP(t *testing.T, pdf []byte) (*httptest.ResponseRecorder, int) {
	t.Helper()
	received := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v25.0/111/media" {
			t.Error("unexpected provider route", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		body, _ := io.ReadAll(r.Body)
		received = len(body)
		io.WriteString(w, `{"id":"555"}`)
	}))
	defer provider.Close()
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
		return auth.Principal{UserID: 1, Site: "site-a"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{"operation": "messages.media_upload", "media_type": "document", "mime_type": "application/pdf", "filename": "invoice.pdf", "content_base64": base64.StdEncoding.EncodeToString(pdf)}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "whatsapp.execute", "arguments": arguments}})
	request := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(string(raw)))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response, received
}
