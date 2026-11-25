// Package requestlog keeps one database row for each Hub HTTP request. It
// stores no query string, header value other than the user agent, body or secret.
package requestlog

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
)

// Entry is one row of relay_request_log.
type Entry struct {
	OccurredAt    time.Time
	Method        string
	Path          string
	Route         string
	Status        int
	DurationMS    float64
	RequestBytes  int64
	ResponseBytes int64
	RemoteIP      netip.Addr
	UserAgent     string
	SiteID        string
	Account       string
	RequestID     string
	Error         string
}

type tagsKey struct{}

type tags struct{ site, account string }

// Tag records the site and channel account a handler resolved for the request.
// Empty values keep the earlier value. Call it from the request goroutine.
func Tag(ctx context.Context, site, account string) {
	t, _ := ctx.Value(tagsKey{}).(*tags)
	if t == nil {
		return
	}
	if site != "" {
		t.site = site
	}
	if account != "" {
		t.account = account
	}
}

// ClientIP is the caller's address. The Hub binds loopback behind a TLS proxy,
// so a loopback peer's last X-Forwarded-For entry is the client.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if candidate := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); candidate != nil {
				return candidate.String()
			}
		}
	}
	return host
}

// clean makes s safe for a PostgreSQL text column and keeps at most limit bytes.
func clean(s string, limit int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
