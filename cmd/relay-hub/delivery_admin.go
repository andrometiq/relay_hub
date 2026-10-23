package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

type deliveryChange struct {
	Account    string `json:"account"`
	Channel    string `json:"channel"`
	Action     string `json:"action"`
	ID         string `json:"id"`
	Outcome    string `json:"outcome"`
	Note       string `json:"note"`
	ProviderID string `json:"provider_id"`
	ChatID     int64  `json:"chat_id"`
}
type deliveryPage struct {
	Rows any  `json:"deliveries"`
	More bool `json:"more"`
}
type deliveryBackend interface {
	List(context.Context, string, string, int64) (deliveryPage, error)
	Change(context.Context, deliveryChange) error
}
type hubDeliveryBackend struct {
	wa       *whatsapp.Outbox
	channels channels.Outbox
}

func (b hubDeliveryBackend) List(ctx context.Context, account, channel string, after int64) (deliveryPage, error) {
	if channel == "WhatsApp" {
		if b.wa == nil {
			return deliveryPage{}, errors.New("WhatsApp delivery unavailable")
		}
		rows, err := b.wa.List(ctx, whatsapp.Account{ID: account}, strconv.FormatInt(after, 10))
		return deliveryPage{rows, len(rows) == 50}, err
	}
	rows, err := b.channels.List(ctx, account, after)
	more := len(rows) > 50
	if more {
		rows = rows[:50]
	}
	return deliveryPage{rows, more}, err
}
func (b hubDeliveryBackend) Change(ctx context.Context, in deliveryChange) error {
	id, err := strconv.ParseInt(in.ID, 10, 64)
	if err != nil || id < 1 {
		return errors.New("invalid delivery")
	}
	if in.Channel == "WhatsApp" {
		if b.wa == nil {
			return errors.New("WhatsApp delivery unavailable")
		}
		if in.Action == "cancel" {
			return b.wa.Cancel(ctx, whatsapp.Account{ID: in.Account}, in.ID)
		}
		return b.wa.Resolve(ctx, whatsapp.Account{ID: in.Account}, id, in.Outcome, in.Note, in.ProviderID)
	}
	if in.Action == "cancel" {
		return b.channels.Cancel(ctx, in.Account, id)
	}
	return b.channels.Resolve(ctx, in.Account, id, in.Outcome, in.Note, channels.Resolution{ProviderID: in.ProviderID, ChatID: in.ChatID})
}

var deliveryAccount = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func deliveryAdmin(backend deliveryBackend, session func(*http.Request) (auth.User, error), origin string) http.Handler {
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
		account, channel := r.URL.Query().Get("account"), r.URL.Query().Get("channel")
		if r.Method == http.MethodPost {
			cookie, err := r.Cookie(auth.Cookie)
			if err != nil || !hmac.Equal([]byte(r.Header.Get("X-Relay-CSRF")), []byte(auth.CSRF(cookie.Value))) || r.Header.Get("Origin") != origin {
				http.Error(w, "invalid request origin", 403)
				return
			}
			var in deliveryChange
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid request", 400)
				return
			}
			account, channel = in.Account, in.Channel
			if !deliveryAccount.MatchString(account) || (channel != "WhatsApp" && channel != "Telegram and email") || (in.Action != "cancel" && in.Action != "resolve") {
				http.Error(w, "invalid delivery action", 400)
				return
			}
			if err := backend.Change(r.Context(), in); err != nil {
				http.Error(w, "delivery cannot be changed; refresh its current state", 400)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		if !deliveryAccount.MatchString(account) || (channel != "WhatsApp" && channel != "Telegram and email") {
			http.Error(w, "select an account and delivery channel", 400)
			return
		}
		after := int64(0)
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || after < 0 {
				http.Error(w, "invalid cursor", 400)
				return
			}
		}
		result, err := backend.List(r.Context(), account, channel, after)
		if err != nil {
			http.Error(w, "delivery records unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(result)
	})
}
