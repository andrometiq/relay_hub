// Package review proxies isolated Hub and Frappe services, preserving their authentication.
// Static Core design review remains available when no installed upstream is configured.
package review

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const cookieName = "__Host-relay_review"

type Config struct{ Origin, Username, Salt, PasswordHash, HubDir, CoreDir, CoreUpstream, CoreSite, CoreSocket string }
type Server struct {
	cfg           Config
	db            *pgxpool.Pool
	version       string
	limit         *rate.Limiter
	hashSlots     chan struct{}
	hub, core     http.Handler
	installedCore http.Handler
	coreSocket    http.Handler
}

func New(cfg Config, db *pgxpool.Pool) (*Server, error) {
	u, e := url.Parse(cfg.Origin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("review origin must be an HTTPS origin")
	}
	salt, e := hex.DecodeString(cfg.Salt)
	if e != nil || len(salt) < 16 {
		return nil, fmt.Errorf("invalid review salt")
	}
	hash, e := hex.DecodeString(cfg.PasswordHash)
	if e != nil || len(hash) != 32 || cfg.Username == "" {
		return nil, fmt.Errorf("invalid review credential")
	}
	s := &Server{cfg: cfg, db: db, version: digest(cfg.Username + cfg.Salt + cfg.PasswordHash), limit: rate.NewLimiter(rate.Every(6*time.Second), 5), hashSlots: make(chan struct{}, 1)}
	for _, d := range []string{cfg.HubDir, cfg.CoreDir} {
		if _, e := os.Stat(d + "/index.html"); e != nil {
			return nil, e
		}
	}
	s.hub = http.StripPrefix("/hub/", http.FileServer(http.Dir(cfg.HubDir)))
	s.core = http.StripPrefix("/core/", http.FileServer(http.Dir(cfg.CoreDir)))
	if cfg.CoreUpstream != "" {
		target, err := url.Parse(cfg.CoreUpstream)
		if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "" || cfg.CoreSite == "" || strings.ContainsAny(cfg.CoreSite, "/\\\r\n") {
			return nil, fmt.Errorf("invalid isolated Core upstream")
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		original := proxy.Director
		proxy.Director = func(req *http.Request) {
			original(req)
			req.Host = strings.TrimPrefix(cfg.Origin, "https://")
			req.Header.Set("X-Frappe-Site-Name", cfg.CoreSite)
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", req.Host)
			req.Header.Del("X-Forwarded-Prefix")
			req.Header.Set("X-Forwarded-Port", "443")
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) { http.Error(w, "Core service unavailable", 503) }
		s.installedCore = proxy
		if cfg.CoreSocket != "" {
			if !filepath.IsAbs(cfg.CoreSocket) {
				return nil, fmt.Errorf("Core socket must be an absolute local path")
			}
			socketProxy := *proxy
			socketProxy.Transport = &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", cfg.CoreSocket)
			}}
			s.coreSocket = &socketProxy
		}

	}
	return s, nil
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func csrf(token string) string {
	h := hmac.New(sha256.New, []byte(token))
	h.Write([]byte("relay-review-csrf"))
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Server) setCookie(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Strict-Transport-Security", "max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; media-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if "https://"+r.Host != s.cfg.Origin {
		http.Error(w, "Invalid host", http.StatusForbidden)
		return
	}
	if s.installedCore != nil && (r.URL.Path == "/core" || strings.HasPrefix(r.URL.Path, "/core/")) {
		http.Redirect(w, r, "/relay", http.StatusSeeOther)
		return
	}
	// Hub owns its authentication. Do not gate its API on a preview cookie.
	if strings.HasPrefix(r.URL.Path, "/hub/") {
		if r.URL.Path == "/hub/readyz" || r.URL.Path == "/hub/healthz" {
			http.NotFound(w, r)
			return
		}
		target, _ := url.Parse("http://127.0.0.1:18081")
		proxy := httputil.NewSingleHostReverseProxy(target)
		original := proxy.Director
		proxy.Director = func(req *http.Request) {
			original(req)
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/hub")
			req.URL.RawPath = ""
		}
		proxy.ServeHTTP(w, r)
		return
	}
	if s.installedCore != nil {
		if r.URL.Path == "/hub" {
			http.Redirect(w, r, "/hub/", http.StatusSeeOther)
			return
		}
		// Frappe owns login, CSRF, document permissions and private file access.
		// Its standard login/Desk templates contain framework-managed scripts.
		w.Header().Del("Content-Security-Policy")
		if s.coreSocket != nil && strings.HasPrefix(r.URL.Path, "/socket.io/") {
			s.coreSocket.ServeHTTP(w, r)
			return
		}
		s.installedCore.ServeHTTP(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if r.Method == "POST" {
		if r.Header.Get("Origin") != s.cfg.Origin {
			http.Error(w, "Invalid origin", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
	}
	if r.URL.Path == "/login" {
		s.login(w, r)
		return
	}
	c, e := r.Cookie(cookieName)
	if e != nil || len(c.Value) != 64 {
		http.Redirect(w, r, "/login", 303)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var id string
	e = s.db.QueryRow(ctx, `UPDATE relay_review_sessions SET last_seen=now() WHERE token_hash=$1 AND credential_version=$2 AND expires_at>now() AND last_seen>now()-interval '30 minutes' RETURNING token_hash`, digest(c.Value), s.version).Scan(&id)
	if e != nil {
		s.setCookie(w, "", -1)
		http.Redirect(w, r, "/login", 303)
		return
	}
	if r.Method == "POST" && !hmac.Equal([]byte(r.Form.Get("csrf")), []byte(csrf(c.Value))) {
		http.Error(w, "Invalid CSRF token", 403)
		return
	}
	switch r.URL.Path {
	case "/readyz":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		probe, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:18081/readyz", nil)
		if err != nil {
			w.WriteHeader(503)
			return
		}
		client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(probe)
		if err != nil {
			w.WriteHeader(503)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	case "/logout":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if _, e = s.db.Exec(ctx, "DELETE FROM relay_review_sessions WHERE token_hash=$1", id); e != nil {
			http.Error(w, "Retry logout", 503)
			return
		}
		s.setCookie(w, "", -1)
		http.Redirect(w, r, "/login", 303)
	case "/sessions":
		s.sessions(w, r, id, csrf(c.Value))
	case "/":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		render(w, portal, map[string]any{"CSRF": csrf(c.Value)})
	default:
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		p := r.URL.Path
		if p == "/hub" || p == "/core" {
			http.Redirect(w, r, p+"/", 303)
			return
		}
		prefix := ""
		var handler http.Handler
		if strings.HasPrefix(p, "/hub/") {
			prefix = "/hub/"
			handler = s.hub
		}
		if strings.HasPrefix(p, "/core/") {
			prefix = "/core/"
			handler = s.core
		}
		rel := strings.TrimPrefix(p, prefix)
		allowed := rel == "" || rel == "index.html" || (strings.HasPrefix(rel, "assets/") && path.Clean(rel) == rel && !strings.HasSuffix(rel, "/") && !strings.HasSuffix(rel, ".map"))
		for _, seg := range strings.Split(rel, "/") {
			if strings.HasPrefix(seg, ".") {
				allowed = false
			}
		}
		if prefix == "" || !allowed {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		t := random()
		// Preserve the pre-login challenge across tabs in the same browser.
		if existing, err := r.Cookie("__Host-relay_login"); err == nil && len(existing.Value) == 64 {
			if decoded, err := hex.DecodeString(existing.Value); err == nil && len(decoded) == 32 {
				t = existing.Value
			}
		}
		http.SetCookie(w, &http.Cookie{Name: "__Host-relay_login", Value: t, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 600})
		render(w, loginPage, map[string]any{"CSRF": csrf(t)})
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	c, e := r.Cookie("__Host-relay_login")
	if e != nil || len(c.Value) != 64 || !hmac.Equal([]byte(r.Form.Get("csrf")), []byte(csrf(c.Value))) {
		http.Error(w, "Reload login page", 403)
		return
	}
	if !s.limit.Allow() {
		w.Header().Set("Retry-After", "6")
		http.Error(w, "Too many attempts. Please wait.", 429)
		return
	}
	select {
	case s.hashSlots <- struct{}{}:
		defer func() { <-s.hashSlots }()
	default:
		http.Error(w, "Please retry shortly", 429)
		return
	}
	salt, _ := hex.DecodeString(s.cfg.Salt)
	expected, _ := hex.DecodeString(s.cfg.PasswordHash)
	got, e := pbkdf2.Key(sha256.New, r.Form.Get("password"), salt, 600000, 32)
	if e != nil || subtle.ConstantTimeCompare(got, expected) != 1 || subtle.ConstantTimeCompare([]byte(r.Form.Get("username")), []byte(s.cfg.Username)) != 1 {
		http.Error(w, "Invalid login", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, e := s.db.Begin(ctx)
	if e != nil {
		http.Error(w, "Login unavailable", 503)
		return
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351018)")
	if e != nil {
		http.Error(w, "Login unavailable", 503)
		return
	}
	_, e = tx.Exec(ctx, `DELETE FROM relay_review_sessions WHERE expires_at<=now() OR last_seen<=now()-interval '30 minutes' OR credential_version<>$1`, s.version)
	if e != nil {
		http.Error(w, "Login unavailable", 503)
		return
	}
	if old, e := r.Cookie(cookieName); e == nil {
		if _, e = tx.Exec(ctx, "DELETE FROM relay_review_sessions WHERE token_hash=$1", digest(old.Value)); e != nil {
			http.Error(w, "Login unavailable", 503)
			return
		}
	}
	_, e = tx.Exec(ctx, `DELETE FROM relay_review_sessions WHERE token_hash IN (SELECT token_hash FROM relay_review_sessions ORDER BY created_at DESC OFFSET 4)`)
	if e != nil {
		http.Error(w, "Login unavailable", 503)
		return
	}
	token := random()
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, e = tx.Exec(ctx, `INSERT INTO relay_review_sessions(token_hash,credential_version,expires_at,user_agent) VALUES($1,$2,now()+interval '8 hours',$3)`, digest(token), s.version, ua)
	if e != nil || tx.Commit(ctx) != nil {
		http.Error(w, "Login unavailable", 503)
		return
	}
	s.setCookie(w, token, 28800)
	http.SetCookie(w, &http.Cookie{Name: "__Host-relay_login", Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/", 303)
}
func (s *Server) sessions(w http.ResponseWriter, r *http.Request, id, token string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if r.Method == "POST" {
		target := r.Form.Get("session")
		var e error
		if target == "others" {
			_, e = s.db.Exec(ctx, "DELETE FROM relay_review_sessions WHERE token_hash<>$1 AND credential_version=$2", id, s.version)
		} else {
			_, e = s.db.Exec(ctx, "DELETE FROM relay_review_sessions WHERE token_hash=$1 AND credential_version=$2", target, s.version)
		}
		if e != nil {
			http.Error(w, "Retry revocation", 503)
			return
		}
		http.Redirect(w, r, "/sessions", 303)
		return
	}
	rows, e := s.db.Query(ctx, `SELECT token_hash,user_agent,created_at,last_seen FROM relay_review_sessions WHERE credential_version=$1 AND expires_at>now() AND last_seen>now()-interval '30 minutes' ORDER BY created_at DESC LIMIT 5`, s.version)
	if e != nil {
		http.Error(w, "Sessions unavailable", 503)
		return
	}
	defer rows.Close()
	type session struct {
		ID, Agent, Created, Seen string
		Current                  bool
	}
	list := []session{}
	for rows.Next() {
		var v session
		var created, seen time.Time
		if rows.Scan(&v.ID, &v.Agent, &created, &seen) != nil {
			http.Error(w, "Sessions unavailable", 503)
			return
		}
		v.Created = created.UTC().Format(time.RFC3339)
		v.Seen = seen.UTC().Format(time.RFC3339)
		v.Current = v.ID == id
		list = append(list, v)
	}
	if rows.Err() != nil {
		http.Error(w, "Sessions unavailable", 503)
		return
	}
	render(w, sessionsPage, map[string]any{"CSRF": token, "Sessions": list})
}

const shell = `<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Relay review</title><style>body{margin:0;background:#f7f8fa;color:#20242b;font:15px system-ui}main{max-width:760px;margin:7vh auto;padding:24px}article,form.card{background:white;border:1px solid #e3e6eb;border-radius:12px;padding:24px;margin:16px 0}h1{font-size:27px}a{color:#2563eb}label{display:block;margin:18px 0}input{display:block;box-sizing:border-box;width:100%;padding:12px;border:1px solid #cdd3dc;border-radius:7px;margin-top:8px}button,.button{display:inline-block;background:#2563eb;color:white;padding:11px 16px;border:0;border-radius:7px;text-decoration:none;cursor:pointer}nav{display:flex;gap:20px;align-items:center;flex-wrap:wrap}small,p{color:#596475;line-height:1.6}.agent{overflow-wrap:anywhere}</style></head><body><main>`
const loginPage = shell + `<h1>Sign in to Relay</h1><p>Private UI review</p><form class="card" method="post" action="/login"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Login ID<input name="username" autocomplete="username" required maxlength="100"></label><label>Password<input type="password" name="password" autocomplete="current-password" required maxlength="200"></label><button>Sign in</button></form></main></body></html>`
const portal = shell + `<nav><b>Relay</b><a href="/sessions">Sessions</a><form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Sign out</button></form></nav><h1>Your review workspace</h1><article><h2>Relay Core</h2><p>Inbox, teams, contacts, flows and WhatsApp workspace.</p><a class="button" href="/core/">Open Core</a></article><article><h2>Relay Hub</h2><p>Accounts, channels and delivery operations.</p><a class="button" href="/hub/">Open Hub</a></article><p>UI preview with sample data. Live messaging and configuration changes are unavailable through this review link.</p></main></body></html>`
const sessionsPage = shell + `<a href="/">← Workspace</a><h1>Active sessions</h1><p>30-minute idle timeout · 8-hour maximum · Up to 5 sessions</p><form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="session" value="others"><button>Sign out other sessions</button></form>{{range .Sessions}}<article><b>{{if .Current}}This session{{else}}Other session{{end}}</b><p class="agent">{{.Agent}}</p><small>Signed in {{.Created}}<br>Last active {{.Seen}}</small><form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="session" value="{{.ID}}"><button>Revoke session</button></form></article>{{end}}</main></body></html>`

func render(w http.ResponseWriter, body string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	template.Must(template.New("page").Parse(body)).Execute(w, data)
}
