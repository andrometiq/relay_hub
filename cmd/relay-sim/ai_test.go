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
	call := func(incoming string, search map[string]any, targets []any, tool bool) map[string]any {
		input := map[string]any{"incoming": incoming}
		if targets != nil {
			input["targets"] = targets
		}
		prompt, _ := json.Marshal(input)
		plan, _ := json.Marshal(search)
		messages := []any{map[string]any{"role": "system", "content": "\nAuthorized search:\n" + string(plan) + "\nSkill:\nLocal ledger"}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": string(prompt)}}}}
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
	offer := decision(call("I have a doubt in my ledger", nil, nil, false))
	if offer["handoff"] != false || offer["search"] == nil {
		t.Fatal(offer)
	}
	if decision(call("yes", nil, nil, false))["handoff"] != true {
		t.Fatal("read without confirmation")
	}
	plan := offer["search"].(map[string]any)
	targets := []any{map[string]any{"id": "event-a"}, map[string]any{"id": "event-b"}}
	for _, incoming := range []string{"I have a doubt in my ledger", "unknown", "talk to a human"} {
		matched := decision(call(incoming, nil, targets, false))
		scores := matched["scores"].([]any)
		if len(matched) != 2 || len(scores) != 2 || matched["human_request"] != (incoming == "talk to a human") {
			t.Fatal(matched)
		}
		for i, id := range []string{"event-a", "event-b"} {
			row := scores[i].(map[string]any)
			expected := 0.0
			if incoming == "I have a doubt in my ledger" {
				expected = 0.99
			}
			if row["id"] != id || row["score"] != expected {
				t.Fatal(row)
			}
		}
	}
	tool := call("yes", plan, nil, false)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tool["name"] != "SEARCH" || !strings.Contains(tool["arguments"].(string), "posting_date") {
		t.Fatal(tool)
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(tool["arguments"].(string)), &arguments); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"purpose", "reference", "arguments"} {
		got, _ := json.Marshal(arguments[key])
		want, _ := json.Marshal(plan[key])
		if string(got) != string(want) {
			t.Fatalf("confirmed search changed %s", key)
		}
	}
	if !strings.Contains(decision(call("yes", plan, nil, true))["reply"].(string), "MODEL INVENTED") {
		t.Fatal("must challenge Core arithmetic")
	}
	if decision(call("talk to a human", plan, nil, false))["handoff"] != true {
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
