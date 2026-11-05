package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

// Leave time to encode and flush diagnostics before the server write deadline.
const accountConnectionTimeout = 20 * time.Second
const httpWriteTimeout = 30 * time.Second

type accountConnections interface {
	TelegramUsername(context.Context, string) (string, error)
	TelegramConnection(context.Context, string, string, string, bool) (telegram.WebhookHealth, error)
	TestEmailConnection(context.Context, string) (channels.EmailConnection, error)
	WebhookPath(provider, key string) string
}

// Only persisted, active account credentials are tested. Callers cannot supply
// arbitrary provider targets, tokens or callback URLs to this endpoint.
// connectionStatus stores the result of a check on the channel, so pages show it later.
type connectionStatus func(ctx context.Context, channel, key string, values map[string]any) error

func accountConnection(registry accountConnections, session func(*http.Request) (auth.User, error), origin, publicURL, telegramMaster string, record connectionStatus) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := session(r)
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		if user.Role != "admin" {
			http.Error(w, "forbidden", 403)
			return
		}
		account, channel := r.PathValue("account"), r.PathValue("channel")
		if !deliveryAccount.MatchString(account) || (channel != "telegram" && channel != "email") {
			http.Error(w, "invalid account or channel", 400)
			return
		}
		repair := false
		if r.Method == http.MethodPost {
			cookie, err := r.Cookie(auth.Cookie)
			if err != nil || r.Header.Get("Origin") != origin || !hmac.Equal([]byte(r.Header.Get("X-Relay-CSRF")), []byte(auth.CSRF(cookie.Value))) {
				http.Error(w, "invalid request origin", 403)
				return
			}
			var in struct {
				Action string `json:"action"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || (in.Action != "test" && !(channel == "telegram" && in.Action == "repair")) {
				http.Error(w, "invalid action", 400)
				return
			}
			repair = in.Action == "repair"
		} else if r.Method != http.MethodGet || channel != "telegram" {
			http.Error(w, "method not allowed", 405)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), accountConnectionTimeout)
		defer cancel()
		var result any
		if channel == "telegram" {
			result, err = checkTelegram(ctx, registry, record, account, publicURL, telegramMaster, repair)
		} else {
			var mail channels.EmailConnection
			mail, err = registry.TestEmailConnection(ctx, account)
			result = mail
			status := mail.Status()
			if err != nil {
				status = "failed"
			}
			recordStatus(ctx, record, "Email", account, "connection_status", status)
			if err == nil && status == "failed" {
				err = &connectionFailure{Status: 422, Message: mail.Failure(), Detail: map[string]string{"smtp": mail.SMTP, "imap": mail.IMAP}}
			}
		}
		var failure *connectionFailure
		if errors.As(err, &failure) {
			body := map[string]string{"message": failure.Message}
			maps.Copy(body, failure.Detail)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(failure.Status)
			json.NewEncoder(w).Encode(body)
			return
		}
		if err != nil {
			http.Error(w, "Connection check failed. Check saved settings and runtime activation, then retry.", 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(result)
	})
}

// connectionFailure is a check that ran and failed for a reason the admin can fix; its message and the
// provider states in Detail are safe to show.
type connectionFailure struct {
	Status  int
	Message string
	Detail  map[string]string
}

func (f *connectionFailure) Error() string { return f.Message }

// checkTelegram runs the shared Telegram check and keeps its result on the channel.
func checkTelegram(ctx context.Context, registry accountConnections, record connectionStatus, account, publicURL, master string, repair bool) (channels.TelegramCheck, error) {
	result, err := channels.CheckTelegram(ctx, registry, account, publicURL, master, repair)
	if result.Username != "" {
		recordStatus(ctx, record, "Telegram", account, "username", result.Username)
	}
	recordStatus(ctx, record, "Telegram", account, "webhook_status", result.WebhookStatus)
	if errors.Is(err, channels.ErrTelegramToken) {
		return result, &connectionFailure{Status: 422, Message: "Telegram did not accept the bot token. Copy the token from @BotFather again."}
	}
	return result, err
}

// recordStatus keeps a check result; a failed write only loses the display value.
func recordStatus(ctx context.Context, record connectionStatus, channel, key, field, value string) {
	if record == nil {
		return
	}
	if err := record(ctx, channel, key, map[string]any{field: value, "checked_at": time.Now().UTC().Format(time.RFC3339)}); err != nil {
		log.Printf("connection status of %s %s was not stored: %v", channel, key, err)
	}
}
