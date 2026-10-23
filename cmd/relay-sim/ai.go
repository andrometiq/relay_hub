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
			History  []struct {
				Content string `json:"content"`
			} `json:"history"`
		}
		for _, m := range turn.Messages {
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
		switch strings.ToLower(strings.TrimSpace(prompt.Incoming)) {
		case "i have a doubt in my ledger":
			decision = map[string]any{"reply": "Shall I check your supplier ledger from 2026-10-01 to 2026-10-31? Please reply yes to confirm, or ask for a person.", "handoff": false, "reason": ""}
		case "yes":
			confirmed := false
			for _, h := range prompt.History {
				if strings.Contains(h.Content, "2026-10-01 to 2026-10-31") {
					confirmed = true
				}
			}
			if confirmed {
				if turn.Messages[len(turn.Messages)-1].Role == "tool" {
					// Deliberately wrong arithmetic proves Core, not the model, owns totals.
					decision = map[string]any{"reply": "MODEL INVENTED TOTAL 999999", "handoff": false, "reason": ""}
				} else {
					message["tool_calls"] = []any{map[string]any{"id": "sim-ledger-read", "type": "function", "function": map[string]any{"name": "supplier_ledger", "arguments": `{"filters":{"posting_date":["between",["2026-10-01","2026-10-31"]]},"limit":20}`}}}
					finish = "tool_calls"
				}
			}
		case "please connect me to a person":
			decision = map[string]any{"reply": "", "handoff": true, "reason": "Supplier requested a person"}
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
