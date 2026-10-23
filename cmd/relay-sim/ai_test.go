package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFakeAILedgerConversation(t *testing.T) {
	records := newRecorder()
	fake := &fakeAI{records: records}
	call := func(incoming string, history []any, tool bool) map[string]any {
		prompt, _ := json.Marshal(map[string]any{"incoming": incoming, "history": history})
		messages := []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": string(prompt)}}}}
		if tool {
			messages = append(messages, map[string]any{"role": "tool", "content": `{"totals":{"debit":"100.1","credit":"40.05","net_debit":"60.05"}}`})
		}
		body, _ := json.Marshal(map[string]any{"model": "relay-sim-ledger", "messages": messages})
		req := httptest.NewRequest(http.MethodPost, "/ai/chat/completions", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer relay-sim-ai-only")
		out := httptest.NewRecorder()
		fake.ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("status %d: %s", out.Code, out.Body.String())
		}
		var response struct {
			Choices []struct {
				Message map[string]any `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Choices[0].Message
	}
	decision := func(message map[string]any) map[string]any {
		var result map[string]any
		if err := json.Unmarshal([]byte(message["content"].(string)), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	offer := decision(call("I have a doubt in my ledger", nil, false))
	if offer["handoff"] != false || !strings.Contains(offer["reply"].(string), "2026-10-01 to 2026-10-31") {
		t.Fatal(offer)
	}
	if decision(call("yes", nil, false))["handoff"] != true {
		t.Fatal("read without confirmation")
	}
	history := []any{map[string]any{"content": offer["reply"]}}
	tool := call("yes", history, false)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tool["name"] != "supplier_ledger" || !strings.Contains(tool["arguments"].(string), "posting_date") {
		t.Fatal(tool)
	}
	if !strings.Contains(decision(call("yes", history, true))["reply"].(string), "MODEL INVENTED") {
		t.Fatal("must challenge Core arithmetic")
	}
	if decision(call("please connect me to a person", history, false))["handoff"] != true {
		t.Fatal("missing handoff")
	}
	for _, row := range records.snapshot() {
		if row.Kind != "ai" || row.Headers.Get("Authorization") != "[redacted]" {
			t.Fatal("unsafe capture")
		}
	}
}

func TestFakeAIRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		method, token, body string
		status              int
	}{
		{"GET", "relay-sim-ai-only", `{}`, 405},
		{"POST", "real-provider-key", `{}`, 401},
		{"POST", "relay-sim-ai-only", `{"model":"real-model","messages":[{}]}`, 400},
		{"POST", "relay-sim-ai-only", `{`, 400},
		{"POST", "relay-sim-ai-only", strings.Repeat("x", 129<<10), 413},
	} {
		req := httptest.NewRequest(tc.method, "/ai/chat/completions", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		out := httptest.NewRecorder()
		(&fakeAI{newRecorder()}).ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("want %d got %d", tc.status, out.Code)
		}
	}
}
