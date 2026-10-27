package hubmcp

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
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

func mediaFailure(err error) (*mcp.CallToolResult, map[string]any, error) {
	var rejected *whatsapp.MediaError
	if !errors.As(err, &rejected) {
		return nil, nil, errors.New("media download unavailable")
	}
	result := &mcp.CallToolResult{}
	result.SetError(rejected)
	return result, map[string]any{"provider": "Hub", "error": map[string]any{
		"check": rejected.Check, "outcome_unknown": false,
	}}, nil
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

// sendsMessage reports whether an operation delivers a message and so needs a request key.
func sendsMessage(op string) bool {
	switch op {
	case "messages.flow", "messages.send", "messages.media", "messages.location", "messages.contacts", "messages.react", "templates.send", "groups.send", "groups.media", "groups.template", "groups.pin":
		return true
	}
	return false
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
		db := mediaStores[0].DB
		siteTool := func(ctx context.Context, r *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, map[string]any, error) {
			principal, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok {
				return nil, nil, errors.New("site credential required")
			}
			var site string
			var enabled bool
			if e := db.QueryRow(ctx, `SELECT origin,enabled FROM relay_sites WHERE site_id=$1`, principal.Site).Scan(&site, &enabled); e != nil || !enabled {
				return nil, nil, errors.New("account unavailable")
			}
			list, err := siteChannels(ctx, db, principal.Site)
			if err != nil {
				return nil, nil, errors.New("channels unavailable")
			}
			return nil, map[string]any{"account": principal.Site, "site": site, "enabled": enabled, "channels": list}, nil
		}
		mcp.AddTool(server, &mcp.Tool{Name: "site.get", Description: "Read this token's site and its channels; no credentials are returned."}, siteTool)
		mcp.AddTool(server, &mcp.Tool{Name: "account.get", Description: "Alias of site.get for older clients."}, siteTool)
		mcp.AddTool(server, &mcp.Tool{Name: "channels.list", Description: "List the WhatsApp numbers, Telegram bots and mailboxes this site uses. Pass a key as the channel argument of a send when the site has several channels of one type."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, map[string]any, error) {
			principal, ok := ctx.Value(principalKey{}).(auth.Principal)
			if !ok {
				return nil, nil, errors.New("site credential required")
			}
			list, err := siteChannels(ctx, db, principal.Site)
			if err != nil {
				return nil, nil, errors.New("channels unavailable")
			}
			return nil, map[string]any{"channels": list}, nil
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
			Channel string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			account, err := whatsappNumber(ctx, wa, in.Channel)
			if err != nil {
				return mediaFailure(&whatsapp.MediaError{Check: "account"})
			}
			file, err := wa.DownloadAttachment(ctx, account, in.MediaID)
			if err != nil {
				return mediaFailure(err)
			}
			return mediaResult(file.Data, file.MIME, file.Filename, whatsapp.MaxDownloadBytes)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.media", Description: "Read a bounded sticker already received by this account. Provider credentials and download URLs stay in Hub."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			MediaID string `json:"media_id"`
			Channel string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			account, err := whatsappNumber(ctx, wa, in.Channel)
			if err != nil {
				return mediaFailure(&whatsapp.MediaError{Check: "account"})
			}
			file, err := wa.DownloadMedia(ctx, account, in.MediaID, 2<<20)
			if err != nil {
				return mediaFailure(err)
			}
			return mediaResult(file.Data, file.MIME, file.Filename)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.execute", Description: "Account-scoped WhatsApp messages and templates. Mutations are not automatically retried."}, func(ctx context.Context, r *mcp.CallToolRequest, routed whatsappInput) (*mcp.CallToolResult, map[string]any, error) {
			input := routed.Input
			account, err := whatsappNumber(ctx, wa, routed.Channel)
			if err != nil {
				return nil, nil, err
			}
			if strings.HasPrefix(input.Operation, "groups.") || strings.HasPrefix(input.Operation, "calls.") {
				return nil, nil, errors.New("WhatsApp groups and calls are disabled in v1")
			}
			// Uploads and read receipts send no message, so they run directly.
			direct := input.Operation == "messages.media_upload" || input.Operation == "messages.read"
			if sendsMessage(input.Operation) && input.RequestKey == "" {
				return nil, nil, errors.New("request_key required for sends")
			}
			if input.RequestKey != "" && !direct {
				if wa.Outbox == nil {
					return nil, nil, errors.New("durable delivery unavailable")
				}
				result, err := wa.Outbox.Submit(ctx, account, input)
				if err != nil {
					return whatsappFailure(err)
				}
				return nil, result, nil
			}
			account.Groups, account.Calls = false, false
			result, err := wa.Execute(ctx, account, input)
			if err != nil {
				return whatsappFailure(err)
			}
			return nil, result, nil
		})
	}
	if wa != nil && wa.Outbox != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.send", Description: "Durably queue one message using a stable request key."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			Message whatsapp.Submission `json:"message"`
			Channel string              `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			a, err := whatsappNumber(ctx, wa, in.Channel)
			if err != nil {
				return nil, nil, err
			}
			if strings.HasPrefix(in.Message.Message.Operation, "groups.") || strings.HasPrefix(in.Message.Message.Operation, "calls.") {
				return nil, nil, errors.New("only individual WhatsApp sends are available in v1")
			}
			rows, err := wa.Outbox.Enqueue(ctx, a, []whatsapp.Submission{in.Message})
			if err != nil {
				return nil, nil, errors.New("send not confirmed; check input and retry with the same key")
			}
			return nil, map[string]any{"deliveries": rows}, nil
		})

		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.bulk", Description: "Durably queue 1-50 template, text or media sends with stable request keys. Bulk media uses media_id or media_url. Invalid recipients are rejected individually; queued reports the account queue depth for backpressure. Results arrive in coalesced bulk update callbacks."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			Messages []whatsapp.Submission `json:"messages"`
			Channel  string                `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			a, err := whatsappNumber(ctx, wa, in.Channel)
			if err != nil {
				return nil, nil, err
			}
			rows, queued, err := wa.Outbox.EnqueueBulk(ctx, a, in.Messages)
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"deliveries": rows, "queued": queued, "limit": whatsapp.BulkQueueLimit, "messages_per_second": a.SendRate()}, nil
		})

		mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.resolve", Description: "Record a verified uncertain delivery outcome without resending."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			ID         int64  `json:"id"`
			Outcome    string `json:"outcome"`
			Note       string `json:"note"`
			ProviderID string `json:"provider_id,omitempty"`
			Channel    string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			a, err := whatsappScope(ctx, wa, in.Channel)
			if err != nil {
				return nil, nil, err
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
			Channel    string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			a, err := whatsappScope(ctx, wa, in.Channel)
			if err != nil {
				return nil, nil, err
			}
			if in.CancelID != "" {
				err := wa.Outbox.Cancel(ctx, a, in.CancelID)
				if err != nil {
					return nil, nil, errors.New("queued delivery could not be cancelled")
				}
				return nil, map[string]any{"cancelled": err == nil}, err
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
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.typing", Description: "Briefly indicate typing using the account bot. Ephemeral; never queued or replayed."}, func(ctx context.Context, r *mcp.CallToolRequest, routed telegramTyping) (*mcp.CallToolResult, map[string]any, error) {
			bot, err := channelKey(ctx, registry, "Telegram", routed.Channel)
			if err != nil {
				return nil, nil, err
			}
			result, err := registry.TelegramTyping(ctx, bot, routed.TypingInput)
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.message", Description: "Send text, sticker, reaction, location or contact using the account bot. Replies preserve their group and topic. Mutations are sent once."}, func(ctx context.Context, r *mcp.CallToolRequest, routed telegramMessage) (*mcp.CallToolResult, map[string]any, error) {
			bot, err := channelKey(ctx, registry, "Telegram", routed.Channel)
			if err != nil {
				return nil, nil, err
			}
			in := routed.MessageInput
			var result map[string]any
			if mediaStore.DB != nil {
				key := in.RequestKey
				in.RequestKey = ""
				result, err = (channels.Outbox{DB: mediaStore.DB, Registry: registry}).Submit(ctx, bot, key, "telegram.message", in)
			} else {
				result, err = registry.TelegramMessage(ctx, bot, in)
			}
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, nil
		})

		mcp.AddTool(server, &mcp.Tool{Name: "telegram.attachment", Description: "Read an account-journaled Telegram attachment up to 4 MiB without exposing provider credentials."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			FileID  string `json:"file_id"`
			Channel string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			bot, err := channelKey(ctx, registry, "Telegram", in.Channel)
			if err != nil {
				return nil, nil, err
			}
			known, err := mediaStore.TelegramMediaKnown(ctx, bot, in.FileID)
			if err != nil || !known {
				return nil, nil, errors.New("attachment is unavailable for this account")
			}
			file, err := registry.TelegramMedia(ctx, bot, in.FileID)
			if err != nil {
				return nil, nil, errors.New("attachment is unavailable for this account")
			}
			return mediaResult(file.Data, file.ContentType, file.Filename, telegram.MaxMediaBytes)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.media", Description: "Read a bounded sticker already received by this account. Provider credentials and download URLs stay in Hub."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			FileID  string `json:"file_id"`
			Channel string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			bot, err := channelKey(ctx, registry, "Telegram", in.Channel)
			if err != nil {
				return nil, nil, err
			}
			known, err := mediaStore.TelegramStickerKnown(ctx, bot, in.FileID)
			if err != nil || !known {
				return nil, nil, errors.New("sticker is unavailable for this account")
			}
			file, err := registry.TelegramSticker(ctx, bot, in.FileID)
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
			Channel    string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			bot, err := channelKey(ctx, registry, "Telegram", in.Channel)
			if err != nil {
				return nil, nil, err
			}
			var result map[string]any
			message := telegram.MessageInput{Operation: "text", ChatID: in.ChatID, Text: in.Text, ReplyTo: in.ReplyTo, ThreadID: in.ThreadID}
			if mediaStore.DB != nil {
				result, err = (channels.Outbox{DB: mediaStore.DB, Registry: registry}).Submit(ctx, bot, in.RequestKey, "telegram.message", message)
			} else {
				result, err = registry.TelegramMessage(ctx, bot, message)
			}
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "telegram.groups", Description: "Manage Telegram groups, members, invitations and forum topics through the account-scoped bot. Requires the corresponding Telegram administrator permissions; mutations are not automatically retried."}, func(ctx context.Context, r *mcp.CallToolRequest, routed telegramGroup) (*mcp.CallToolResult, map[string]any, error) {
			in := routed.GroupInput
			bot, err := channelKey(ctx, registry, "Telegram", routed.Channel)
			if err != nil {
				return nil, nil, err
			}
			result, err := registry.TelegramGroup(ctx, bot, in)
			if err != nil {
				return telegramFailure(err)
			}
			return nil, result, err
		})
		mcp.AddTool(server, &mcp.Tool{Name: "email.attachment", Description: "Read an account-scoped mailbox attachment up to 4 MiB."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			ProviderID string `json:"provider_id"`
			Index      int    `json:"index"`
			Channel    string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			if mediaStore.DB == nil || in.Index < 0 || in.Index > 7 || len(in.ProviderID) > 300 {
				return nil, nil, errors.New("attachment unavailable")
			}
			mailbox, err := channelKey(ctx, registry, "Email", in.Channel)
			if err != nil {
				return nil, nil, err
			}
			var data []byte
			var mime, name string
			if err := mediaStore.DB.QueryRow(ctx, `SELECT content,content_type,filename FROM relay_email_attachments WHERE account=$1 AND provider_id=$2 AND slot=$3`, mailbox, in.ProviderID, in.Index).Scan(&data, &mime, &name); err != nil {
				return nil, nil, errors.New("attachment unavailable")
			}
			return mediaResult(data, mime, name, 4<<20)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "email.mailbox", Description: "Read mailbox sync status and the twenty latest rejected messages for this account."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
			Channel string `json:"channel,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			if mediaStore.DB == nil {
				return nil, nil, errors.New("mailbox status unavailable")
			}
			mailbox, err := channelKey(ctx, registry, "Email", in.Channel)
			if err != nil {
				return nil, nil, err
			}
			rows, err := mediaStore.DB.Query(ctx, `SELECT jsonb_build_object('mailbox',mailbox,'last_sync',last_sync,'error',last_error,'last_uid',last_uid) FROM relay_mailbox_cursor WHERE account=$1 ORDER BY last_sync DESC NULLS LAST LIMIT 10`, mailbox)
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
			rejected, err := mediaStore.DB.Query(ctx, `SELECT jsonb_build_object('uid',uid,'reason',reason,'created_at',created_at) FROM relay_mailbox_rejections WHERE account=$1 ORDER BY created_at DESC LIMIT 20`, mailbox)
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
		mcp.AddTool(server, &mcp.Tool{Name: "email.send", Description: "Send an email through the account-scoped SMTP adapter."}, func(ctx context.Context, r *mcp.CallToolRequest, routed emailMessage) (*mcp.CallToolResult, map[string]any, error) {
			mailbox, err := channelKey(ctx, registry, "Email", routed.Channel)
			if err != nil {
				return nil, nil, err
			}
			in := routed.EmailInput
			var result map[string]any

			if mediaStore.DB != nil {
				key := in.RequestKey
				in.RequestKey = ""
				result, err = (channels.Outbox{DB: mediaStore.DB, Registry: registry}).Submit(ctx, mailbox, key, "email.send", in)
			} else {
				result, err = registry.SendEmailMessage(ctx, mailbox, in)
			}
			if err != nil {
				return emailFailure(err)
			}
			return nil, result, nil
		})

		if mediaStore.DB != nil {
			mcp.AddTool(server, &mcp.Tool{Name: "channels.deliveries", Description: "Read the account's durable Telegram and email delivery journal; uncertain sends are never automatically replayed."}, func(ctx context.Context, r *mcp.CallToolRequest, in struct {
				RequestKey string `json:"request_key,omitempty"`
				After      int64  `json:"after,omitempty"`
				CancelID   int64  `json:"cancel_id,omitempty"`
				Channel    string `json:"channel,omitempty"`
			}) (*mcp.CallToolResult, map[string]any, error) {
				key, site, err := deliveryKey(ctx, registry, in.Channel)
				if err != nil {
					return nil, nil, err
				}
				queue := channels.Outbox{DB: mediaStore.DB, Registry: registry, Site: site}
				if in.RequestKey != "" {
					if in.After != 0 || in.CancelID != 0 {
						return nil, nil, errors.New("delivery lookup cannot include pagination or cancellation")
					}
					row, err := queue.Lookup(ctx, key, in.RequestKey)
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
					if err := queue.Cancel(ctx, key, in.CancelID); err != nil {
						return nil, nil, err
					}
				}
				rows, err := queue.List(ctx, key, in.After)
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
				ID         int64  `json:"id"`
				Outcome    string `json:"outcome"`
				Note       string `json:"note"`
				ProviderID string `json:"provider_id,omitempty"`
				ChatID     int64  `json:"chat_id,omitempty"`
				Channel    string `json:"channel,omitempty"`
			}) (*mcp.CallToolResult, map[string]any, error) {
				key, site, err := deliveryKey(ctx, registry, in.Channel)
				if err != nil {
					return nil, nil, err
				}
				queue := channels.Outbox{DB: mediaStore.DB, Registry: registry, Site: site}
				if err := queue.Resolve(ctx, key, in.ID, in.Outcome, in.Note, channels.Resolution{ProviderID: in.ProviderID, ChatID: in.ChatID}); err != nil {
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

// whatsappFailure keeps Meta's code and safe user text in the tool error, so
// Core can show why a template or send was refused.
func whatsappFailure(err error) (*mcp.CallToolResult, map[string]any, error) {
	var provider *whatsapp.ProviderError
	if !errors.As(err, &provider) {
		return nil, nil, err
	}
	result := &mcp.CallToolResult{}
	result.SetError(err)
	return result, map[string]any{"provider": "WhatsApp", "error": provider}, nil
}

func emailFailure(err error) (*mcp.CallToolResult, map[string]any, error) {
	var provider *relayemail.SendError
	if !errors.As(err, &provider) {
		return nil, nil, err
	}
	result := &mcp.CallToolResult{}
	result.SetError(provider)
	return result, map[string]any{"provider": "Email", "error": provider}, nil
}
