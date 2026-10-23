package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// TypingInput is ephemeral and never enters the durable delivery outbox.
type TypingInput struct {
	ChatID   int64 `json:"chat_id"`
	ThreadID int64 `json:"message_thread_id,omitempty"`
}

func (c *Client) Typing(ctx context.Context, in TypingInput) (map[string]any, error) {
	if !c.cfg.Enabled || !validChatID(in.ChatID) || in.ThreadID < 0 || in.ThreadID >= 1<<52 {
		return nil, errors.New("invalid or disabled Telegram typing destination")
	}
	body := map[string]any{"chat_id": in.ChatID, "action": "typing"}
	if in.ThreadID > 0 {
		body["message_thread_id"] = in.ThreadID
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := c.call(ctx, "sendChatAction", body)
	if err != nil {
		return nil, err
	}
	var ok bool
	if json.Unmarshal(raw, &ok) != nil || !ok {
		return nil, errors.New("Telegram typing was not accepted")
	}
	return map[string]any{"ok": true}, nil
}
