package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func channelEventsAdmin(db *pgxpool.Pool, session func(*http.Request) (auth.User, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, e := session(r)
		if e != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		if u.Role != "admin" {
			http.Error(w, "forbidden", 403)
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		before := int64(0)
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, e = strconv.ParseInt(raw, 10, 64)
			if e != nil || before < 0 {
				http.Error(w, "invalid cursor", 400)
				return
			}
		}
		account := strings.TrimSpace(r.URL.Query().Get("account"))
		channel := strings.TrimSpace(r.URL.Query().Get("channel"))
		if len(account) > 100 || (channel != "" && channel != "Telegram" && channel != "Email") {
			http.Error(w, "invalid filter", 400)
			return
		}
		rows, e := db.Query(r.Context(), `SELECT id,account,channel,direction,provider_id,sender,recipient,subject,body,occurred_at::text,payload,COALESCE((SELECT error FROM relay_channel_callback_delivery d WHERE d.event_id=relay_channel_events.id AND NOT d.acknowledged AND (d.failures>0 OR d.quarantined)),'') FROM relay_channel_events WHERE ($1::bigint=0 OR id<$1) AND ($2='' OR account=$2) AND ($3='' OR channel=$3) ORDER BY id DESC LIMIT $4`, before, account, channel, limit+1)
		if e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		defer rows.Close()
		type item struct {
			ID                                                                                    int64 `json:"id"`
			Account, Channel, Direction, ProviderID, Sender, Recipient, Subject, Body, OccurredAt string
			Payload                                                                               json.RawMessage `json:"payload"`
			CallbackError                                                                         string          `json:"callback_error,omitempty"`
		}
		list := []item{}
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.ID, &v.Account, &v.Channel, &v.Direction, &v.ProviderID, &v.Sender, &v.Recipient, &v.Subject, &v.Body, &v.OccurredAt, &v.Payload, &v.CallbackError); e != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			list = append(list, v)
		}
		if e = rows.Err(); e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		more := len(list) > limit
		if more {
			list = list[:limit]
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"events": list, "more": more})
	})
}
