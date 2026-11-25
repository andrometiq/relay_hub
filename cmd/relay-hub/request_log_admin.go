package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type requestLogRow struct {
	ID            int64     `json:"id"`
	OccurredAt    time.Time `json:"occurred_at"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Route         *string   `json:"route"`
	Status        int       `json:"status"`
	DurationMS    float32   `json:"duration_ms"`
	RequestBytes  int64     `json:"request_bytes"`
	ResponseBytes int64     `json:"response_bytes"`
	RemoteIP      *string   `json:"remote_ip"`
	UserAgent     string    `json:"user_agent"`
	SiteID        *string   `json:"site_id"`
	Account       *string   `json:"account"`
	RequestID     string    `json:"request_id"`
	Error         *string   `json:"error"`
}

const requestLogReadTimeout = 5 * time.Second

// requestLogAdmin pages the Hub request log for admins, newest first.
type requestLogAdmin struct{ db *pgxpool.Pool }

func (a requestLogAdmin) register(mux *http.ServeMux, protect func(http.Handler) http.Handler, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.Handle("GET /api/request-log", protect(admin(a.list)))
}

// list filters by status, site and an RFC 3339 from/to range. before is the
// last id of the previous page.
func (a requestLogAdmin) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	number := func(key string, low, high int64) (int64, bool) {
		raw := query.Get(key)
		if raw == "" {
			return 0, true
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		return n, err == nil && n >= low && n <= high
	}
	moment := func(key string) (*time.Time, bool) {
		raw := query.Get(key)
		if raw == "" {
			return nil, true
		}
		t, err := time.Parse(time.RFC3339, raw)
		return &t, err == nil
	}
	before, beforeOK := number("before", 1, 1<<62)
	status, statusOK := number("status", 100, 599)
	limit, limitOK := number("limit", 1, 200)
	from, fromOK := moment("from")
	to, toOK := moment("to")
	site := query.Get("site")
	if !beforeOK || !statusOK || !limitOK || !fromOK || !toOK || len(site) > 140 {
		http.Error(w, "invalid filter", http.StatusBadRequest)
		return
	}
	if limit == 0 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestLogReadTimeout)
	defer cancel()
	rows, err := a.db.Query(ctx, `SELECT id,occurred_at,method,path,route,status,duration_ms,request_bytes,response_bytes,host(remote_ip),user_agent,site_id,account,request_id,error
 FROM relay_request_log WHERE ($1::bigint=0 OR id<$1) AND ($2::int=0 OR status=$2) AND ($3='' OR site_id=$3)
 AND ($4::timestamptz IS NULL OR occurred_at>=$4) AND ($5::timestamptz IS NULL OR occurred_at<$5) ORDER BY id DESC LIMIT $6`,
		before, status, site, from, to, limit+1)
	if err != nil {
		storeFailure(w, err)
		return
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[requestLogRow])
	if err != nil {
		storeFailure(w, err)
		return
	}
	more := len(entries) > int(limit)
	if more {
		entries = entries[:limit]
	}
	writeJSON(w, map[string]any{"entries": entries, "more": more})
}
