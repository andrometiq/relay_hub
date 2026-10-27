package hubmcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

type attachmentStore struct{}

func (attachmentStore) Save(context.Context, string, []byte) error { return nil }
func (attachmentStore) List(context.Context, string, int64) ([]whatsapp.Event, error) {
	return nil, nil
}
func (attachmentStore) AttachmentKnown(_ context.Context, account, id string) (bool, error) {
	return account == "site-a" && id == "123", nil
}
func (attachmentStore) MediaKnown(context.Context, string, string) (bool, error) { return false, nil }

type attachmentTransport func(*http.Request) (*http.Response, error)

func (f attachmentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAttachmentHTTPContract(t *testing.T) {
	for _, kind := range []string{"image/jpeg", "audio/ogg", "video/mp4", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"} {
		t.Run(kind, func(t *testing.T) {
			raw := bytes.Repeat([]byte("fixture"), 1000000) // Above the former 4 MiB attachment bound.
			var mu sync.Mutex
			calls := 0
			account := whatsapp.Account{ID: "site-a", Site: "https://fixture.invalid", AccessToken: "private-provider-token", Version: "v25.0", PhoneID: "111", WABAID: "222"}
			service, err := whatsapp.NewWithHTTPClient([]whatsapp.Account{account}, &http.Client{Transport: attachmentTransport(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				if r.Method != http.MethodGet {
					t.Error("media attempted a mutation")
				}
				body, mime := raw, kind
				if r.URL.Host == "graph.facebook.com" {
					body, _ = json.Marshal(map[string]any{"id": "123", "mime_type": kind, "file_size": len(raw), "url": "https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=123"})
					mime = "application/json"
				} else if r.URL.Host != "lookaside.fbsbx.com" {
					return nil, errors.New("unexpected destination")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {mime}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			service.Events = attachmentStore{}
			handler, err := NewAuthenticated(func(context.Context) error { return nil }, service, func(_ context.Context, token string) (auth.Principal, error) {
				switch token {
				case "reader":
					return auth.Principal{Site: "site-a"}, nil
				case "other-site":
					return auth.Principal{Site: "site-b"}, nil
				}
				return auth.Principal{}, auth.ErrDenied
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			request := func(token, tool, id string, status int, check string) {
				t.Helper()
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{"media_id": id}}})
				req, _ := http.NewRequest("POST", server.URL, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("MCP-Protocol-Version", "2025-06-18")
				res, e := server.Client().Do(req)
				if e != nil {
					t.Error(e)
					return
				}
				defer res.Body.Close()
				encoded, _ := io.ReadAll(res.Body)
				if res.StatusCode != status {
					t.Errorf("status=%d want=%d", res.StatusCode, status)
					return
				}
				if strings.Contains(string(encoded), "private-provider-token") {
					t.Error("credential leaked")
				}
				if status != 200 {
					return
				}
				var envelope struct {
					Result struct {
						IsError    bool           `json:"isError"`
						Structured map[string]any `json:"structuredContent"`
					} `json:"result"`
				}
				if e = json.Unmarshal(encoded, &envelope); e != nil {
					t.Error(e)
					return
				}
				result := envelope.Result
				if check != "" {
					detail, _ := result.Structured["error"].(map[string]any)
					if !result.IsError || result.Structured["provider"] != "Hub" || detail["check"] != check {
						t.Errorf("missing denial check %s", check)
					}
				} else {
					data, _ := result.Structured["content_base64"].(string)
					decoded, e := base64.StdEncoding.DecodeString(data)
					if result.IsError || e != nil || !bytes.Equal(decoded, raw) || result.Structured["content_type"] != kind || result.Structured["filename"] == "" {
						t.Error("invalid attachment response")
					}
				}
			}
			request("other-site", "whatsapp.attachment", "123", 200, "account")
			request("reader", "whatsapp.attachment", "999", 200, "ownership")
			request("reader", "whatsapp.attachment", "../123", 200, "input")
			request("reader", "whatsapp.media", "123", 200, "ownership")
			request("bad-token", "whatsapp.attachment", "123", 401, "")
			if calls != 0 {
				t.Fatal("denials contacted provider")
			}
			request("reader", "whatsapp.attachment", "123", 200, "")
		})
	}
}
