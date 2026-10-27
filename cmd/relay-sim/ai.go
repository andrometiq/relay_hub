package main

// This endpoint exists only in the simulation command. It speaks the same chat
// protocol as frappe_tools' OpenRouter adapter and has no upstream transport.
import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type fakeAI struct{ records *recorder }

func (a *fakeAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	status := http.StatusOK
	result := map[string]any{}
	var turn struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	switch {
	case r.Method != http.MethodPost:
		status = http.StatusMethodNotAllowed
	case r.Header.Get("Authorization") != "Bearer relay-sim-ai-only":
		status = http.StatusUnauthorized
	case err != nil:
		status = http.StatusRequestEntityTooLarge
	case json.Unmarshal(body, &turn) != nil || turn.Model != "relay-sim-ledger" || len(turn.Messages) == 0:
		status = http.StatusBadRequest
	default:
		var prompt struct {
			Incoming string `json:"incoming"`
			Targets  []struct {
				ID string `json:"id"`
			} `json:"targets"`
		}
		var authorized map[string]any
		for _, m := range turn.Messages {
			if m.Role == "system" {
				var text string
				if json.Unmarshal(m.Content, &text) == nil {
					if _, plan, ok := strings.Cut(text, "\nAuthorized search:\n"); ok {
						plan, _, _ = strings.Cut(plan, "\nSkill:\n")
						_ = json.Unmarshal([]byte(plan), &authorized)
					}
				}
			}
			if m.Role != "user" {
				continue
			}
			var text string
			if json.Unmarshal(m.Content, &text) != nil {
				var parts []struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(m.Content, &parts) == nil {
					for _, part := range parts {
						text += part.Text
					}
				}
			}
			_ = json.Unmarshal([]byte(text), &prompt)
		}
		decision := map[string]any{"reply": "", "handoff": true, "reason": "Unsupported simulation intent"}
		message := map[string]any{"role": "assistant"}
		finish := "stop"
		plan := map[string]any{"purpose": "review your ledger entries and totals", "reference": "supplier_ledger",
			"fields":    []string{"company", "posting_date", "voucher_type", "voucher_no", "debit", "credit", "remarks"},
			"arguments": map[string]any{"filters": map[string]any{"posting_date": []any{"between", []string{"2026-10-01", "2026-10-31"}}}, "limit": 20}}
		switch strings.ToLower(strings.TrimSpace(prompt.Incoming)) {
		case "i have a doubt in my ledger":
			decision = map[string]any{"reply": "", "handoff": false, "reason": "", "search": plan}
		case "yes":
			if authorized != nil {
				if turn.Messages[len(turn.Messages)-1].Role == "tool" {
					// Deliberately wrong arithmetic proves Core, not the model, owns totals.
					decision = map[string]any{"reply": "MODEL INVENTED TOTAL 999999", "handoff": false, "reason": ""}
				} else {
					arguments, _ := json.Marshal(map[string]any{"purpose": authorized["purpose"], "reference": authorized["reference"], "arguments": authorized["arguments"]})
					message["tool_calls"] = []any{map[string]any{"id": "sim-ledger-read", "type": "function", "function": map[string]any{"name": "SEARCH", "arguments": string(arguments)}}}
					finish = "tool_calls"
				}
			}
		case "please connect me to a person", "talk to a human":
			decision = map[string]any{"reply": "", "handoff": true, "reason": "Supplier requested a person"}
		}
		if prompt.Targets != nil {
			scores := []any{}
			for _, target := range prompt.Targets {
				score := 0.0
				if strings.EqualFold(strings.TrimSpace(prompt.Incoming), "I have a doubt in my ledger") {
					score = 0.99
				}
				scores = append(scores, map[string]any{"id": target.ID, "score": score})
			}
			decision = map[string]any{"scores": scores, "human_request": strings.Contains(prompt.Incoming, "human") || strings.Contains(prompt.Incoming, "person")}
			delete(message, "tool_calls")
			finish = "stop"
		}
		if finish == "stop" {
			content, _ := json.Marshal(decision)
			message["content"] = string(content)
		}
		result = map[string]any{"id": "relay-sim-ai", "choices": []any{map[string]any{"message": message, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10}}
	}
	if status != http.StatusOK {
		result = map[string]any{"error": "invalid simulation AI request"}
	}
	reply, _ := json.Marshal(result)
	a.records.record(r, "ai", body, status, reply, started, "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(reply)
}
