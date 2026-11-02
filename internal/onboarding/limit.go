package onboarding

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter allows a fixed number of events per key in a sliding window. It is
// in memory: a Hub restart forgets it, which is acceptable for enrolment abuse.
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	events map[string][]time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, events: map[string][]time.Time{}}
}

// allow records one event for every key and returns the wait when one key is full.
func (l *limiter) allow(now time.Time, keys ...string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) > 10000 {
		for key, times := range l.events {
			if len(times) == 0 || now.Sub(times[len(times)-1]) > l.window {
				delete(l.events, key)
			}
		}
	}
	for _, key := range keys {
		times := l.recent(key, now)
		if len(times) >= l.limit {
			return false, l.window - now.Sub(times[0])
		}
	}
	for _, key := range keys {
		l.events[key] = append(l.recent(key, now), now)
	}
	return true, 0
}

func (l *limiter) recent(key string, now time.Time) []time.Time {
	times := l.events[key]
	kept := times[:0]
	for _, at := range times {
		if now.Sub(at) < l.window {
			kept = append(kept, at)
		}
	}
	l.events[key] = kept
	return kept
}

// clientIP is the caller's address. The Hub binds loopback behind a TLS proxy,
// so a loopback peer's last X-Forwarded-For entry is the client.
func clientIP(r *http.Request) string {
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
