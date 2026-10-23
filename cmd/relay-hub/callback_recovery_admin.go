package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"strconv"
)

func callbackRetryAdmin(retry func(context.Context, string, string, int64) error, session func(*http.Request) (auth.User, error), origin string, recoverWhatsApp ...func(context.Context, string, string, int64, int64, string, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := session(r)
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		if u.Role != "admin" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		cookie, err := r.Cookie(auth.Cookie)
		if err != nil || r.Header.Get("Origin") != origin || !hmac.Equal([]byte(r.Header.Get("X-Relay-CSRF")), []byte(auth.CSRF(cookie.Value))) {
			http.Error(w, "invalid request origin", 403)
			return
		}
		var in struct {
			Account string `json:"account"`
			Channel string `json:"channel"`
			ID      string `json:"id"`
			Lane    string `json:"lane"`
			Action  string `json:"action"`
			Note    string `json:"note"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
		id, err := strconv.ParseInt(in.ID, 10, 64)
		if err != nil || id < 1 || !deliveryAccount.MatchString(in.Account) || (in.Channel != "Telegram" && in.Channel != "Email" && in.Channel != "WhatsApp") {
			http.Error(w, "invalid callback", 400)
			return
		}
		if in.Channel == "WhatsApp" {
			if len(recoverWhatsApp) == 0 {
				http.Error(w, "recovery unavailable", 503)
				return
			}
			err = recoverWhatsApp[0](r.Context(), in.Account, in.Lane, id, u.ID, in.Action, in.Note)
		} else {
			err = retry(r.Context(), in.Account, in.Channel, id)
		}
		if err != nil {
			http.Error(w, "Callback remains quarantined. Resolve the payload size or delivery failure before retrying.", 409)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func callbackFailuresAdmin(db *pgxpool.Pool, session func(*http.Request) (auth.User, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := session(r)
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		if u.Role != "admin" {
			http.Error(w, "forbidden", 403)
			return
		}
		account, channel := r.URL.Query().Get("account"), r.URL.Query().Get("channel")
		if account == "" || len(account) > 100 || (channel != "Telegram" && channel != "Email" && channel != "WhatsApp") {
			http.Error(w, "invalid filter", 400)
			return
		}
		before := int64(0)
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || before < 1 {
				http.Error(w, "invalid cursor", 400)
				return
			}
		}
		query := `SELECT event_id,reason,created_at::text,'' AS lane,-1 AS position,0 AS failures,'' AS next_attempt,true AS quarantined FROM relay_channel_callback_quarantine WHERE account=$1 AND channel=$2 AND resolved_at IS NULL AND ($3::bigint=0 OR event_id<$3) ORDER BY event_id DESC LIMIT 51`
		if channel == "WhatsApp" {
			query = `SELECT d.event_id,d.error,d.updated_at::text,d.lane,d.error_position,d.failures,d.next_attempt::text,d.quarantined
    FROM relay_whatsapp_callback_delivery d JOIN relay_whatsapp_events e ON e.id=d.event_id
    WHERE e.account=$1 AND $2='WhatsApp' AND NOT d.acknowledged AND (d.failures>0 OR d.quarantined)
    AND ($3::bigint=0 OR d.event_id<$3) ORDER BY d.event_id DESC,d.lane LIMIT 52`
		}
		rows, err := db.Query(r.Context(), query, account, channel, before)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		defer rows.Close()
		type failure struct {
			ID          int64  `json:"id"`
			Reason      string `json:"reason"`
			Created     string `json:"created_at"`
			Lane        string `json:"lane,omitempty"`
			Position    int    `json:"position"`
			Failures    int    `json:"failures"`
			NextAttempt string `json:"next_attempt"`
			Quarantined bool   `json:"quarantined"`
		}
		items := []failure{}
		for rows.Next() {
			var item failure
			if err = rows.Scan(&item.ID, &item.Reason, &item.Created, &item.Lane, &item.Position, &item.Failures, &item.NextAttempt, &item.Quarantined); err != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			items = append(items, item)
		}
		if rows.Err() != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		limit := 50
		// Keep both WhatsApp lanes together when the page ends within an event.
		if len(items) > limit && items[limit-1].ID == items[limit].ID {
			limit++
		}
		more := len(items) > limit
		if more {
			items = items[:limit]
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": items, "more": more})
	})
}
