package telegram

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestTypingIsEphemeralAndBounded(t *testing.T) {
	calls := 0
	c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/sendChatAction") {
			t.Error("incorrect method")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["action"] != "typing" || body["chat_id"] != float64(-100123) || body["message_thread_id"] != float64(42) || len(body) != 3 {
			t.Errorf("wrong typing payload: %v", body)
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	if _, err := c.Typing(t.Context(), TypingInput{ChatID: -100123, ThreadID: 42}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []TypingInput{{}, {ChatID: 1, ThreadID: -1}, {ChatID: 1, ThreadID: 1 << 52}} {
		if _, err := c.Typing(t.Context(), in); err == nil {
			t.Error("invalid destination accepted")
		}
	}
	c.cfg.Enabled = false
	if _, err := c.Typing(t.Context(), TypingInput{ChatID: 1}); err == nil {
		t.Error("disabled account accepted")
	}
	if calls != 1 {
		t.Fatalf("unexpected provider calls: %d", calls)
	}
}
