package auth

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// UI exposes only compiled assets; all account data continues through authenticated handlers.
func (h *HTTP) UI(dir string) http.Handler {
	return h.Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && !strings.Contains(r.Header.Get("Accept"), "application/json") {
			body, e := os.ReadFile(dir + "/index.html")
			if e != nil {
				http.Error(w, "UI unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(strings.ReplaceAll(string(body), "./assets/", h.Prefix+"/assets/")))
			return
		}
		h.ServeHTTP(w, r)
	}))
}
func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
func fuzzyPattern(q string) string {
	q = strings.TrimSpace(q)
	if len(q) > 100 {
		q = q[:100]
	}
	var b strings.Builder
	b.WriteByte('%')
	for _, c := range q {
		if c == '%' || c == '_' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
		b.WriteByte('%')
	}
	return b.String()
}
func listQuery(r *http.Request) (int64, bool, string, error) {
	raw := r.URL.Query().Get("cursor")
	var id int64
	var e error
	if raw != "" {
		id, e = strconv.ParseInt(raw, 10, 64)
		if id < 0 {
			e = ErrDenied
		}
	}
	return id, r.URL.Query().Get("direction") == "before", fuzzyPattern(r.URL.Query().Get("q")), e
}
