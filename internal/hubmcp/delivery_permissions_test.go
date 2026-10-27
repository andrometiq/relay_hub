package hubmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
)

func TestChannelDeliveryResolutionEvidenceAndSiteBinding(t *testing.T) {
	db, _ := simulationDatabase(t, t.Context())
	account := "local"
	h, err := NewAuthenticatedChannels(db.Ping, nil, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{Site: account}, nil
	}, &channels.Registry{}, channels.Store{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any, wantError string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer local-test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var response struct {
			Error  any `json:"error"`
			Result struct {
				IsError    bool           `json:"isError"`
				Structured map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Error != nil || response.Result.IsError != (wantError != "") {
			t.Fatal("MCP response", w.Code, w.Body.String())
		}
		if wantError != "" && !strings.Contains(w.Body.String(), wantError) {
			t.Fatalf("expected %q: %s", wantError, w.Body.String())
		}
		if wantError == "" && response.Result.Structured == nil {
			t.Fatal("missing structured result")
		}
		return w.Body.String()
	}
	for _, operation := range []string{"telegram.message", "email.send"} {
		payload := `{"operation":"text","chat_id":-123,"text":"test"}`
		provider := "42"
		if operation == "email.send" {
			payload = `{"to":["r@example.invalid"],"subject":"test","body":"test"}`
			provider = "<confirmed@example.invalid>"
		}
		var id int64
		if err = db.QueryRow(t.Context(), `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash,state,site_id) VALUES('local',$1,$2,$3,'test','uncertain','local') RETURNING id`, strings.ReplaceAll(operation, ".", "-"), operation, []byte(payload)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"id": id, "outcome": "submitted", "note": "Verified provider acceptance", "provider_id": provider}
		if operation == "telegram.message" {
			args["chat_id"] = -123
		}
		account = "other"
		call("channels.resolve", args, "no rows")
		account = "local"
		if operation == "telegram.message" {
			delete(args, "chat_id")
			call("channels.resolve", args, "verified Telegram chat and message identifiers required")
			args["chat_id"] = -123
		}
		call("channels.resolve", args, "")
	}
	// Rejections do not require provider evidence, but accepted messages do.
	var rejectedID int64
	if err = db.QueryRow(t.Context(), `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash,state,site_id) VALUES('local','reject','email.send','{}','test','uncertain','local') RETURNING id`).Scan(&rejectedID); err != nil {
		t.Fatal(err)
	}
	call("channels.resolve", map[string]any{"id": rejectedID, "outcome": "submitted", "note": "Verified acceptance"}, "verified email Message-ID required")
	call("channels.resolve", map[string]any{"id": rejectedID, "outcome": "rejected", "note": "Verified rejection"}, "")

	const privateText = "private Telegram message"
	const privateReply = "private embedded reply"
	if _, err = db.Exec(t.Context(), `UPDATE relay_channel_outbox SET result=$1 WHERE account='local' AND request_key='telegram-message'`, []byte(`{"operation":"text","chat_id":-123,"message_id":42,"result":{"text":"`+privateText+`","reply_to_message":{"text":"`+privateReply+`"}}}`)); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{}, {"request_key": "telegram-message"}} {
		body := call("channels.deliveries", args, "")
		if !strings.Contains(body, privateText) || !strings.Contains(body, privateReply) {
			t.Fatal("site key did not receive full provider result")
		}
		account = "other"
		body = call("channels.deliveries", args, "")
		if strings.Contains(body, privateText) || strings.Contains(body, privateReply) {
			t.Fatal("cross-account disclosure")
		}
		account = "local"
	}
}
