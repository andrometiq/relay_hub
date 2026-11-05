package main

import (
	"encoding/json"
	"github.com/andrometiq/relay_hub/internal/channels"
	"net/http"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5/pgxpool"
)

// accountHealth reports observed delivery evidence independently from credentials.
// It never contacts Meta or changes the callback queue.
func accountHealth(db *pgxpool.Pool, session func(*http.Request) (auth.User, error)) http.Handler {
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
		account := r.URL.Query().Get("account")
		if !deliveryAccount.MatchString(account) {
			http.Error(w, "invalid account", 400)
			return
		}
		channel := r.URL.Query().Get("channel")
		if channel != "" && channel != "WhatsApp" && channel != "Telegram" && channel != "Email" {
			http.Error(w, "invalid channel", 400)
			return
		}
		if channel == "Telegram" || channel == "Email" {
			result, err := channels.AccountHealth(r.Context(), db, account, channel)
			if err != nil {
				http.Error(w, "account health unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(result)
			return
		}
		result, err := whatsapp.ReadDeliveryHealth(r.Context(), db, account)
		if err != nil {
			http.Error(w, "account health unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(result)
	})
}
