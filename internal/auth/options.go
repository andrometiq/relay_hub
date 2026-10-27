package auth

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Options are bounded and permission scoped. The site ID range uses the
// directory index rather than downloading sites to filter in the browser.
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
	// Administrators see every site; members see only their granted sites.
	sql := `SELECT site_id,name FROM relay_sites WHERE site_id COLLATE "C" >= $1 AND site_id COLLATE "C" < $2 ORDER BY site_id COLLATE "C" LIMIT 31`
	args := []any{query, query + "\U0010ffff"}
	if kind == "grants" && u.Role != "admin" {
		sql = `SELECT g.site_id,COALESCE(a.name,'') FROM relay_user_sites g LEFT JOIN relay_sites a ON a.site_id=g.site_id WHERE g.user_id=$3 AND g.site_id COLLATE "C" >= $1 AND g.site_id COLLATE "C" < $2 ORDER BY g.site_id COLLATE "C" LIMIT 31`
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
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) != nil {
			http.Error(w, "Search unavailable", 503)
			return
		}
		ids = append(ids, id)
		seen[id] = true
		names[id] = name
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
		label := id
		if names[id] != "" && names[id] != id {
			label = names[id] + " (" + id + ")"
		}
		items = append(items, map[string]string{"value": id, "label": label})
	}
	jsonResponse(w, 200, map[string]any{"items": items, "more": more})
}
