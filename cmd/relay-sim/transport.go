package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

func localURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme != "http" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("expected a loopback HTTP origin")
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("destination must be a literal loopback address")
	}
	if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
		return nil, errors.New("expected an explicit valid port")
	}
	return u, nil
}

type capture struct {
	Sequence   int             `json:"sequence"`
	Kind       string          `json:"kind"`
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	Headers    http.Header     `json:"headers"`
	Body       json.RawMessage `json:"body"`
	Status     int             `json:"status"`
	Response   json.RawMessage `json:"response"`
	StartedNS  int64           `json:"started_ns"`
	FinishedNS int64           `json:"finished_ns"`
	Fault      string          `json:"fault,omitempty"`
}

type recorder struct {
	mu     sync.Mutex
	start  time.Time
	rows   []capture
	faults map[string][]string
	hold   bool
}

func newRecorder() *recorder {
	return &recorder{start: time.Now(), rows: []capture{}, faults: map[string][]string{}}
}
func (s *recorder) snapshot() []capture {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capture{}, s.rows...)
}
func (s *recorder) fault(kind string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.faults[kind]; len(f) > 0 {
		s.faults[kind] = f[1:]
		return f[0]
	}
	if kind == "status" && s.hold {
		return "503"
	}
	return ""
}
func safeJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`null`)
	}
	if json.Valid(b) {
		return b
	}
	v, _ := json.Marshal(string(b))
	return v
}
func (s *recorder) record(r *http.Request, kind string, body []byte, status int, reply []byte, started time.Time, fault string) {
	headers := http.Header{}
	for _, k := range []string{"Content-Type", "Accept", "X-Relay-Timestamp", "X-Relay-Dispatch-Ms", "Retry-After"} {
		if v := r.Header.Get(k); v != "" {
			headers.Set(k, v)
		}
	}
	for _, k := range []string{"Authorization", "Cookie", "X-Relay-Signature", "X-Hub-Signature-256"} {
		if r.Header.Get(k) != "" {
			headers.Set(k, "[redacted]")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, capture{len(s.rows) + 1, kind, r.Method, r.URL.Path, headers, safeJSON(body), status, safeJSON(reply), started.Sub(s.start).Nanoseconds(), time.Since(s.start).Nanoseconds(), fault})
}

type simTransport struct {
	base       *http.Transport
	meta, core *url.URL
	mu         sync.RWMutex
	site       string
	records    *recorder
}

func newTransport(meta, core *url.URL, records *recorder) *simTransport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, errors.New("simulation refused non-loopback dial")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
	}
	return &simTransport{base: base, meta: meta, core: core, records: records}
}
func (t *simTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	t.mu.RLock()
	site := t.site
	t.mu.RUnlock()
	origin := u.Scheme + "://" + u.Host
	graph := origin == "https://graph.facebook.com"
	if graph {
		clone.URL.Scheme = t.meta.Scheme
		clone.URL.Host = t.meta.Host
		clone.Host = t.meta.Host
	} else if origin == site || origin == t.core.String() {
		clone.URL.Scheme = t.core.Scheme
		clone.URL.Host = t.core.Host
		clone.Host = r.URL.Host
	} else {
		return nil, errors.New("simulation refused unexpected destination")
	}
	if graph {
		return t.base.RoundTrip(clone)
	}
	kind := "callback"
	if strings.HasSuffix(r.URL.Path, ".receive_statuses") {
		kind = "status"
	}
	started := time.Now()
	var body []byte
	var err error
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, 2<<20))
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		clone.Body = io.NopCloser(bytes.NewReader(body))
	}
	fault := t.records.fault(kind)
	var res *http.Response
	switch fault {
	case "503", "429":
		code := 503
		if fault == "429" {
			code = 429
		}
		res = &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"simulated failure"}`))}
	case "wrong_ack":
		res = &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"message":{"accepted":0}}`))}
	default:
		res, err = t.base.RoundTrip(clone)
	}
	if err != nil {
		t.records.record(r, kind, body, 0, []byte(`{"error":"transport failed"}`), started, fault)
		return nil, err
	}
	reply, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	t.records.record(r, kind, body, res.StatusCode, reply, started, fault)
	res.Body = io.NopCloser(bytes.NewReader(reply))
	return res, nil
}
