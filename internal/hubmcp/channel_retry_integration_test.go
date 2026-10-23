package hubmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

func TestChannelMCPPendingRetryWithoutRequestKey(t *testing.T) {
	for _, tool := range []string{"telegram.send", "telegram.message", "email.send"} {
		t.Run(tool, func(t *testing.T) {
			db, _ := simulationDatabase(t, t.Context())
			var calls atomic.Int32
			registry := &channels.Registry{}
			args := map[string]any{"chat_id": 123, "text": "Local durable retry fixture"}
			if tool == "email.send" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						calls.Add(1)
						conn.SetDeadline(time.Now().Add(time.Second))
						fmt.Fprint(conn, "421 Local fixture temporarily unavailable\r\n")
						conn.Close()
					}
				}()
				t.Cleanup(func() { listener.Close(); <-done })
				client, err := relayemail.New(relayemail.Config{AccountID: "fixture", Enabled: true, Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, From: "sender@example.invalid"})
				if err != nil {
					t.Fatal(err)
				}
				registry.Email = map[string]*relayemail.Client{"fixture": client}
				args = map[string]any{"to": []string{"recipient@example.invalid"}, "subject": "Local retry", "body": "Meaningful local message"}
			} else {
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`)
				}))
				t.Cleanup(provider.Close)
				client, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: "fixture", Enabled: true, BotToken: "123456:synthetic_fixture_token_never_used"}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
					req := r.Clone(r.Context())
					req.URL.Scheme = "http"
					req.URL.Host = provider.Listener.Addr().String()
					return http.DefaultTransport.RoundTrip(req)
				})})
				if err != nil {
					t.Fatal(err)
				}
				registry.Telegram = map[string]*telegram.Client{"fixture": client}
				if tool == "telegram.message" {
					args["operation"] = "text"
				}
			}
			handler, err := NewAuthenticatedChannels(db.Ping, nil, func(context.Context, string) (auth.Principal, error) {
				return auth.Principal{Account: "fixture", Scopes: []string{"messages:send", "messages:read"}}, nil
			}, registry, channels.Store{DB: db})
			if err != nil {
				t.Fatal(err)
			}
			call := func(name string, arguments map[string]any) map[string]any {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "http://127.0.0.1/mcp", bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer local-fixture")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("MCP-Protocol-Version", "2025-06-18")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				var response struct {
					Error  json.RawMessage `json:"error"`
					Result struct {
						IsError    bool           `json:"isError"`
						Structured map[string]any `json:"structuredContent"`
					} `json:"result"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Error) > 0 || response.Result.IsError || response.Result.Structured == nil {
					t.Fatalf("expected MCP acceptance: %s", w.Body.String())
				}
				return response.Result.Structured
			}
			receipt := call(tool, args) // request_key is deliberately omitted.
			key, _ := receipt["request_key"].(string)
			id, _ := receipt["delivery_id"].(float64)
			if key == "" || id < 1 || receipt["state"] != "queued" || receipt["pending"] != true || receipt["next_attempt"] == nil {
				t.Fatalf("missing durable pending receipt: %+v", receipt)
			}
			var scheduled bool
			if err := db.QueryRow(t.Context(), `SELECT state='queued' AND attempts=1 AND next_attempt>now() AND provider_error IS NOT NULL FROM relay_channel_outbox WHERE id=$1 AND request_key=$2`, int64(id), key).Scan(&scheduled); err != nil || !scheduled {
				t.Fatal("receipt does not identify a scheduled retry", err)
			}
			lookup := call("channels.deliveries", map[string]any{"request_key": key})
			rows, ok := lookup["deliveries"].([]any)
			if !ok || len(rows) != 1 || rows[0].(map[string]any)["id"] != id || rows[0].(map[string]any)["state"] != "queued" {
				t.Fatalf("pending receipt cannot be looked up: %+v", lookup)
			}
			args["request_key"] = key
			repeated := call(tool, args)
			if repeated["delivery_id"] != id || repeated["request_key"] != key || repeated["pending"] != true {
				t.Fatal("pending replay lost identity", repeated)
			}
			// A duplicate request during worker dispatch must also report acceptance.
			if _, err := db.Exec(t.Context(), `UPDATE relay_channel_outbox SET state='dispatching' WHERE id=$1`, int64(id)); err != nil {
				t.Fatal(err)
			}
			repeated = call(tool, args)
			if repeated["delivery_id"] != id || repeated["state"] != "dispatching" || repeated["pending"] != true {
				t.Fatal("dispatching replay lost identity", repeated)
			}
			var count int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_outbox`).Scan(&count); err != nil || count != 1 || calls.Load() != 1 {
				t.Fatal("retry bypassed backoff or created another intent", count, calls.Load(), err)
			}
		})
	}
}
