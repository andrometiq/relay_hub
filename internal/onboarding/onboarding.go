// Package onboarding links a Frappe site to the Hub without copied keys and
// connects its WhatsApp numbers through Meta Embedded Signup. It owns site
// enrolment, signup sessions and their steps, the Hub signup window, the
// resume worker and the signup MCP tools.
package onboarding

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config wires the onboarding service into the Hub or the simulator.
type Config struct {
	DB    *pgxpool.Pool
	Store *containers.Store
	Auth  *auth.Store
	// Client makes every Graph call and the Core proof call.
	Client *http.Client
	// PublicOrigin is RELAY_PUBLIC_ORIGIN plus RELAY_PUBLIC_PREFIX, used in launch_url and mcp_url.
	PublicOrigin string
	// SDKURL is the Facebook JS SDK the signup window loads.
	SDKURL string
	// DialogOrigin accepts the origin of Embedded Signup messages.
	DialogOrigin func(origin string) bool
	// GraphBase is the Graph API host without a version.
	GraphBase string
	// Reload republishes runtime accounts and callback sites after a channel change.
	Reload   func(context.Context) error
	Metadata *whatsapp.MetadataSync
	Now      func() time.Time
}

const defaultSDKURL = "https://connect.facebook.net/en_US/sdk.js"

// Service runs enrolment and WhatsApp signup.
type Service struct {
	cfg     Config
	limiter *limiter
	wake    chan struct{}
}

// New checks the configuration and fills defaults.
func New(cfg Config) (*Service, error) {
	if cfg.DB == nil || cfg.Store == nil || cfg.Auth == nil {
		return nil, errors.New("onboarding needs the database, container store and auth store")
	}
	origin, err := url.Parse(cfg.PublicOrigin)
	if err != nil || origin.Host == "" || (origin.Scheme != "https" && !(origin.Scheme == "http" && loopbackHost(origin.Hostname()))) || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("onboarding needs an HTTPS public origin")
	}
	cfg.PublicOrigin = strings.TrimSuffix(cfg.PublicOrigin, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 12 * time.Second}
	}
	client := *cfg.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > 12*time.Second {
		client.Timeout = 12 * time.Second
	}
	cfg.Client = &client
	if cfg.SDKURL == "" {
		cfg.SDKURL = defaultSDKURL
	}
	if _, err = sourceOrigin(cfg.SDKURL); err != nil {
		return nil, err
	}
	if cfg.DialogOrigin == nil {
		cfg.DialogOrigin = FacebookDialog
	}
	if cfg.GraphBase == "" {
		cfg.GraphBase = "https://graph.facebook.com"
	}
	cfg.GraphBase = strings.TrimSuffix(cfg.GraphBase, "/")
	if cfg.Reload == nil {
		cfg.Reload = func(context.Context) error { return nil }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, limiter: newLimiter(20, time.Hour), wake: make(chan struct{}, 1)}, nil
}

// FacebookDialog accepts HTTPS origins on facebook.com and its subdomains.
func FacebookDialog(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Path != "" || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "facebook.com" || strings.HasSuffix(host, ".facebook.com")
}

// ExactOrigin accepts only one origin, such as the simulator's fake Meta address.
func ExactOrigin(allowed string) func(string) bool {
	return func(origin string) bool { return origin == allowed }
}

// CheckSourceURL refuses an SDK or dialog URL that is not HTTPS, except on loopback.
func CheckSourceURL(raw string) error {
	_, err := sourceOrigin(raw)
	return err
}

func sourceOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackHost(u.Hostname()))) {
		return "", errors.New("Facebook SDK and dialog URLs must be HTTPS, or HTTP on loopback")
	}
	return u.Scheme + "://" + u.Host, nil
}

// loopbackHost reports a loopback IP, localhost or a *.localhost name.
func loopbackHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Register mounts the enrolment API and the signup window.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/enrol", s.enrol)
	mux.HandleFunc("GET /onboard/whatsapp/{session}", s.page)
	mux.HandleFunc("GET /onboard/assets/{file}", s.asset)
	mux.HandleFunc("GET /onboard/whatsapp/{session}/config", s.windowConfig)
	mux.HandleFunc("GET /onboard/whatsapp/{session}/state", s.windowState)
	mux.HandleFunc("POST /onboard/whatsapp/{session}/event", s.windowEvent)
	mux.HandleFunc("POST /onboard/whatsapp/{session}/code", s.windowCode)
}

// now is truncated to microseconds, the precision PostgreSQL stores.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// wakeWorker asks the worker to look for due steps now.
func (s *Service) wakeWorker() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
