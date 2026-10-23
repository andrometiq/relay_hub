package main

import (
	"encoding/json"
	"net/http"

	"github.com/andrometiq/relay_hub/internal/auth"
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
		var result struct {
			Webhook     string `json:"webhook_received_at"`
			Callback    string `json:"core_confirmed_at"`
			Pending     int64  `json:"callback_pending"`
			Failed      int64  `json:"callback_failed"`
			Quarantined int64  `json:"callback_quarantined"`
		}
		err = db.QueryRow(r.Context(), `SELECT
   COALESCE((SELECT max(created_at)::text FROM relay_whatsapp_events WHERE account=$1),''),
   COALESCE(max(d.updated_at) FILTER (WHERE d.acknowledged)::text,''),
   count(*) FILTER (WHERE NOT d.acknowledged),
   count(*) FILTER (WHERE NOT d.acknowledged AND d.failures>0),
   count(*) FILTER (WHERE d.quarantined)
   FROM relay_whatsapp_callback_delivery d JOIN relay_whatsapp_events e ON e.id=d.event_id WHERE e.account=$1`, account).
			Scan(&result.Webhook, &result.Callback, &result.Pending, &result.Failed, &result.Quarantined)
		if err != nil {
			http.Error(w, "account health unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(result)
	})
}
