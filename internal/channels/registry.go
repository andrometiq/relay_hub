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
	"github.com/andrometiq/relay_hub/internal/containers"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"maps"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Registry struct {
	// Initialization-only maps. Runtime consumers use snapshot-backed methods;
	// configuration reloads must call Replace on this stable registry instance.
	Telegram map[string]*telegram.Client
	Email    map[string]*relayemail.Client
	// Bindings maps "Provider/key" to the channel's site and display address.
	// A registry without bindings treats each key as its own site.
	Bindings map[string]Binding
	// Paths maps "Provider/public path id" to the channel key. A registry
	// without paths accepts the key itself as the path.
	Paths      map[string]string
	initialize sync.Once
	current    atomic.Pointer[registrySnapshot]
}
type registrySnapshot struct {
	telegram map[string]*telegram.Client
	email    map[string]*relayemail.Client
	bindings map[string]Binding
	paths    map[string]string
}

// Binding is the current owner of one channel. Site is empty while unassigned.
type Binding struct {
	Site, To string
	// Path is the random public webhook path id of the channel.
	Path string
}
type Status struct{ Telegram, Email int }

func (r *Registry) snapshot() *registrySnapshot {
	r.initialize.Do(func() {
		r.current.Store(&registrySnapshot{telegram: maps.Clone(r.Telegram), email: maps.Clone(r.Email), bindings: maps.Clone(r.Bindings), paths: maps.Clone(r.Paths)})
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

// Binding returns the site and display address of one Telegram or Email channel.
func (r *Registry) Binding(provider, key string) Binding {
	s := r.snapshot()
	if s.bindings == nil {
		return Binding{Site: key, To: key}
	}
	return s.bindings[provider+"/"+key]
}

// WebhookKey returns the channel key behind a public Telegram or Email webhook path.
func (r *Registry) WebhookKey(provider, path string) (string, bool) {
	s := r.snapshot()
	if s.paths == nil {
		return path, true
	}
	key, ok := s.paths[provider+"/"+path]
	return key, ok
}

// WebhookPath returns the public webhook path id of a channel.
func (r *Registry) WebhookPath(provider, key string) string {
	if path := r.Binding(provider, key).Path; path != "" {
		return path
	}
	return key
}

// SiteKeys lists the active channel keys of one provider that a site uses, sorted.
func (r *Registry) SiteKeys(provider, site string) []string {
	s := r.snapshot()
	active := func(key string) bool {
		if provider == "Telegram" {
			return s.telegram[key] != nil
		}
		return provider == "Email" && s.email[key] != nil
	}
	keys := []string{}
	if s.bindings == nil {
		if active(site) {
			keys = append(keys, site)
		}
		return keys
	}
	for ref, binding := range s.bindings {
		key, found := strings.CutPrefix(ref, provider+"/")
		if found && binding.Site == site && active(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func Load(rows []containers.RuntimeCredential) (*Registry, error) {
	r := &Registry{Telegram: map[string]*telegram.Client{}, Email: map[string]*relayemail.Client{}, Bindings: map[string]Binding{}, Paths: map[string]string{}}
	for _, v := range rows {
		if v.Channel == "Telegram" || v.Channel == "Email" {
			r.Bindings[v.Channel+"/"+v.Key] = Binding{Site: v.Site, To: DisplayAddress(v), Path: v.WebhookID}
			r.Paths[v.Channel+"/"+v.WebhookID] = v.Key
		}
		switch v.Channel {
		case "Telegram":
			c, e := telegram.New(telegram.Config{AccountID: v.Key, BotToken: v.Secret, Enabled: true})
			if e != nil {
				return nil, e
			}
			r.Telegram[v.Key] = c
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
			c, e := relayemail.New(relayemail.Config{AccountID: v.Key, Host: host, Port: port, Username: user, Password: v.Secret, From: from, Enabled: true, TrustedAuthservID: text("trusted_authserv_id"), TrustedDSNDomain: text("trusted_dsn_domain"), IMAPHost: text("imap_host"), IMAPPort: imapPort, IMAPUsername: text("imap_username"), IMAPPassword: text("imap_password"), IMAPFolder: text("imap_folder")})
			if e != nil {
				return nil, e
			}
			r.Email[v.Key] = c
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

// EmailWebhookSecret is scoped to the configured account and never exposes the master.
func (r *Registry) EmailWebhookSecret(account, master string) (string, error) {
	return emailWebhookSecret(r.snapshot().email[account], account, master)
}
func emailWebhookSecret(client *relayemail.Client, account, master string) (string, error) {
	if client == nil || len(master) < 32 {
		return "", errors.New("email webhook is not configured")
	}
	mac := hmac.New(sha256.New, []byte(master))
	mac.Write([]byte("relay/email/webhook/v1/" + account))
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func (r *Registry) EmailWebhook(ctx context.Context, store Store, account, master string, supplied []byte, body []byte) error {
	client := r.snapshot().email[account]
	token, err := emailWebhookSecret(client, account, master)
	if err != nil || subtle.ConstantTimeCompare([]byte(token), supplied) != 1 {
		return errors.New("invalid email webhook")
	}
	var in struct {
		ID, Sender, Recipient, Subject, Body string
		Raw                                  string          `json:"raw"`
		Payload                              json.RawMessage `json:"payload"`
		OccurredAt                           time.Time       `json:"occurred_at"`
	}
	if json.Unmarshal(body, &in) != nil || in.ID == "" || len(in.ID) > 512 {
		return errors.New("invalid email event")
	}
	received := relayemail.Received{Authentication: relayemail.Authentication{Status: "unverified"}}
	if in.Raw != "" {
		received, err = client.ParseIngress([]byte(in.Raw))
		if err != nil {
			return err
		}
		in.Sender, in.Recipient, in.Subject, in.Body = received.From, strings.Join(received.To, ","), received.Subject, received.Body
	} else {
		received, err = unverifiedEmailPayload(in.Payload)
		if err != nil {
			return err
		}
		sender, parseErr := mail.ParseAddress(in.Sender)
		if parseErr != nil || in.Body == "" || len(in.Body) > 100000 || len(in.Subject) > 998 {
			return errors.New("invalid email event")
		}
		in.Sender = strings.ToLower(sender.Address)
		received.From, received.Subject, received.Body = in.Sender, in.Subject, in.Body
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}
	payload, err := json.Marshal(received)
	if err != nil {
		return err
	}
	return store.SaveEmail(ctx, Event{Account: account, Channel: "Email", Direction: "inbound", ProviderID: in.ID, Sender: in.Sender, Recipient: in.Recipient, Subject: in.Subject, Body: in.Body, OccurredAt: in.OccurredAt, Payload: payload}, received.DeliveryStatuses, received.Attachments)
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
	return sendEmailMessage(ctx, account, input, c)
}

func sendEmailMessage(ctx context.Context, account string, input EmailInput, c *relayemail.Client) (map[string]any, error) {
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

// displayAddress is the bot username or mailbox shown to Core as "to".
// DisplayAddress is the bot username, mailbox address or stored address of a channel.
func DisplayAddress(v containers.RuntimeCredential) string {
	if v.Channel == "Telegram" {
		if name, _ := v.Config["username"].(string); name != "" {
			return "@" + strings.TrimPrefix(name, "@")
		}
	}
	if v.Channel == "Email" {
		if from, _ := v.Config["from"].(string); from != "" {
			return from
		}
	}
	return v.Address
}
