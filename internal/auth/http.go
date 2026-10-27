package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"golang.org/x/time/rate"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const Cookie = "__Host-relay_hub"

type HTTP struct {
	Store          *Store
	Origin, Prefix string
	Accounts       []string
	limiter        *rate.Limiter
	slots          chan struct{}
}

func NewHTTP(store *Store, origin, prefix string, accounts []string) (*HTTP, error) {
	u, e := url.Parse(origin)
	if e != nil {
		return nil, ErrDenied
	}
	// Browsers treat localhost as trustworthy, including Secure cookies.
	// Plain HTTP is allowed only for local development, never a network host.
	localHTTP := u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
	if (u.Scheme != "https" && !localHTTP) || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrDenied
	}
	if prefix != "" && prefix != "/hub" {
		return nil, ErrDenied
	}
	return &HTTP{Store: store, Origin: origin, Prefix: prefix, Accounts: accounts, limiter: rate.NewLimiter(rate.Every(time.Second*3), 10), slots: make(chan struct{}, 2)}, nil
}
func CSRF(token string) string {
	h := hmac.New(sha256.New, []byte(token))
	h.Write([]byte("relay-hub-csrf"))
	return hex.EncodeToString(h.Sum(nil))
}
func cookie(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: Cookie, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (h *HTTP) redirect(w http.ResponseWriter, r *http.Request, p string) {
	http.Redirect(w, r, h.Prefix+p, 303)
}
func (h *HTTP) Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
func (h *HTTP) Session(r *http.Request) (User, error) {
	c, e := r.Cookie(Cookie)
	if e != nil {
		return User{}, e
	}
	return h.Store.Session(r.Context(), c.Value)
}

func (h *HTTP) Protect(next http.Handler) http.Handler {
	return h.Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		c, e := r.Cookie(Cookie)
		if e != nil {
			h.redirect(w, r, "/auth/login")
			return
		}
		if _, e = h.Store.Session(ctx, c.Value); e != nil {
			if !errors.Is(e, ErrDenied) {
				http.Error(w, "Session service temporarily unavailable", 503)
				return
			}
			cookie(w, "", -1)
			h.redirect(w, r, "/auth/login")
			return
		}
		cookie(w, c.Value, 34560000)
		next.ServeHTTP(w, r)
	}))
}
func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	// Proxy must preserve Host; configured origin is exact, not derived from forwarded headers.
	configured, _ := url.Parse(h.Origin)
	if r.Host != configured.Host {
		http.Error(w, "Invalid host", 403)
		return
	}
	if r.Method == "POST" {
		if r.Header.Get("Origin") != h.Origin {
			http.Error(w, "Invalid origin", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		if r.ParseForm() != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
	}
	if r.URL.Path == "/auth/login" {
		h.login(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	c, e := r.Cookie(Cookie)
	if e != nil {
		h.redirect(w, r, "/auth/login")
		return
	}
	u, e := h.Store.Session(ctx, c.Value)
	if e != nil {
		if !errors.Is(e, ErrDenied) {
			http.Error(w, "Session service temporarily unavailable", 503)
			return
		}
		cookie(w, "", -1)
		h.redirect(w, r, "/auth/login")
		return
	}
	cookie(w, c.Value, 34560000)
	if r.URL.Path == "/auth/options" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		h.searchOptions(w, r, u)
		return
	}
	data := map[string]any{"JSON": strings.Contains(r.Header.Get("Accept"), "application/json"), "User": u, "Prefix": h.Prefix, "CSRF": CSRF(c.Value), "Scopes": Scopes, "Accounts": []string{}}
	if r.Method == "POST" {
		if !hmac.Equal([]byte(r.Form.Get("csrf")), []byte(CSRF(c.Value))) {
			http.Error(w, "Invalid CSRF token", 403)
			return
		}
		id, _ := strconv.ParseInt(r.Form.Get("id"), 10, 64)
		switch r.URL.Path {
		case "/auth/logout":
			_, e = h.Store.DB.Exec(ctx, "DELETE FROM relay_auth_sessions WHERE token_hash=$1", Hash(c.Value))
			if e == nil {
				actorID := u.ID
				_ = h.Store.RecordOperationalAudit(ctx, "user", "session.logout", "completed", "", &actorID)
				cookie(w, "", -1)
				if data["JSON"] == true {
					jsonResponse(w, 200, map[string]any{"redirect": h.Prefix + "/auth/login"})
					return
				}
				h.redirect(w, r, "/auth/login")
				return
			}
		case "/auth/password":
			if id == 0 {
				id = u.ID
			}
			select {
			case h.slots <- struct{}{}:
				e = h.Store.ChangePasswordWithTokenRevocation(ctx, u, id, r.Form.Get("old_password"), r.Form.Get("password"), r.Form.Get("revoke_tokens") != "false")
				<-h.slots
			default:
				http.Error(w, "Try again shortly", 429)
				return
			}
			if e == nil && id == u.ID {
				cookie(w, "", -1)
				if data["JSON"] == true {
					jsonResponse(w, 200, map[string]any{"redirect": h.Prefix + "/auth/login"})
					return
				}
				h.redirect(w, r, "/auth/login")
				return
			}
		case "/auth/users":
			switch r.Form.Get("action") {
			case "create":
				if u.Role != "admin" {
					e = ErrDenied
					break
				}
				select {
				case h.slots <- struct{}{}:
					_, e = h.Store.CreateUser(ctx, u, strings.ToLower(strings.TrimSpace(r.Form.Get("username"))), r.Form.Get("password"), r.Form.Get("role"))
					<-h.slots
				default:
					http.Error(w, "Try again shortly", 429)
					return
				}
			case "enabled":
				e = h.Store.SetEnabled(ctx, u, id, r.Form.Get("enabled") == "true")
			case "grant":
				account := r.Form.Get("account")
				known := false
				if u.Role == "admin" {
					e = h.Store.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM relay_sites WHERE site_id=$1)", account).Scan(&known)
					if e != nil {
						http.Error(w, "Account lookup unavailable", 503)
						return
					}
					for _, a := range h.Accounts {
						if a == account {
							known = true
							break
						}
					}
				}
				if !known {
					e = ErrDenied
				} else {
					e = h.Store.Grant(ctx, u, id, account, r.Form.Get("allow") == "true")
				}
			default:
				e = ErrDenied
			}
		case "/auth/tokens":
			if r.Form.Get("action") == "revoke" {
				e = h.Store.Revoke(ctx, u, id)
			} else {
				days, parseErr := strconv.Atoi(r.Form.Get("days"))
				if parseErr != nil {
					e = ErrDenied
					break
				}
				var token string
				token, e = h.Store.Issue(ctx, u, r.Form.Get("name"), r.Form.Get("account"), r.Form["scopes"], days)
				if e == nil {
					data["Token"] = token
				}
			}
		case "/auth/sessions":
			if r.Form.Get("action") == "others" {
				_, e = h.Store.DB.Exec(ctx, "DELETE FROM relay_auth_sessions WHERE user_id=$1 AND token_hash<>$2", u.ID, Hash(c.Value))
				if e == nil {
					actorID := u.ID
					_ = h.Store.RecordOperationalAudit(ctx, "user", "session.revoke_others", "completed", "", &actorID)
				}
			} else {
				_, e = h.Store.DB.Exec(ctx, "DELETE FROM relay_auth_sessions WHERE user_id=$1 AND id=$2", u.ID, id)
				if e == nil {
					actorID := u.ID
					_ = h.Store.RecordOperationalAudit(ctx, "user", "session.revoke", "completed", "", &actorID)
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			data["Error"] = "Action rejected. Check permissions, password and required fields."
		} else {
			data["Notice"] = "Saved"
		}
	}
	if r.Method == "POST" && data["JSON"] == true {
		status := 200
		if e != nil {
			status = 400
		}
		jsonResponse(w, status, data)
		return
	}
	cursor, before, pattern, queryErr := listQuery(r)
	if queryErr != nil {
		http.Error(w, "Invalid cursor", 400)
		return
	}
	switch r.URL.Path {
	case "/auth/users":
		if u.Role != "admin" {
			http.Error(w, "Administrator required", 403)
			return
		}
		comparison, order := ">", "ASC"
		if before {
			comparison, order = "<", "DESC"
		}
		rows, e := h.Store.DB.Query(ctx, "SELECT id,username,role,enabled,auth_version,ARRAY(SELECT site_id FROM relay_user_sites WHERE user_id=relay_users.id ORDER BY site_id),ARRAY(SELECT g.site_id FROM relay_user_sites g JOIN relay_sites a ON a.site_id=g.site_id WHERE g.user_id=relay_users.id AND a.enabled AND relay_users.enabled ORDER BY g.site_id) FROM relay_users WHERE ($1::bigint=0 OR id "+comparison+" $1) AND username ILIKE $2 ORDER BY id "+order+" LIMIT 51", cursor, pattern)
		if e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		defer rows.Close()
		type userAccess struct {
			User
			Grants          []string `json:"grants"`
			EffectiveGrants []string `json:"effective_grants"`
		}
		list := []userAccess{}
		for rows.Next() {
			var v userAccess
			if rows.Scan(&v.ID, &v.Username, &v.Role, &v.Enabled, &v.Version, &v.Grants, &v.EffectiveGrants) != nil {
				http.Error(w, "Unavailable", 503)
				return
			}
			list = append(list, v)
		}
		if rows.Err() != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		data["More"] = len(list) > 50
		if len(list) > 50 {
			list = list[:50]
		}
		if before {
			for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
				list[i], list[j] = list[j], list[i]
			}
		}
		data["Users"] = list
		h.render(w, usersPage, data)
	case "/auth/tokens":
		type token struct {
			ID                             int64
			Name, Account, Scopes, Expires string
			Revoked                        bool
		}
		list := []token{}
		comparison, order := ">", "ASC"
		if before {
			comparison, order = "<", "DESC"
		}
		rows, e := h.Store.DB.Query(ctx, "SELECT id,name,site_id,array_to_string(scopes,', '),CASE WHEN expires_at='infinity'::timestamptz THEN 'Never' ELSE expires_at::text END,revoked_at IS NOT NULL FROM relay_api_tokens WHERE user_id=$1 AND ($2::bigint=0 OR id "+comparison+" $2) AND (name || ' ' || site_id) ILIKE $3 ORDER BY id "+order+" LIMIT 51", u.ID, cursor, pattern)
		if e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		for rows.Next() {
			var v token
			if rows.Scan(&v.ID, &v.Name, &v.Account, &v.Scopes, &v.Expires, &v.Revoked) != nil {
				rows.Close()
				http.Error(w, "Unavailable", 503)
				return
			}
			list = append(list, v)
		}
		rows.Close()
		if rows.Err() != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		data["More"] = len(list) > 50
		if len(list) > 50 {
			list = list[:50]
		}
		if before {
			for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
				list[i], list[j] = list[j], list[i]
			}
		}
		data["Tokens"] = list
		data["Grants"] = []string{}
		h.render(w, tokensPage, data)
	case "/auth/sessions":
		type session struct {
			ID          int64
			Agent, Seen string
			Current     bool
		}
		list := []session{}
		rows, e := h.Store.DB.Query(ctx, "SELECT id,user_agent,last_seen::text,token_hash=$2 FROM relay_auth_sessions WHERE user_id=$1 AND auth_version=$3 AND expires_at>now() ORDER BY id DESC", u.ID, Hash(c.Value), u.Version)
		if e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var v session
			if rows.Scan(&v.ID, &v.Agent, &v.Seen, &v.Current) != nil {
				http.Error(w, "Unavailable", 503)
				return
			}
			list = append(list, v)
		}
		if rows.Err() != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		data["Sessions"] = list
		h.render(w, sessionsPage, data)
	case "/auth/password":
		h.render(w, passwordPage, data)
	default:
		http.NotFound(w, r)
	}
}
func (h *HTTP) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		t := Secret()
		// Preserve the pre-login challenge across tabs in the same browser.
		if existing, err := r.Cookie("__Host-relay_hub_login"); err == nil && len(existing.Value) == 64 {
			if decoded, err := hex.DecodeString(existing.Value); err == nil && len(decoded) == 32 {
				t = existing.Value
			}
		}
		http.SetCookie(w, &http.Cookie{Name: "__Host-relay_hub_login", Value: t, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 600})
		h.render(w, loginPage, map[string]any{"JSON": strings.Contains(r.Header.Get("Accept"), "application/json"), "Prefix": h.Prefix, "CSRF": CSRF(t)})
		return
	}
	c, e := r.Cookie("__Host-relay_hub_login")
	if e != nil || len(c.Value) != 64 || !hmac.Equal([]byte(r.Form.Get("csrf")), []byte(CSRF(c.Value))) {
		http.Error(w, "Reload sign-in page", 403)
		return
	}
	if !h.limiter.Allow() {
		w.Header().Set("Retry-After", "3")
		http.Error(w, "Please wait before trying again", 429)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "Please retry shortly", 429)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	token, e := h.Store.Login(ctx, strings.ToLower(strings.TrimSpace(r.Form.Get("username"))), r.Form.Get("password"), r.UserAgent())
	if e != nil {
		http.Error(w, "Invalid login or service unavailable", 401)
		return
	}
	if old, e := r.Cookie(Cookie); e == nil {
		if _, e = h.Store.DB.Exec(ctx, "DELETE FROM relay_auth_sessions WHERE token_hash=$1", Hash(old.Value)); e != nil {
			http.Error(w, "Login unavailable", 503)
			return
		}
	}
	cookie(w, token, 34560000)
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		jsonResponse(w, 200, map[string]any{"redirect": h.Prefix + "/"})
		return
	}
	h.redirect(w, r, "/")
}
func (h *HTTP) render(w http.ResponseWriter, body string, data any) {
	if m, ok := data.(map[string]any); ok && m["JSON"] == true {
		jsonResponse(w, 200, data)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	template.Must(template.New("page").Parse(shell+body+"</main></body></html>")).Execute(w, data)
}

const shell = `<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Relay Hub</title><style>body{margin:0;background:#f7f8fa;color:#20242b;font:14px system-ui}main{max-width:900px;margin:4vh auto;padding:20px}article,form.card{background:white;border:1px solid #e3e6eb;border-radius:10px;padding:20px;margin:16px 0}h1{font-size:25px}a{color:#2563eb}label{display:block;margin:14px 0}input,select{box-sizing:border-box;max-width:100%;padding:10px;border:1px solid #cdd3dc;border-radius:6px;margin:6px}input:not([type=checkbox]):not([type=hidden]){display:block;width:100%;margin:6px 0}button{background:#2563eb;color:white;padding:10px 14px;border:0;border-radius:6px;cursor:pointer;margin:4px}nav{display:flex;gap:16px;align-items:center;flex-wrap:wrap}.error{color:#b91c1c}.notice{color:#15803d}p,small{color:#596475;line-height:1.5}.wrap{overflow-wrap:anywhere}code{display:block;background:#eff6ff;padding:16px;overflow-wrap:anywhere}fieldset{border:1px solid #e3e6eb}fieldset label{display:inline-block;margin:6px}form.inline{display:inline}hr{border:0;border-top:1px solid #e3e6eb}</style></head><body><main>{{if .User}}<nav><a href="{{.Prefix}}/">Relay Hub</a>{{if eq .User.Role "admin"}}<a href="{{.Prefix}}/auth/users">Users</a>{{end}}<a href="{{.Prefix}}/auth/tokens">API tokens</a><a href="{{.Prefix}}/auth/sessions">Sessions</a><a href="{{.Prefix}}/auth/password">Password</a><form method="post" action="{{.Prefix}}/auth/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Sign out</button></form></nav>{{end}}{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}`
const loginPage = `<h1>Sign in to Relay Hub</h1><form class="card" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Username<input name="username" autocomplete="username" required maxlength="100"></label><label>Password<input type="password" name="password" autocomplete="current-password" required maxlength="200"></label><button>Sign in</button></form>`
const usersPage = `<h1>Users</h1><form class="card" method="post"><h2>New user</h2><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="create"><label>Username<input name="username" required maxlength="100"></label><label>Initial password<input type="password" name="password" required minlength="14" maxlength="200" autocomplete="new-password"></label><label>Role<select name="role"><option value="member">Member</option><option value="admin">Administrator</option></select></label><button>Create user</button></form>{{range .Users}}<article><h2>{{.Username}}</h2><p>{{.Role}} · {{if .Enabled}}Active{{else}}Disabled{{end}}</p><form class="inline" method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="action" value="enabled"><input type="hidden" name="enabled" value="{{if .Enabled}}false{{else}}true{{end}}"><button>{{if .Enabled}}Disable{{else}}Enable{{end}}</button></form><form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="action" value="grant"><label>Account<select name="account" required>{{range $.Accounts}}<option>{{.}}</option>{{end}}</select></label><button name="allow" value="true">Grant access</button><button name="allow" value="false">Remove access</button></form>{{if ne .ID $.User.ID}}<form method="post" action="{{$.Prefix}}/auth/password"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><label>New password<input type="password" name="password" minlength="14" maxlength="200" required autocomplete="new-password"></label><button>Reset password and revoke credentials</button></form>{{end}}</article>{{end}}`
const tokensPage = `<h1>API tokens</h1>{{if .Token}}<p>Copy this token now. It will not be shown again.</p><code>{{.Token}}</code>{{end}}<form class="card" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Name<input name="name" required maxlength="100"></label><label>Account<input name="account" required maxlength="100" placeholder="Account ID"></label><label>Expires in days (0 means no expiry)<input type="number" name="days" min="0" max="90" value="0" required></label><fieldset><legend>Permissions</legend>{{range .Scopes}}<label><input type="checkbox" name="scopes" value="{{.}}">{{.}}</label>{{end}}</fieldset><button>Create token</button></form>{{range .Tokens}}<article><b>{{.Name}}</b><p>{{.Account}} · {{if .Revoked}}Revoked{{else}}Expires {{.Expires}}{{end}}</p><p>{{.Scopes}}</p>{{if not .Revoked}}<form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="revoke"><input type="hidden" name="id" value="{{.ID}}"><button>Revoke token</button></form>{{end}}</article>{{end}}`
const passwordPage = `<h1>Change password</h1><p>Changing a password revokes all sessions and API tokens for that user.</p><form class="card" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Current password<input type="password" name="old_password" required maxlength="200" autocomplete="current-password"></label><label>New password<input type="password" name="password" required minlength="14" maxlength="200" autocomplete="new-password"></label><button>Change password</button></form>`
const sessionsPage = `<h1>Sessions</h1><p>Sessions stay signed in until you sign out or revoke them · Five sessions per user</p><form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button name="action" value="others">Sign out other sessions</button></form>{{range .Sessions}}<article><b>{{if .Current}}This session{{else}}Other session{{end}}</b><p class="wrap">{{.Agent}}</p><p>Last active {{.Seen}}</p><form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><button>Revoke session</button></form></article>{{end}}`
