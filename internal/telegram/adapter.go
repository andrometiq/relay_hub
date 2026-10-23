// Package telegram contains the transport-safe Telegram Bot API boundary.
// Provider calls stay behind this adapter so Core never handles bot credentials.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	AccountID, BotToken string
	Enabled             bool
}
type Message struct {
	UpdateID, ChatID, MessageID int64
	Username, Text              string
	Date                        time.Time
	// ChatID is the conversation, never the group participant's private chat.
	ChatType                                  string
	SenderID, SenderChatID, ThreadID, ReplyTo int64
	Kind                                      string
	ContentType                               string
	Sticker                                   *StickerContent
	Location                                  *LocationContent
	Contact                                   *ContactContent
	Reactions, OldReactions                   []Reaction
	ReactionCounts                            []ReactionCount
	Quote                                     *ReplyPreview
}
type Client struct {
	cfg  Config
	http *http.Client
}

var tokenRE = regexp.MustCompile(`^[0-9]{6,12}:[A-Za-z0-9_-]{20,}$`)

func New(cfg Config) (*Client, error) { return NewWithHTTPClient(cfg, nil) }

// NewWithHTTPClient supports instrumented transports and isolated simulations.
// The provider origin, deadline ceiling and redirect policy remain fixed.
func NewWithHTTPClient(cfg Config, supplied *http.Client) (*Client, error) {
	if strings.TrimSpace(cfg.AccountID) == "" || !tokenRE.MatchString(cfg.BotToken) {
		return nil, errors.New("invalid Telegram account configuration")
	}
	hc := http.Client{Timeout: 12 * time.Second}
	if supplied != nil {
		hc = *supplied
	}
	if hc.Timeout <= 0 || hc.Timeout > 12*time.Second {
		hc.Timeout = 12 * time.Second
	}
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	hc.Jar = nil
	return &Client{cfg: cfg, http: &hc}, nil
}

// APIError keeps retry/migration information without including URLs, tokens or
// provider descriptions, which may echo user-controlled content and secrets.
// The caller decides whether to retry. Mutations are never blindly replayed.
type APIError struct {
	StatusCode, ErrorCode, RetryAfter int
	MigrateToChatID                   int64
	OutcomeUnknown                    bool
}

func (e *APIError) Error() string {
	if e.OutcomeUnknown {
		return "Telegram transport failed; delivery outcome unknown"
	}
	return fmt.Sprintf("Telegram rejected request (HTTP %d, code %d)", e.StatusCode, e.ErrorCode)
}
func (c *Client) call(ctx context.Context, method string, body map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("invalid Telegram request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.cfg.BotToken+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("invalid Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	return c.perform(req)
}

func (c *Client) perform(req *http.Request) (json.RawMessage, error) {
	if !c.cfg.Enabled {
		return nil, errors.New("Telegram account is disabled")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &APIError{OutcomeUnknown: true}
	}
	defer resp.Body.Close()
	rawResponse, err := io.ReadAll(io.LimitReader(resp.Body, 1048577))
	if err != nil || len(rawResponse) > 1048576 {
		return nil, errors.New("invalid Telegram response; delivery outcome unknown")
	}
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter      int   `json:"retry_after"`
			MigrateToChatID int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	if json.Unmarshal(rawResponse, &envelope) != nil {
		return nil, errors.New("invalid Telegram response; delivery outcome unknown")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.OK {
		return nil, &APIError{StatusCode: resp.StatusCode, ErrorCode: envelope.ErrorCode, RetryAfter: envelope.Parameters.RetryAfter, MigrateToChatID: envelope.Parameters.MigrateToChatID}
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, errors.New("Telegram did not confirm request acceptance")
	}
	return envelope.Result, nil
}
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) error {
	return c.SendMessageToThread(ctx, chatID, text, replyTo, 0)
}
func (c *Client) SendMessageToThread(ctx context.Context, chatID int64, text string, replyTo, threadID int64) error {
	_, err := c.ExecuteMessage(ctx, MessageInput{Operation: "text", ChatID: chatID, Text: text, ReplyTo: replyTo, ThreadID: threadID})
	return err
}

// Telegram identifiers have at most 52 significant bits. Keep exact JSON and
// frontend round trips and reject int64 extremes instead of silently rounding.
func validChatID(id int64) bool { return id != 0 && id > -(1<<52) && id < (1<<52) }
