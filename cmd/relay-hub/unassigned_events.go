package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// unassignedEvent is a stored event of a number, bot or mailbox without a site.
// Account is the channel key, or empty for an unknown WhatsApp number.
type unassignedEvent struct {
	ID        int64     `json:"id"`
	Channel   string    `json:"channel"`
	Account   string    `json:"account"`
	Container string    `json:"container,omitempty"`
	Created   time.Time `json:"created_at"`
	Payload   any       `json:"payload"`
}

type eventReplayer func(context.Context, string, int64) error

// unassignedEvents lists and replays events kept while their channel had no site.
type unassignedEvents struct {
	db     *pgxpool.Pool
	replay eventReplayer
}

func (u unassignedEvents) register(mux *http.ServeMux, protect func(http.Handler) http.Handler, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.Handle("GET /api/unassigned-events", protect(admin(u.list)))
	mux.Handle("POST /api/unassigned-events/replay", protect(admin(u.replayEvents)))
}

func (u unassignedEvents) list(w http.ResponseWriter, r *http.Request) {
	rows, err := u.db.Query(r.Context(), `SELECT * FROM (
 SELECT id,'WhatsApp',account,coalesce(container_id,''),created_at,payload FROM relay_whatsapp_events
 WHERE site_id IS NULL AND replayed_at IS NULL ORDER BY id DESC LIMIT 100) w
 UNION ALL SELECT * FROM (
 SELECT id,channel,account,'',occurred_at,jsonb_build_object('sender',sender,'subject',subject,'body',left(body,500)) FROM relay_channel_events
 WHERE site_id IS NULL AND replayed_at IS NULL ORDER BY id DESC LIMIT 100) c ORDER BY 5 DESC`)
	if err != nil {
		storeFailure(w, err)
		return
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (unassignedEvent, error) {
		var event unassignedEvent
		err := row.Scan(&event.ID, &event.Channel, &event.Account, &event.Container, &event.Created, &event.Payload)
		return event, err
	})
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

func (u unassignedEvents) replayEvents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channel string  `json:"channel"`
		IDs     []int64 `json:"ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > 100 {
		http.Error(w, "select 1 to 100 events", http.StatusBadRequest)
		return
	}
	replayed := 0
	for _, id := range in.IDs {
		if err := u.replay(r.Context(), in.Channel, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		replayed++
	}
	writeJSON(w, map[string]int{"replayed": replayed})
}
