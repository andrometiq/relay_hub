package hubmcp

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Health struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

type accountKey struct{}
type principalKey struct{}
type TokenVerifier func(context.Context, string) (auth.Principal, error)

func mediaResult(data []byte, mime, name string, limits ...int) (*mcp.CallToolResult, map[string]any, error) {
	limit := 2 << 20
	if len(limits) > 0 {
		limit = limits[0]
	}
	if len(data) == 0 || len(data) > limit {
		return nil, nil, errors.New("attachment exceeds the proxy limit")
	}
	// Explicit text avoids SDK duplication of the base64 data in both Content
	// and StructuredContent, keeping the complete JSON envelope below 3 MiB.
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Media content attached"}}}, map[string]any{
		"content_base64": base64.StdEncoding.EncodeToString(data), "content_type": mime, "filename": name,
	}, nil
}

func allowed(ctx context.Context, scope string) bool {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return !ok || p.Allows(scope)
}

// Preserve only safe provider control fields. A migration suggestion is never
// applied automatically: Core must verify a binding for the new destination.
func telegramFailure(err error) (*mcp.CallToolResult, map[string]any, error) {
	var providerError *telegram.APIError
	if !errors.As(err, &providerError) {
		return nil, nil, err
	}
	result := &mcp.CallToolResult{}
	result.SetError(err)
	return result, map[string]any{"provider": "Telegram", "error": map[string]any{
		"http_status": providerError.StatusCode, "code": providerError.ErrorCode,
		"retry_after": providerError.RetryAfter, "migrate_to_chat_id": providerError.MigrateToChatID,
		"outcome_unknown": providerError.OutcomeUnknown,
	}}, nil
}
func whatsappCallFailure(err error) (*mcp.CallToolResult, map[string]any, error) {
	var providerError *whatsapp.ProviderError
	status := 0
	unknown := true
	if errors.As(err, &providerError) {
		status = providerError.Status
		unknown = !(status >= 400 && status < 500 && status != 408)
	} else if errors.Is(err, whatsapp.ErrBusy) {
		unknown = false
	} else {
		return nil, nil, err
	}
	result := &mcp.CallToolResult{}
	result.SetError(err)
	return result, map[string]any{"provider": "WhatsApp", "error": map[string]any{"http_status": status, "outcome_unknown": unknown}}, nil
}
func operationScope(op string) string {
	switch op {
	case "messages.flow", "messages.send", "messages.media", "messages.react", "messages.location", "messages.contacts", "templates.send", "groups.send", "groups.media", "groups.template", "groups.pin":
		return "messages:send"
	case "messages.history", "events.list", "events.latest":
		return "messages:read"
	case "capabilities", "account.profile":
		return "account:read"
	case "flows.list", "flows.get", "flows.json", "flows.assets", "account.metadata", "templates.list":
		return "templates:read"
	case "groups.list", "groups.info", "groups.requests.list", "groups.invite.get":
		return "groups:read"
	case "calls.permission.get":
		return "calls:read"
	}
	if strings.HasPrefix(op, "templates.") || strings.HasPrefix(op, "flows.") {
		return "templates:write"
	}
	if strings.HasPrefix(op, "groups.") {
		return "groups:write"
	}
	if strings.HasPrefix(op, "calls.") {
		return "calls:write"
	}
	return "invalid"
}

func New(token string, check func(context.Context) error) (http.Handler, error) {
	return NewWithWhatsApp(token, check, nil)
}

func NewWithWhatsApp(token string, check func(context.Context) error, wa *whatsapp.Service) (http.Handler, error) {
	return newServer(token, check, wa, nil)
}

// NewAuthenticated disables all legacy shared tokens.
func NewAuthenticated(check func(context.Context) error, wa *whatsapp.Service, verify TokenVerifier) (http.Handler, error) {
	if verify == nil {
		return nil, errors.New("token verifier required")
	}
	return newServer("", check, wa, verify)
}
func NewAuthenticatedChannels(check func(context.Context) error, wa *whatsapp.Service, verify TokenVerifier, registry *channels.Registry, mediaStores ...channels.Store) (http.Handler, error) {
	if verify == nil {
		return nil, errors.New("token verifier required")
	}
	return newServerChannels("", check, wa, verify, registry, mediaStores...)
}
func newServer(token string, check func(context.Context) error, wa *whatsapp.Service, verify TokenVerifier) (http.Handler, error) {
	return newServerChannels(token, check, wa, verify, nil)
}
func newServerChannels(token string, check func(context.Context) error, wa *whatsapp.Service, verify TokenVerifier, registry *channels.Registry, mediaStores ...channels.Store) (http.Handler, error) {
	var mediaStore channels.Store
	if len(mediaStores) > 0 {
		mediaStore = mediaStores[0]
	}
	if verify == nil && len(token) < 32 {
		return nil, errors.New("RELAY_MCP_TOKEN must contain at least 32 characters")
	}
	if verify == nil && wa != nil {
		for _, account := range wa.AccountsSnapshot() {
			if subtle.ConstantTimeCompare([]byte(token), []byte(account.CoreToken)) == 1 {
				return nil, errors.New("account credential must differ from Hub service credential")
			}
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "relay-hub", Version: "0.1.0"}, nil)
	if verify != nil && len(mediaStores) > 0 && mediaStores[0].DB != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "account.get", Description: "Read this token's account and site binding; no credentials are returned."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, map[string]any, error) {
			principal, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok || !allowed(ctx, "account:read") {
				return nil, nil, errors.New("account:read required")
			}
			var site string
			var enabled bool
			if e := mediaStores[0].DB.QueryRow(ctx, `SELECT site,enabled FROM relay_accounts WHERE account_id=$1`, principal.Account).Scan(&site, &enabled); e != nil || !enabled {
				return nil, nil, errors.New("account unavailable")
			}
			return nil, map[string]any{"account": principal.Account, "site": site, "enabled": enabled}, nil
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "health.get", Description: "Check Relay Hub database readiness"}, func(ctx context.Context, r *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, Health, error) {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if check(c) != nil {
			return nil, Health{}, errors.New("Hub is not ready")
		}
		return nil, Health{Status: "ready", Service: "relay_hub"}, nil
	})
	if wa != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.attachment", Description: "Read a bounded attachment already received by this account. Provider credentials and download URLs remain in Hub."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			MediaID string `json:"media_id"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			account, ok := ctx.Value(accountKey{}).(whatsapp.Account)
			if !ok || !allowed(ctx, "messages:read") {
				return nil, nil, errors.New("account credential with messages:read required")
			}
			file, err := wa.DownloadAttachment(ctx, account, in.MediaID)
			if err != nil {
				return nil, nil, errors.New("attachment is unavailable for this account")
			}
			return mediaResult(file.Data, file.MIME, file.Filename, 4<<20)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.media", Description: "Read a bounded sticker already received by this account. Provider credentials and download URLs stay in Hub."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			MediaID string `json:"media_id"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			account, ok := ctx.Value(accountKey{}).(whatsapp.Account)
			if !ok || !allowed(ctx, "messages:read") {
				return nil, nil, errors.New("account credential with messages:read required")
			}
			file, err := wa.DownloadMedia(ctx, account, in.MediaID, 2<<20)
			if err != nil {
				return nil, nil, errors.New("sticker is unavailable for this account")
			}
			return mediaResult(file.Data, file.MIME, file.Filename)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.execute", Description: "Account-scoped WhatsApp templates, groups and call signaling. Mutations are not automatically retried."}, func(ctx context.Context, r *mcp.CallToolRequest, input whatsapp.Input) (*mcp.CallToolResult, map[string]any, error) {
			account, ok := ctx.Value(accountKey{}).(whatsapp.Account)
			if !ok {
				return nil, nil, errors.New("an account-scoped credential is required")
			}
			if !allowed(ctx, operationScope(input.Operation)) || input.Operation == "account.metadata" && !allowed(ctx, "account:read") {
				return nil, nil, errors.New("token scope does not permit operation")
			}
			if input.RequestKey != "" {
				if wa.Outbox == nil {
					return nil, nil, errors.New("durable delivery unavailable")
				}
				result, err := wa.Outbox.Submit(ctx, account, input)
				return nil, result, err
			}
			result, err := wa.Execute(ctx, account, input)
			if err != nil && input.Operation == "calls.action" {
				return whatsappCallFailure(err)
			}
			return nil, result, err
		})
	}
	if wa != nil && wa.Outbox != nil {
		enqueue := func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			Messages []whatsapp.Submission `json:"messages"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			a, ok := ctx.Value(accountKey{}).(whatsapp.Account)
			if !ok {
				return nil, nil, errors.New("account credential required")
			}
			if !allowed(ctx, "messages:send") {
				return nil, nil, errors.New("messages:send scope required")
			}
			rows, err := wa.Outbox.Enqueue(ctx, a, in.Messages)
			if err != nil {
				return nil, nil, errors.New("batch not confirmed; check input and retry with the same keys")
			}
			return nil, map[string]any{"deliveries": rows}, err
		}
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.enqueue", Description: "Durably queue 1–50 messages using stable request keys."}, enqueue)
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.bulk", Description: "Durably queue 1–50 messages using stable request keys."}, enqueue)
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.send", Description: "Durably queue one message using a stable request key."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			Message whatsapp.Submission `json:"message"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			return enqueue(ctx, r, struct {
				Messages []whatsapp.Submission `json:"messages"`
			}{Messages: []whatsapp.Submission{in.Message}})
		})

		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.resolve", Description: "Record a verified uncertain delivery outcome without resending."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			ID         int64  `json:"id"`
			Outcome    string `json:"outcome"`
			Note       string `json:"note"`
			ProviderID string `json:"provider_id,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			a, ok := ctx.Value(accountKey{}).(whatsapp.Account)
			if !ok || !allowed(ctx, "deliveries:read") || !allowed(ctx, "deliveries:cancel") {
				return nil, nil, errors.New("delivery read and cancellation scopes required")
			}
			if err := wa.Outbox.Resolve(ctx, a, in.ID, in.Outcome, in.Note, in.ProviderID); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"resolved": true}, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.deliveries", Description: "List account delivery intents or cancel a queued intent."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			RequestKey string `json:"request_key,omitempty"`
			After      string `json:"after,omitempty"`
			CancelID   string `json:"cancel_id,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			a, ok := ctx.Value(accountKey{}).(whatsapp.Account)
			if !ok {
				return nil, nil, errors.New("account credential required")
			}
			if in.CancelID != "" {
				if !allowed(ctx, "deliveries:cancel") {
					return nil, nil, errors.New("deliveries:cancel scope required")
				}
				err := wa.Outbox.Cancel(ctx, a, in.CancelID)
				if err != nil {
					return nil, nil, errors.New("queued delivery could not be cancelled")
				}
				return nil, map[string]any{"cancelled": err == nil}, err
			}
			if !allowed(ctx, "deliveries:read") {
				return nil, nil, errors.New("deliveries:read scope required")
			}
			if in.RequestKey != "" {
				d, err := wa.Outbox.Lookup(ctx, a, in.RequestKey)
				if err != nil {
					return nil, nil, errors.New("delivery unavailable")
				}
				return nil, map[string]any{"deliveries": []whatsapp.Delivery{d}}, nil
			}
			rows, err := wa.Outbox.List(ctx, a, in.After)
			if err != nil {
				return nil, nil, errors.New("deliveries unavailable")
			}
			return nil, map[string]any{"deliveries": rows}, err
		})
	}
	if registry != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.typing", Description: "Briefly indicate typing using the account bot. Ephemeral; never queued or replayed."}, func(ctx context.Context, r *mcp.CallToolRequest, in telegram.TypingInput) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok || !allowed(ctx, "messages:send") {
				return nil, nil, errors.New("account credential with messages:send required")
			}
			result, err := registry.TelegramTyping(ctx, p.Account, in)
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.message", Description: "Send text, sticker, reaction, location or contact using the account bot. Replies preserve their group and topic. Mutations are sent once."}, func(ctx context.Context, r *mcp.CallToolRequest, in telegram.MessageInput) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok || !allowed(ctx, "messages:send") {
				return nil, nil, errors.New("account credential with messages:send required")
			}
			var result map[string]any
			var err error
			if mediaStore.DB != nil {
				key := in.RequestKey
				in.RequestKey = ""
				result, err = (channels.Outbox{DB: mediaStore.DB, Registry: registry}).Submit(ctx, p.Account, key, "telegram.message", in)
			} else {
				result, err = registry.TelegramMessage(ctx, p.Account, in)
			}
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, nil
		})

		mcp.AddTool(server, &mcp.Tool{Name: "telegram.attachment", Description: "Read an account-journaled Telegram attachment up to 4 MiB without exposing provider credentials."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			FileID string `json:"file_id"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok || !allowed(ctx, "messages:read") {
				return nil, nil, errors.New("messages:read required")
			}
			known, err := mediaStore.TelegramMediaKnown(ctx, p.Account, in.FileID)
			if err != nil || !known {
				return nil, nil, errors.New("attachment is unavailable for this account")
			}
			file, err := registry.TelegramMedia(ctx, p.Account, in.FileID)
			if err != nil {
				return nil, nil, errors.New("attachment is unavailable for this account")
			}
			return mediaResult(file.Data, file.ContentType, file.Filename, telegram.MaxMediaBytes)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.media", Description: "Read a bounded sticker already received by this account. Provider credentials and download URLs stay in Hub."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			FileID string `json:"file_id"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok || !allowed(ctx, "messages:read") {
				return nil, nil, errors.New("account credential with messages:read required")
			}
			known, err := mediaStore.TelegramStickerKnown(ctx, p.Account, in.FileID)
			if err != nil || !known {
				return nil, nil, errors.New("sticker is unavailable for this account")
			}
			file, err := registry.TelegramSticker(ctx, p.Account, in.FileID)
			if err != nil {
				return nil, nil, errors.New("sticker is unavailable for this account")
			}
			return mediaResult(file.Data, file.ContentType, file.Filename)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.send", Description: "Send a Telegram message through the account-scoped bot."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			RequestKey string `json:"request_key,omitempty"`
			ChatID     int64  `json:"chat_id"`
			Text       string `json:"text"`
			ReplyTo    int64  `json:"reply_to,omitempty"`
			ThreadID   int64  `json:"message_thread_id,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok {
				return nil, nil, errors.New("account credential required")
			}
			if !allowed(ctx, "messages:send") {
				return nil, nil, errors.New("messages:send scope required")
			}
			var err error
			var result map[string]any
			message := telegram.MessageInput{Operation: "text", ChatID: in.ChatID, Text: in.Text, ReplyTo: in.ReplyTo, ThreadID: in.ThreadID}
			if mediaStore.DB != nil {
				result, err = (channels.Outbox{DB: mediaStore.DB, Registry: registry}).Submit(ctx, p.Account, in.RequestKey, "telegram.message", message)
			} else {
				result, err = registry.TelegramMessage(ctx, p.Account, message)
			}
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.groups", Description: "Manage Telegram groups, members, invitations and forum topics through the account-scoped bot. Requires the corresponding Telegram administrator permissions; mutations are not automatically retried."}, func(ctx context.Context, r *mcp.CallToolRequest, in telegram.GroupInput) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok {
				return nil, nil, errors.New("account credential required")
			}
			scope := "groups:write"
			if telegram.GroupReadOperation(in.Operation) {
				scope = "groups:read"
			}
			if !allowed(ctx, scope) {
				return nil, nil, errors.New(scope + " scope required")
			}
			result, err := registry.TelegramGroup(ctx, p.Account, in)
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, err
		})
		mcp.AddTool(server, &mcp.Tool{Name: "email.attachment", Description: "Read an account-scoped mailbox attachment up to 4 MiB."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			ProviderID string `json:"provider_id"`
			Index      int    `json:"index"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok || !allowed(ctx, "messages:read") || mediaStore.DB == nil || in.Index < 0 || in.Index > 7 || len(in.ProviderID) > 300 {
				return nil, nil, errors.New("attachment unavailable")
			}
			var data []byte
			var mime, name string
			if err := mediaStore.DB.QueryRow(ctx, `SELECT content,content_type,filename FROM relay_email_attachments WHERE account=$1 AND provider_id=$2 AND slot=$3`, p.Account, in.ProviderID, in.Index).Scan(&data, &mime, &name); err != nil {
				return nil, nil, errors.New("attachment unavailable")
			}
			return mediaResult(data, mime, name, 4<<20)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "email.mailbox", Description: "Read mailbox sync status and the twenty latest rejected messages for this account."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok || !allowed(ctx, "messages:read") || mediaStore.DB == nil {
				return nil, nil, errors.New("messages:read required")
			}
			rows, err := mediaStore.DB.Query(ctx, `SELECT jsonb_build_object('mailbox',mailbox,'last_sync',last_sync,'error',last_error,'last_uid',last_uid) FROM relay_mailbox_cursor WHERE account=$1 ORDER BY last_sync DESC NULLS LAST LIMIT 10`, p.Account)
			if err != nil {
				return nil, nil, err
			}
			states := []map[string]any{}
			for rows.Next() {
				var value map[string]any
				if err = rows.Scan(&value); err != nil {
					rows.Close()
					return nil, nil, err
				}
				states = append(states, value)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, nil, err
			}
			rejected, err := mediaStore.DB.Query(ctx, `SELECT jsonb_build_object('uid',uid,'reason',reason,'created_at',created_at) FROM relay_mailbox_rejections WHERE account=$1 ORDER BY created_at DESC LIMIT 20`, p.Account)
			if err != nil {
				return nil, nil, err
			}
			defer rejected.Close()
			issues := []map[string]any{}
			for rejected.Next() {
				var value map[string]any
				if err = rejected.Scan(&value); err != nil {
					return nil, nil, err
				}
				issues = append(issues, value)
			}
			return nil, map[string]any{"mailboxes": states, "rejections": issues}, rejected.Err()
		})
		mcp.AddTool(server, &mcp.Tool{Name: "email.send", Description: "Send an email through the account-scoped SMTP adapter."}, func(ctx context.Context, r *mcp.CallToolRequest, in channels.EmailInput) (*mcp.CallToolResult, map[string]any, error) {
			p, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok {
				return nil, nil, errors.New("account credential required")
			}
			if !allowed(ctx, "messages:send") {
				return nil, nil, errors.New("messages:send scope required")
			}
			var err error
			var result map[string]any

			if mediaStore.DB != nil {
				key := in.RequestKey
				in.RequestKey = ""
				result, err = (channels.Outbox{DB: mediaStore.DB, Registry: registry}).Submit(ctx, p.Account, key, "email.send", in)
			} else {
				result, err = registry.SendEmailMessage(ctx, p.Account, in)
			}
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		})

		if mediaStore.DB != nil {
			mcp.AddTool(server, &mcp.Tool{Name: "channels.deliveries", Description: "Read the account's durable Telegram and email delivery journal; uncertain sends are never automatically replayed."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
				RequestKey string `json:"request_key,omitempty"`
				After      int64  `json:"after,omitempty"`
				CancelID   int64  `json:"cancel_id,omitempty"`
			}) (*mcp.CallToolResult, map[string]any, error) {
				p, ok := ctx.Value(principalKey{}).(auth.Principal)
				if !ok || !allowed(ctx, "messages:read") {
					return nil, nil, errors.New("messages:read required")
				}
				queue := channels.Outbox{DB: mediaStore.DB, Registry: registry}
				if in.RequestKey != "" {
					if in.After != 0 || in.CancelID != 0 {
						return nil, nil, errors.New("delivery lookup cannot include pagination or cancellation")
					}
					row, err := queue.Lookup(ctx, p.Account, in.RequestKey)
					if err != nil {
						return nil, nil, err
					}
					rows := []map[string]any{}
					if row != nil {
						rows = append(rows, row)
					}
					return nil, map[string]any{"deliveries": rows}, nil
				}
				if in.CancelID > 0 {
					if !allowed(ctx, "messages:send") {
						return nil, nil, errors.New("messages:send required")
					}
					if err := queue.Cancel(ctx, p.Account, in.CancelID); err != nil {
						return nil, nil, err
					}
				}
				rows, err := queue.List(ctx, p.Account, in.After)
				if err != nil {
					return nil, nil, err
				}
				more := len(rows) > 50
				if more {
					rows = rows[:50]
				}
				return nil, map[string]any{"deliveries": rows, "more": more}, nil
			})
			mcp.AddTool(server, &mcp.Tool{Name: "channels.resolve", Description: "Record an externally verified outcome for an uncertain delivery; never resends."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
				ID      int64  `json:"id"`
				Outcome string `json:"outcome"`
				Note    string `json:"note"`
			}) (*mcp.CallToolResult, map[string]any, error) {
				p, ok := ctx.Value(principalKey{}).(auth.Principal)
				if !ok || !allowed(ctx, "messages:send") || !allowed(ctx, "messages:read") {
					return nil, nil, errors.New("messages:read and messages:send required")
				}
				queue := channels.Outbox{DB: mediaStore.DB, Registry: registry}
				if err := queue.Resolve(ctx, p.Account, in.ID, in.Outcome, in.Note); err != nil {
					return nil, nil, err
				}
				return nil, map[string]any{"resolved": true}, nil
			})

		}
	}
	// Explicit host validation supports a trusted HTTPS reverse proxy while
	// retaining DNS-rebinding protection for direct loopback use.
	publicHost := ""
	if raw := os.Getenv("RELAY_PUBLIC_ORIGIN"); raw != "" {
		origin, err := url.Parse(raw)
		if err != nil {
			return nil, errors.New("invalid public origin")
		}
		localHTTP := origin.Scheme == "http" && (origin.Hostname() == "localhost" || origin.Hostname() == "127.0.0.1" || origin.Hostname() == "::1")
		if (origin.Scheme != "https" && !localHTTP) || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
			return nil, errors.New("invalid public origin")
		}
		publicHost = origin.Host
	}
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname := r.Host
		if host, _, err := net.SplitHostPort(hostname); err == nil {
			hostname = host
		}
		ip := net.ParseIP(strings.Trim(hostname, "[]"))
		local := hostname == "localhost" || (ip != nil && ip.IsLoopback())
		if !local && (publicHost == "" || r.Host != publicHost) {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		// Browser access is denied; business tools require an account credential.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origin not permitted", http.StatusForbidden)
			return
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		authorized := verify == nil && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) == 1
		if verify == nil && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && wa != nil {
			if account, ok := wa.Authenticate(supplied); ok {
				authorized = true
				r = r.WithContext(context.WithValue(r.Context(), accountKey{}, account))
			}
		}
		if verify != nil && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			c, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			p, err := verify(c, supplied)
			cancel()
			if err == nil {
				authorized = true
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
				if wa != nil {
					for _, a := range wa.AccountsSnapshot() {
						if a.ID == p.Account {
							r = r.WithContext(context.WithValue(r.Context(), accountKey{}, a))
							break
						}
					}
				}
			}
		}
		if !authorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// A single bounded group JPEG (5 MiB) is base64 encoded inside MCP.
		// Other operations retain their smaller adapter-specific payload limits.
		r.Body = http.MaxBytesReader(w, r.Body, 7<<20)
		transport.ServeHTTP(w, r)
	}), nil
}
