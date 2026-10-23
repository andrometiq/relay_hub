package channels

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/andrometiq/relay_hub/internal/accounts"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"maps"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Registry struct {
	// Initialization-only maps. Runtime consumers use snapshot-backed methods;
	// configuration reloads must call Replace on this stable registry instance.
	Telegram   map[string]*telegram.Client
	Email      map[string]*relayemail.Client
	initialize sync.Once
	current    atomic.Pointer[registrySnapshot]
}
type registrySnapshot struct {
	telegram map[string]*telegram.Client
	email    map[string]*relayemail.Client
}
type Status struct{ Telegram, Email int }

func (r *Registry) snapshot() *registrySnapshot {
	r.initialize.Do(func() {
		r.current.Store(&registrySnapshot{telegram: maps.Clone(r.Telegram), email: maps.Clone(r.Email)})
	})
	return r.current.Load()
}

// Replace atomically publishes a fully validated registry while preserving the
// receiver's identity for handlers that captured it during server startup. A
// request already in flight may finish with its earlier snapshot; subsequent
// requests all see the newly enabled accounts and their new credentials.
func (r *Registry) Replace(next *Registry) error {
	if next == nil {
		return errors.New("replacement channel registry is required")
	}
	r.snapshot()
	r.current.Store(next.snapshot())
	return nil
}

func (r *Registry) Status() Status {
	s := r.snapshot()
	return Status{Telegram: len(s.telegram), Email: len(s.email)}
}

func Load(rows []accounts.RuntimeCredential) (*Registry, error) {
	r := &Registry{Telegram: map[string]*telegram.Client{}, Email: map[string]*relayemail.Client{}}
	for _, v := range rows {
		switch v.Channel {
		case "Telegram":
			c, e := telegram.New(telegram.Config{AccountID: v.AccountID, BotToken: v.Secret, Enabled: true})
			if e != nil {
				return nil, e
			}
			r.Telegram[v.AccountID] = c
		case "Email":
			port := 0
			if n, ok := v.Config["port"].(float64); ok {
				port = int(n)
			}
			if port == 0 {
				if s, ok := v.Config["port"].(string); ok {
					port, _ = strconv.Atoi(s)
				}
			}
			from, _ := v.Config["from"].(string)
			host, _ := v.Config["host"].(string)
			user, _ := v.Config["username"].(string)
			text := func(key string) string { value, _ := v.Config[key].(string); return value }
			imapPort := 0
			if value, ok := v.Config["imap_port"].(float64); ok {
				imapPort = int(value)
			} else {
				imapPort, _ = strconv.Atoi(text("imap_port"))
			}
			c, e := relayemail.New(relayemail.Config{AccountID: v.AccountID, Host: host, Port: port, Username: user, Password: v.Secret, From: from, Enabled: true, IMAPHost: text("imap_host"), IMAPPort: imapPort, IMAPUsername: text("imap_username"), IMAPPassword: text("imap_password"), IMAPFolder: text("imap_folder")})
			if e != nil {
				return nil, e
			}
			r.Email[v.AccountID] = c
		}
	}
	return r, nil
}
func (r *Registry) SendTelegram(ctx context.Context, account string, chat int64, text string, reply int64) error {
	return r.SendTelegramToThread(ctx, account, chat, text, reply, 0)
}
func (r *Registry) SendTelegramToThread(ctx context.Context, account string, chat int64, text string, reply, thread int64) error {
	c := r.snapshot().telegram[account]
	if c == nil {
		return errors.New("Telegram account is not configured")
	}
	return c.SendMessageToThread(ctx, chat, text, reply, thread)
}
func (r *Registry) TelegramGroup(ctx context.Context, account string, input telegram.GroupInput) (map[string]any, error) {
	c := r.snapshot().telegram[account]
	if c == nil {
		return nil, errors.New("Telegram account is not configured")
	}
	return c.ExecuteGroup(ctx, input)
}
func (r *Registry) TelegramMessage(ctx context.Context, account string, input telegram.MessageInput) (map[string]any, error) {
	c := r.snapshot().telegram[account]
	if c == nil {
		return nil, errors.New("Telegram account is not configured")
	}
	return c.ExecuteMessage(ctx, input)
}
func (r *Registry) TelegramSticker(ctx context.Context, account, fileID string) (telegram.StickerFile, error) {
	c := r.snapshot().telegram[account]
	if c == nil {
		return telegram.StickerFile{}, errors.New("Telegram account is not configured")
	}
	return c.FetchSticker(ctx, fileID)
}
func (r *Registry) SendEmail(ctx context.Context, account string, to []string, subject, body string, cc, bcc []string) error {
	c := r.snapshot().email[account]
	if c == nil {
		return errors.New("email account is not configured")
	}
	return c.Send(ctx, to, subject, body, cc, bcc)
}

// TelegramWebhookSecret derives an ingress credential for one configured bot.
// The master stays server-side; learning one bot's token cannot authenticate a
// webhook for a different account. Hex is accepted by Telegram's secret_token.
func (r *Registry) TelegramWebhookSecret(account, master string) (string, error) {
	return telegramWebhookSecret(r.snapshot().telegram[account], account, master)
}

func telegramWebhookSecret(client *telegram.Client, account, master string) (string, error) {
	if client == nil || len(master) < 32 {
		return "", errors.New("Telegram webhook is not configured")
	}
	mac := hmac.New(sha256.New, []byte(master))
	mac.Write([]byte("relay/telegram/webhook/v1/" + account))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (r *Registry) TelegramWebhook(ctx context.Context, store Store, account, master string, supplied []byte, body []byte) error {
	c := r.snapshot().telegram[account]
	token, secretErr := telegramWebhookSecret(c, account, master)
	if c == nil || secretErr != nil || subtle.ConstantTimeCompare([]byte(token), supplied) != 1 {
		return errors.New("invalid Telegram webhook")
	}
	message, err := c.ParseUpdate(body)
	if err != nil {
		return err
	}
	return store.Save(ctx, Event{Account: account, Channel: "Telegram", Direction: "inbound", ProviderID: strconv.FormatInt(message.UpdateID, 10), Sender: strconv.FormatInt(message.ChatID, 10), Body: message.Text, OccurredAt: message.Date, Payload: body})
}
func (r *Registry) EmailWebhook(ctx context.Context, store Store, account, token string, supplied []byte, body []byte) error {
	if r.snapshot().email[account] == nil || token == "" || subtle.ConstantTimeCompare([]byte(token), supplied) != 1 {
		return errors.New("invalid email webhook")
	}
	var in struct {
		ID, Sender, Recipient, Subject, Body string
		OccurredAt                           time.Time       `json:"occurred_at"`
		Payload                              json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(body, &in) != nil || in.ID == "" || in.Sender == "" || in.Body == "" {
		return errors.New("invalid email event")
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}
	if len(in.Payload) == 0 {
		in.Payload = body
	}
	return store.Save(ctx, Event{Account: account, Channel: "Email", Direction: "inbound", ProviderID: in.ID, Sender: in.Sender, Recipient: in.Recipient, Subject: in.Subject, Body: in.Body, OccurredAt: in.OccurredAt, Payload: in.Payload})
}

func (r *Registry) TelegramMedia(ctx context.Context, account, fileID string) (telegram.StickerFile, error) {
	c := r.snapshot().telegram[account]
	if c == nil {
		return telegram.StickerFile{}, errors.New("Telegram account is not configured")
	}
	return c.FetchMedia(ctx, fileID)
}

func (r *Registry) SendEmailMessage(ctx context.Context, account string, input relayemail.Message) (map[string]any, error) {
	c := r.snapshot().email[account]
	if c == nil {
		return nil, errors.New("email account is not configured")
	}
	if input.RequestKey == "" {
		input.RequestKey = rand.Text()
	}
	if err := c.SendMessage(ctx, input); err != nil {
		return nil, err
	}
	return map[string]any{"sent": true, "account": account, "message_id": c.MessageID(input.RequestKey)}, nil
}

func (r *Registry) TelegramTyping(ctx context.Context, account string, input telegram.TypingInput) (map[string]any, error) {
	c := r.snapshot().telegram[account]
	if c == nil {
		return nil, errors.New("Telegram account is not configured")
	}
	return c.Typing(ctx, input)
}
