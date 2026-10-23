package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strconv"
)

type inspectedEvent struct {
	ID       int64  `json:"id"`
	Time     string `json:"time"`
	Kind     string `json:"kind"`
	Preview  string `json:"preview"`
	Callback string `json:"callback"`
}
type eventPage struct {
	Items []inspectedEvent `json:"items"`
	More  bool             `json:"more"`
}
type eventDetail struct {
	Raw       string `json:"raw"`
	Truncated bool   `json:"truncated"`
}
type eventReader interface {
	List(context.Context, string, string, int64) (eventPage, error)
	Detail(context.Context, string, string, int64) (eventDetail, error)
}
type postgresEventReader struct{ db *pgxpool.Pool }

func (b postgresEventReader) List(ctx context.Context, account, channel string, before int64) (eventPage, error) {
	query := `SELECT e.id,e.occurred_at::text,e.direction,left(e.body,240),CASE WHEN d.quarantined THEN 'Quarantined' WHEN d.acknowledged THEN 'Forwarded' WHEN d.failures>0 THEN 'Retrying' ELSE 'Pending' END FROM relay_channel_events e LEFT JOIN relay_channel_callback_delivery d ON d.event_id=e.id WHERE e.account=$1 AND e.channel=$2 AND ($3::bigint=0 OR e.id<$3) ORDER BY e.id DESC LIMIT 51`
	if channel == "WhatsApp" {
		query = `SELECT e.id,e.created_at::text,e.lane,left(COALESCE(e.payload#>>'{entry,0,changes,0,value,messages,0,text,body}',''),240),CASE WHEN d.quarantined THEN 'Quarantined' WHEN d.failed THEN 'Retrying'
 WHEN (e.lane<>'realtime' OR coalesce(d.realtime,false)) AND
 ((e.lane<>'status' AND NOT e.payload @? '$.entry[*].changes[*].value.statuses') OR coalesce(d.status,false)) THEN 'Forwarded' ELSE 'Pending' END
 FROM relay_whatsapp_events e LEFT JOIN LATERAL (
 SELECT bool_or(quarantined) quarantined,bool_or(failures>0 AND NOT acknowledged) failed,
 bool_or(acknowledged AND lane='realtime') realtime,bool_or(acknowledged AND lane='status') status
 FROM relay_whatsapp_callback_delivery WHERE event_id=e.id) d ON true WHERE e.account=$1 AND $2='WhatsApp' AND ($3::bigint=0 OR e.id<$3) ORDER BY e.id DESC LIMIT 51`
	}
	rows, err := b.db.Query(ctx, query, account, channel, before)
	if err != nil {
		return eventPage{}, err
	}
	defer rows.Close()
	result := eventPage{Items: []inspectedEvent{}}
	for rows.Next() {
		var item inspectedEvent
		if err = rows.Scan(&item.ID, &item.Time, &item.Kind, &item.Preview, &item.Callback); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	result.More = len(result.Items) > 50
	if result.More {
		result.Items = result.Items[:50]
	}
	return result, nil
}
func (b postgresEventReader) Detail(ctx context.Context, account, channel string, id int64) (eventDetail, error) {
	// Limit the payload in SQL before transferring it to Go or the browser.
	query := `SELECT left(payload::text,65536),length(payload::text)>65536 FROM relay_channel_events WHERE account=$1 AND channel=$2 AND id=$3`
	if channel == "WhatsApp" {
		query = `SELECT left(payload::text,65536),length(payload::text)>65536 FROM relay_whatsapp_events WHERE account=$1 AND $2='WhatsApp' AND id=$3`
	}
	var result eventDetail
	err := b.db.QueryRow(ctx, query, account, channel, id).Scan(&result.Raw, &result.Truncated)
	return result, err
}
func eventInspector(backend eventReader, session func(*http.Request) (auth.User, error)) http.Handler {
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
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		account, channel := r.URL.Query().Get("account"), r.URL.Query().Get("channel")
		if !deliveryAccount.MatchString(account) || (channel != "WhatsApp" && channel != "Telegram" && channel != "Email") {
			http.Error(w, "select an account and channel", 400)
			return
		}
		parse := func(key string) (int64, error) {
			raw := r.URL.Query().Get(key)
			if raw == "" {
				return 0, nil
			}
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || n < 1 {
				return 0, errors.New("invalid cursor")
			}
			return n, nil
		}
		id, err := parse("id")
		if err != nil {
			http.Error(w, "invalid event", 400)
			return
		}
		before, err := parse("before")
		if err != nil || id > 0 && before > 0 {
			http.Error(w, "invalid cursor", 400)
			return
		}
		var result any
		if id > 0 {
			result, err = backend.Detail(r.Context(), account, channel, id)
		} else {
			result, err = backend.List(r.Context(), account, channel, before)
		}
		if err != nil {
			http.Error(w, "event data unavailable", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(result)
	})
}
