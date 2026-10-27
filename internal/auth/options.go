package auth

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Options are bounded and permission scoped. The account ID range uses the
// directory index rather than downloading accounts to filter in the browser.
func (h *HTTP) searchOptions(w http.ResponseWriter, r *http.Request, u User) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	kind := r.URL.Query().Get("kind")
	if len(query) > 80 {
		http.Error(w, "Search is too long", 400)
		return
	}
	if kind != "accounts" && kind != "grants" {
		http.Error(w, "Invalid option kind", 400)
		return
	}
	if kind == "accounts" && u.Role != "admin" {
		http.Error(w, "Forbidden", 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	sql := `SELECT account_id FROM relay_accounts WHERE account_id COLLATE "C" >= $1 AND account_id COLLATE "C" < $2 ORDER BY account_id COLLATE "C" LIMIT 31`
	args := []any{query, query + "\U0010ffff"}
	if kind == "grants" {
		sql = `SELECT account_id FROM relay_user_accounts WHERE user_id=$3 AND account_id COLLATE "C" >= $1 AND account_id COLLATE "C" < $2 ORDER BY account_id COLLATE "C" LIMIT 31`
		args = append(args, u.ID)
	}
	rows, err := h.Store.DB.Query(ctx, sql, args...)
	if err != nil {
		http.Error(w, "Search unavailable", 503)
		return
	}
	defer rows.Close()
	ids := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			http.Error(w, "Search unavailable", 503)
			return
		}
		ids = append(ids, id)
		seen[id] = true
	}
	if rows.Err() != nil {
		http.Error(w, "Search unavailable", 503)
		return
	}
	if kind == "accounts" {
		for _, id := range h.Accounts {
			if strings.HasPrefix(id, query) && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	sort.Strings(ids)
	more := len(ids) > 30
	if more {
		ids = ids[:30]
	}
	items := []map[string]string{}
	for _, id := range ids {
		items = append(items, map[string]string{"value": id, "label": id})
	}
	jsonResponse(w, 200, map[string]any{"items": items, "more": more})
}
