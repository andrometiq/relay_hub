package requestlog

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Recorder takes finished entries. It must not block.
type Recorder interface{ Record(Entry) }

const errorLimit = 200

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// skipped reports requests that are not logged: static UI assets, health
// probes and the long-lived live update socket.
func skipped(path string) bool {
	return strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/socket.io/") || path == "/healthz" || path == "/readyz"
}

// Middleware records each request that next serves. A proxy's valid
// X-Request-ID is kept; otherwise the Hub makes one. The response carries it.
func Middleware(recorder Recorder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipped(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		requestID := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(requestID) {
			requestID = rand.Text()
		}
		w.Header().Set("X-Request-ID", requestID)
		requestTags := &tags{}
		r = r.WithContext(context.WithValue(r.Context(), tagsKey{}, requestTags))
		body := &countingBody{ReadCloser: r.Body}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = body
		}
		response := &responseRecorder{ResponseWriter: w}
		start := time.Now()
		completed := false
		// No recover: the server keeps logging a panic with its original stack.
		defer func() {
			entry := Entry{OccurredAt: start, Method: clean(r.Method, 16), Path: clean(r.URL.EscapedPath(), 500), Route: clean(r.Pattern, 200),
				Status: response.status, DurationMS: float64(time.Since(start).Microseconds()) / 1000, RequestBytes: max(body.bytes, r.ContentLength), ResponseBytes: response.bytes,
				UserAgent: clean(r.UserAgent(), 200), SiteID: clean(requestTags.site, 140), Account: clean(requestTags.account, 140), RequestID: requestID}
			entry.RemoteIP, _ = netip.ParseAddr(ClientIP(r))
			if entry.Status == 0 {
				entry.Status = http.StatusOK
			}
			if entry.Status >= 500 {
				entry.Error = clean(strings.TrimSpace(response.errorText.String()), errorLimit)
			}
			if !completed {
				entry.Status, entry.Error = http.StatusInternalServerError, "panic"
			}
			recorder.Record(entry)
		}()
		next.ServeHTTP(response, r)
		completed = true
	})
}

type countingBody struct {
	io.ReadCloser
	bytes int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes += int64(n)
	return n, err
}

// responseRecorder keeps the status, the body size and, for a plain-text 5xx,
// the start of the error message that the client also receives.
type responseRecorder struct {
	http.ResponseWriter
	status    int
	bytes     int64
	errorText strings.Builder
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.status == 0 && code >= 200 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 500 && r.errorText.Len() < errorLimit && strings.HasPrefix(r.Header().Get("Content-Type"), "text/plain") {
		r.errorText.Write(p[:min(len(p), errorLimit-r.errorText.Len())])
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

func (r *responseRecorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	http.NewResponseController(r.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the server's writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
