package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	accountstore "github.com/andrometiq/relay_hub/internal/accounts"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/andrometiq/relay_hub/internal/envconfig"
	"github.com/andrometiq/relay_hub/internal/hubmcp"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Registration metadata is available only to the Hub administrator; browser
// session validation is supplied by the same auth service as other admin APIs.
func telegramWebhookSettings(registry *channels.Registry, master string, session func(*http.Request) (auth.User, error)) http.Handler {
	return channelWebhookSettings("telegram", registry.TelegramWebhookSecret, master, session)
}

func channelWebhookSettings(channel string, credential func(string, string) (string, error), master string, session func(*http.Request) (auth.User, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := session(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if u.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		account := r.PathValue("account")
		secret, err := credential(account, master)
		if err != nil {
			http.Error(w, "Webhook unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"path": "/webhooks/" + channel + "/" + account, "secret_token": secret})
	})
}

func connect(ctx context.Context) (*pgx.Conn, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, errors.New("database connection failed; check configuration and availability")
	}
	return conn, nil
}
func check(ctx context.Context, ready bool) error {
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if ready {
		return database.Ready(ctx, conn)
	}
	return database.CheckVersion(ctx, conn)
}

type runtimeAccountSource interface {
	Runtime(context.Context) ([]accountstore.RuntimeCredential, error)
}

func activateWhatsApp(ctx context.Context, wa *whatsapp.Service, store runtimeAccountSource) error {
	configured, err := runtimeWhatsApp(ctx, store)
	if err != nil {
		return err
	}
	return wa.ReplaceAccounts(configured)
}

func runtimeWhatsApp(ctx context.Context, store runtimeAccountSource) ([]whatsapp.Account, error) {
	rows, err := store.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	accounts := make([]whatsapp.Account, 0)
	for _, row := range rows {
		if row.Channel != "WhatsApp" {
			continue
		}
		get := func(key string) string {
			if value, ok := row.Config[key].(string); ok {
				return value
			}
			return ""
		}
		accounts = append(accounts, whatsapp.Account{
			ID: row.AccountID, Site: row.Site, AccessToken: row.Secret,
			AppID: get("app_id"), PhoneID: get("phone_id"), WABAID: get("waba_id"), Version: get("graph_version"),
			AppSecret: get("app_secret"), VerifyToken: get("verify_token"), CoreToken: get("core_token"),
			CoreAPIKey: get("core_api_key"), CoreAPISecret: get("core_api_secret"), CallbackSecret: get("callback_secret"),
			SyncMetadata: boolValue(row.Config["sync_metadata"]), Templates: boolValue(row.Config["templates"]),
		})
	}
	return accounts, nil
}

func boolValue(value any) bool {
	v, ok := value.(bool)
	return ok && v
}

func whatsappCandidate(account accountstore.Account, channel accountstore.Channel, secret string, stored accountstore.RuntimeCredential) whatsapp.Account {
	config := map[string]any{}
	for key, value := range stored.Config {
		config[key] = value
	}
	for key, value := range channel.Config {
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		config[key] = value
	}
	get := func(key string) string { value, _ := config[key].(string); return value }
	if secret == "" {
		secret = stored.Secret
	}
	return whatsapp.Account{ID: account.ID, Site: account.Site, AccessToken: secret,
		AppID: get("app_id"), PhoneID: get("phone_id"), WABAID: get("waba_id"), Version: get("graph_version"),
		AppSecret: get("app_secret"), VerifyToken: get("verify_token"), CoreToken: get("core_token"),
		CoreAPIKey: get("core_api_key"), CoreAPISecret: get("core_api_secret"), CallbackSecret: get("callback_secret"),
		SyncMetadata: boolValue(config["sync_metadata"]), Templates: boolValue(config["templates"]), Groups: false, Calls: false}
}
func runtimeCallbacks(rows []accountstore.RuntimeCredential) []channels.RuntimeCallback {
	result := []channels.RuntimeCallback{}
	for _, row := range rows {
		if row.Channel != "Telegram" && row.Channel != "Email" {
			continue
		}
		get := func(key string) string {
			if v, ok := row.Config[key].(string); ok {
				return v
			}
			return ""
		}
		if get("core_api_key") != "" && get("core_api_secret") != "" && get("callback_secret") != "" {
			result = append(result, channels.RuntimeCallback{Account: row.AccountID, Site: row.Site, APIKey: get("core_api_key"), APISecret: get("core_api_secret"), CallbackSecret: get("callback_secret")})
		}
	}
	return result
}
func run() error {
	if err := envconfig.Load(".env"); err != nil {
		return err
	}
	if len(os.Args) != 2 {
		return errors.New("usage: relay-hub doctor|migrate|serve|bootstrap-user")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch os.Args[1] {
	case "bootstrap-user":
		if err := check(ctx, true); err != nil {
			return err
		}
		pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return err
		}
		defer pool.Close()
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return errors.New("username required on stdin")
		}
		name := strings.TrimSpace(scanner.Text())
		if !scanner.Scan() {
			return errors.New("password required on stdin")
		}
		password := scanner.Text()
		if err = (&auth.Store{DB: pool}).Bootstrap(ctx, name, password); err != nil {
			return err
		}
		fmt.Println("Initial Hub administrator created")
		return nil
	case "doctor":
		if err := check(ctx, false); err != nil {
			return err
		}
		fmt.Println("PostgreSQL 18 connection: OK")
		return nil
	case "migrate":
		conn, err := connect(ctx)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		if err = database.Migrate(ctx, conn); err != nil {
			return err
		}
		fmt.Println("Database schema is up to date")
		return nil
	case "serve":
		if err := check(ctx, true); err != nil {
			return err
		}
		ui := os.Getenv("RELAY_UI_DIR")
		if ui == "" {
			ui = "frontend/dist"
		}
		if _, err := os.Stat(filepath.Join(ui, "index.html")); err != nil {
			return errors.New("UI build missing; run build-ui")
		}
		addr := os.Getenv("RELAY_ADDR")
		if addr == "" {
			addr = "127.0.0.1:18081"
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("Hub binds to loopback; expose through a TLS reverse proxy")
		}
		wa, err := whatsapp.New(nil)
		if err != nil {
			return err
		}
		wa.Events = whatsapp.PostgresEvents{URL: os.Getenv("DATABASE_URL")}
		workers := 4
		if configured := os.Getenv("RELAY_OUTBOX_WORKERS"); configured != "" {
			workers, err = strconv.Atoi(configured)
			if err != nil {
				return errors.New("invalid RELAY_OUTBOX_WORKERS")
			}
		}
		poolConfig, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
		if err != nil {
			return err
		}
		poolConfig.MaxConns = 3
		authPool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
		if err != nil {
			return err
		}
		defer authPool.Close()
		authStore := &auth.Store{DB: authPool}
		accountStore, err := accountstore.New(authPool, os.Getenv("RELAY_CREDENTIAL_KEY"))
		if err != nil {
			return err
		}
		if err = accountStore.ProtectLegacyConfig(context.Background()); err != nil {
			return errors.New("cannot protect stored channel configuration; existing data preserved")
		}
		if err = activateWhatsApp(context.Background(), wa, accountStore); err != nil {
			return errors.New("cannot activate database WhatsApp configuration")
		}

		runtimeRows, err := accountStore.Runtime(context.Background())
		if err != nil {
			return errors.New("cannot load channel credentials")
		}
		channelRegistry, err := channels.Load(runtimeRows)
		if err != nil {
			return errors.New("enabled channel configuration is invalid")
		}
		channelCallbacks := []channels.RuntimeCallback{}
		for _, row := range runtimeRows {
			if row.Channel != "Telegram" && row.Channel != "Email" {
				continue
			}
			get := func(key string) string {
				if v, ok := row.Config[key].(string); ok {
					return v
				}
				return ""
			}
			if get("core_api_key") != "" && get("core_api_secret") != "" && get("callback_secret") != "" {
				channelCallbacks = append(channelCallbacks, channels.RuntimeCallback{Account: row.AccountID, Site: row.Site, APIKey: get("core_api_key"), APISecret: get("core_api_secret"), CallbackSecret: get("callback_secret")})
			}
		}
		channelEvents := channels.Store{DB: authPool}
		channelCallbacksDispatcher := &channels.CallbackDispatcher{URL: os.Getenv("DATABASE_URL")}
		callbacks := &whatsapp.CallbackDispatcher{URL: os.Getenv("DATABASE_URL"), Service: wa}
		wa.Outbox, err = whatsapp.NewOutbox(context.Background(), os.Getenv("DATABASE_URL"), wa, workers)
		if err != nil {
			return err
		}
		defer wa.Outbox.Close()
		accounts := []string{}
		for _, a := range wa.AccountsSnapshot() {
			accounts = append(accounts, a.ID)
		}
		browserAuth, err := auth.NewHTTP(authStore, os.Getenv("RELAY_PUBLIC_ORIGIN"), os.Getenv("RELAY_PUBLIC_PREFIX"), accounts)
		if err != nil {
			return errors.New("valid RELAY_PUBLIC_ORIGIN (HTTPS or loopback HTTP) and optional /hub RELAY_PUBLIC_PREFIX required")
		}

		handler, err := hubmcp.NewAuthenticatedChannels(func(c context.Context) error { return check(c, true) }, wa, authStore.Authenticate, channelRegistry, channelEvents)
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.Handle("GET /auth/", browserAuth.UI(ui))
		mux.Handle("POST /auth/", browserAuth.Headers(browserAuth))
		mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(ui, "assets")))))
		mux.Handle("GET /api/accounts", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, e := browserAuth.Session(r)
			if e != nil {
				http.Error(w, "unauthorized", 401)
				return
			}
			if u.Role != "admin" {
				http.Error(w, "forbidden", 403)
				return
			}
			query, after, before := r.URL.Query().Get("query"), r.URL.Query().Get("after"), r.URL.Query().Get("before")
			if len(query) > 80 || len(after) > 100 || len(before) > 100 || (after != "" && before != "") {
				http.Error(w, "invalid page", 400)
				return
			}
			var rows []accountstore.Account
			var next string
			var previous bool
			if before != "" {
				rows, previous, e = accountStore.PageBefore(r.Context(), query, before, 30)
			} else {
				rows, next, e = accountStore.Page(r.Context(), query, after, 30)
			}
			if e != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			cookie, _ := r.Cookie(auth.Cookie)
			total, enabled, e := accountStore.Summary(r.Context())
			if e != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"accounts": rows, "next": next, "previous": previous, "total": total, "enabled_channels": enabled, "csrf": auth.CSRF(cookie.Value)})
		})))
		mux.Handle("GET /api/accounts/health", browserAuth.Protect(accountHealth(authPool, browserAuth.Session)))
		connections := accountConnection(channelRegistry, browserAuth.Session, os.Getenv("RELAY_PUBLIC_ORIGIN"), os.Getenv("RELAY_PUBLIC_ORIGIN")+os.Getenv("RELAY_PUBLIC_PREFIX"), os.Getenv("RELAY_TELEGRAM_WEBHOOK_SECRET"))
		mux.Handle("GET /api/accounts/{account}/{channel}/connection", browserAuth.Protect(connections))
		mux.Handle("POST /api/accounts/{account}/{channel}/connection", browserAuth.Protect(connections))
		mux.Handle("GET /api/accounts/metadata", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, err := browserAuth.Session(r)
			if err != nil {
				http.Error(w, "unauthorized", 401)
				return
			}
			if user.Role != "admin" {
				http.Error(w, "forbidden", 403)
				return
			}
			account := r.URL.Query().Get("account")
			if account == "" || len(account) > 100 {
				http.Error(w, "invalid account", 400)
				return
			}
			if wa.Metadata == nil {
				http.Error(w, "metadata unavailable", 503)
				return
			}
			result, err := wa.Metadata.Read(r.Context(), account)
			if err != nil {
				http.Error(w, "metadata unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(result)
		})))
		mux.Handle("POST /api/accounts", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, e := browserAuth.Session(r)
			if e != nil {
				http.Error(w, "unauthorized", 401)
				return
			}
			if u.Role != "admin" {
				http.Error(w, "forbidden", 403)
				return
			}
			cookie, ce := r.Cookie(auth.Cookie)
			if ce != nil || !hmac.Equal([]byte(r.Header.Get("X-Relay-CSRF")), []byte(auth.CSRF(cookie.Value))) {
				http.Error(w, "invalid csrf", 403)
				return
			}
			if r.Header.Get("Origin") != os.Getenv("RELAY_PUBLIC_ORIGIN") {
				http.Error(w, "invalid origin", 403)
				return
			}
			var in struct {
				Account accountstore.Account `json:"account"`
				Create  bool                 `json:"create"`
				Secrets map[string]string    `json:"secrets"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&in) != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			if err := validateChannelAccount(r.Context(), accountStore, in.Account, in.Secrets); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for index := range in.Account.Channels {
				channel := &in.Account.Channels[index]
				if channel.Channel != "WhatsApp" {
					continue
				}
				delete(channel.Config, "connection_status")
				delete(channel.Config, "verified_at")
				if !in.Account.Enabled || !channel.Enabled {
					channel.Config["connection_status"] = "disabled"
					continue
				}
				stored := accountstore.RuntimeCredential{Config: map[string]any{}}
				if current, credentialErr := accountStore.Credential(r.Context(), in.Account.ID, "WhatsApp"); credentialErr == nil {
					stored = current
				} else if !errors.Is(credentialErr, pgx.ErrNoRows) {
					http.Error(w, "account validation unavailable", http.StatusServiceUnavailable)
					return
				}
				candidate := whatsappCandidate(in.Account, *channel, in.Secrets["WhatsApp"], stored)
				if e := wa.ValidateCredentials(r.Context(), candidate); e != nil {
					http.Error(w, "WhatsApp credentials could not be verified with Meta", http.StatusBadRequest)
					return
				}
				channel.Config["connection_status"] = "connected"
				channel.Config["verified_at"] = time.Now().UTC().Format(time.RFC3339)
			}
			save := accountStore.Save
			if in.Create {
				save = accountStore.Create
			}
			if e := save(r.Context(), in.Account, in.Secrets); e != nil {
				http.Error(w, "account rejected", 400)
				return
			}
			w.WriteHeader(204)
		})))
		mux.Handle("POST /api/accounts/reload", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, e := browserAuth.Session(r)
			if e != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if u.Role != "admin" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			cookie, ce := r.Cookie(auth.Cookie)
			if ce != nil || !hmac.Equal([]byte(r.Header.Get("X-Relay-CSRF")), []byte(auth.CSRF(cookie.Value))) || r.Header.Get("Origin") != os.Getenv("RELAY_PUBLIC_ORIGIN") {
				http.Error(w, "invalid request", http.StatusForbidden)
				return
			}
			configured, e := runtimeWhatsApp(r.Context(), accountStore)
			var nextChannels *channels.Registry
			var channelRows []accountstore.RuntimeCredential
			if e == nil {
				channelRows, e = accountStore.Runtime(r.Context())
			}
			if e == nil {
				nextChannels, e = channels.Load(channelRows)
			}
			if e == nil {
				e = wa.ReplaceAccounts(configured)
			}
			if e != nil {
				http.Error(w, "runtime reload rejected", http.StatusBadRequest)
				return
			}
			// MCP, webhook and admin handlers share this stable registry object.
			// Publish validated credentials atomically so reload/revocation also
			// takes effect for handlers constructed before this request.
			if e = channelRegistry.Replace(nextChannels); e != nil {
				http.Error(w, "runtime reload rejected", http.StatusBadRequest)
				return
			}
			channelCallbacksDispatcher.Update(runtimeCallbacks(channelRows))
			w.WriteHeader(http.StatusNoContent)
		})))
		mux.Handle("GET /api/audit", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, e := browserAuth.Session(r)
			if e != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if u.Role != "admin" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			limit := 50
			if raw := r.URL.Query().Get("limit"); raw != "" {
				if n, parseErr := strconv.Atoi(raw); parseErr == nil && n > 0 && n <= 100 {
					limit = n
				}
			}
			var cursor int64
			if raw := r.URL.Query().Get("before"); raw != "" {
				cursor, e = strconv.ParseInt(raw, 10, 64)
				if e != nil || cursor < 0 {
					http.Error(w, "invalid cursor", http.StatusBadRequest)
					return
				}
			}
			account := strings.TrimSpace(r.URL.Query().Get("account"))
			action := strings.TrimSpace(r.URL.Query().Get("action"))
			outcome := strings.TrimSpace(r.URL.Query().Get("outcome"))
			if len(account) > 100 || len(action) > 120 || len(outcome) > 20 {
				http.Error(w, "invalid filter", http.StatusBadRequest)
				return
			}
			rows, e := authPool.Query(r.Context(), `
				SELECT id,occurred_at::text,actor_type,actor_id,account_id,channel,action,outcome,request_id,trace_id,metadata,error_code,error_message
				FROM relay_operational_audit
				WHERE ($1::bigint=0 OR id<$1) AND ($2='' OR account_id=$2) AND ($3='' OR action=$3) AND ($4='' OR outcome=$4)
				ORDER BY id DESC LIMIT $5`, cursor, account, action, outcome, limit+1)
			if e != nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			defer rows.Close()
			type auditEntry struct {
				ID           int64          `json:"id"`
				OccurredAt   string         `json:"occurred_at"`
				ActorType    string         `json:"actor_type"`
				ActorID      *int64         `json:"actor_id,omitempty"`
				AccountID    *string        `json:"account_id,omitempty"`
				Channel      *string        `json:"channel,omitempty"`
				Action       string         `json:"action"`
				Outcome      string         `json:"outcome"`
				RequestID    *string        `json:"request_id,omitempty"`
				TraceID      *string        `json:"trace_id,omitempty"`
				Metadata     map[string]any `json:"metadata"`
				ErrorCode    string         `json:"error_code,omitempty"`
				ErrorMessage string         `json:"error_message,omitempty"`
			}
			entries := make([]auditEntry, 0, limit)
			for rows.Next() {
				var v auditEntry
				var raw []byte
				if e = rows.Scan(&v.ID, &v.OccurredAt, &v.ActorType, &v.ActorID, &v.AccountID, &v.Channel, &v.Action, &v.Outcome, &v.RequestID, &v.TraceID, &raw, &v.ErrorCode, &v.ErrorMessage); e != nil {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if len(raw) > 0 && json.Unmarshal(raw, &v.Metadata) != nil {
					v.Metadata = map[string]any{}
				}
				entries = append(entries, v)
			}
			if e = rows.Err(); e != nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			more := len(entries) > limit
			if more {
				entries = entries[:limit]
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"events": entries, "more": more})
		})))
		mux.Handle("GET /api/ready", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, stop := context.WithTimeout(r.Context(), 3*time.Second)
			defer stop()
			if check(c, true) != nil {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(204)
		})))
		mux.Handle("GET /api/channels", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, e := browserAuth.Session(r)
			if e != nil {
				http.Error(w, "unauthorized", 401)
				return
			}
			if u.Role != "admin" {
				http.Error(w, "forbidden", 403)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			status := channelRegistry.Status()
			json.NewEncoder(w).Encode(map[string]any{"telegram": status.Telegram, "email": status.Email, "whatsapp": len(wa.AccountsSnapshot())})
		})))
		mux.Handle("GET /api/event-inspector", browserAuth.Protect(eventInspector(postgresEventReader{db: authPool}, browserAuth.Session)))
		mux.Handle("GET /api/channel-events", browserAuth.Protect(channelEventsAdmin(authPool, browserAuth.Session)))
		mux.Handle("GET /api/callback-failures", browserAuth.Protect(callbackFailuresAdmin(authPool, browserAuth.Session)))
		mux.Handle("POST /api/callback-failures/retry", browserAuth.Protect(callbackRetryAdmin(channelCallbacksDispatcher.RecoverCallback, browserAuth.Session, os.Getenv("RELAY_PUBLIC_ORIGIN"), callbacks.RecoverCallback)))
		deliveryUI := deliveryAdmin(hubDeliveryBackend{wa: wa.Outbox, channels: channels.Outbox{DB: authPool, Registry: channelRegistry}}, browserAuth.Session, os.Getenv("RELAY_PUBLIC_ORIGIN"))
		mux.Handle("GET /api/delivery-queue", browserAuth.Protect(deliveryUI))
		mux.Handle("POST /api/delivery-queue", browserAuth.Protect(deliveryUI))
		mux.Handle("GET /api/deliveries", browserAuth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, e := browserAuth.Session(r)
			if e != nil {
				http.Error(w, "unauthorized", 401)
				return
			}
			if u.Role != "admin" {
				http.Error(w, "forbidden", 403)
				return
			}
			rows, e := authPool.Query(r.Context(), `SELECT state,count(*) FROM (SELECT state FROM relay_outbox UNION ALL SELECT state FROM relay_channel_outbox) deliveries GROUP BY state ORDER BY state`)
			if e != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			defer rows.Close()
			counts := map[string]int64{}
			for rows.Next() {
				var state string
				var count int64
				if rows.Scan(&state, &count) != nil {
					http.Error(w, "unavailable", 503)
					return
				}
				counts[state] = count
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"counts": counts})
		})))
		// Return a bot-specific registration credential only to Hub admins. This
		// endpoint never returns the master key or the Telegram Bot API token.
		mux.Handle("GET /api/accounts/{account}/telegram/webhook", browserAuth.Protect(telegramWebhookSettings(channelRegistry, os.Getenv("RELAY_TELEGRAM_WEBHOOK_SECRET"), browserAuth.Session)))
		mux.HandleFunc("POST /webhooks/telegram/{account}", func(w http.ResponseWriter, r *http.Request) {
			account := r.PathValue("account")
			raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if readErr != nil {
				http.Error(w, "invalid body", 413)
				return
			}
			if e := channelRegistry.TelegramWebhook(r.Context(), channelEvents, account, os.Getenv("RELAY_TELEGRAM_WEBHOOK_SECRET"), []byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), raw); e != nil {
				http.Error(w, "webhook rejected", 403)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.Handle("GET /api/accounts/{account}/email/webhook", browserAuth.Protect(channelWebhookSettings("email", channelRegistry.EmailWebhookSecret, os.Getenv("RELAY_EMAIL_WEBHOOK_SECRET"), browserAuth.Session)))
		mux.HandleFunc("POST /webhooks/email/{account}", func(w http.ResponseWriter, r *http.Request) {
			account := r.PathValue("account")
			raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 12<<20))
			if readErr != nil {
				http.Error(w, "invalid body", 413)
				return
			}
			if e := channelRegistry.EmailWebhook(r.Context(), channelEvents, account, os.Getenv("RELAY_EMAIL_WEBHOOK_SECRET"), []byte(r.Header.Get("X-Relay-Email-Secret")), raw); e != nil {
				http.Error(w, "webhook rejected", 403)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("GET /webhooks/whatsapp/{account}", wa.Webhook)
		mux.HandleFunc("POST /webhooks/whatsapp/{account}", wa.Webhook)
		mux.HandleFunc("GET /webhook/whatsapp", wa.Webhook)
		mux.HandleFunc("POST /webhook/whatsapp", wa.Webhook)
		mux.Handle("POST /mcp", handler)
		mux.Handle("GET /mcp", handler)
		mux.Handle("DELETE /mcp", handler)
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "relay_hub"})
		})
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
			c, stop := context.WithTimeout(r.Context(), 3*time.Second)
			defer stop()
			w.Header().Set("Content-Type", "application/json")
			if err := check(c, true); err != nil {
				w.WriteHeader(503)
				json.NewEncoder(w).Encode(map[string]string{"status": "not_ready"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ready", "database": "PostgreSQL 18", "instance": os.Getenv("RELAY_INSTANCE")})
		})
		mux.Handle("GET /", browserAuth.Protect(http.FileServer(http.Dir(ui))))
		server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: httpWriteTimeout, IdleTimeout: 60 * time.Second}
		mailboxPool, err := channels.NewMailboxPool(context.Background(), authPool.Config())
		if err != nil {
			return err
		}
		defer mailboxPool.Close()
		shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		wa.Metadata = &whatsapp.MetadataSync{DB: authPool, Service: wa}
		wa.Metadata.Run(shutdown)
		wa.Outbox.Run(shutdown)
		channels.Outbox{DB: authPool, Registry: channelRegistry}.Run(shutdown)
		channels.MailboxPoller{DB: mailboxPool, Registry: channelRegistry}.Run(shutdown)
		callbacks.Run(shutdown)
		channelCallbacksDispatcher.Run(shutdown, channelCallbacks)
		go func() {
			<-shutdown.Done()
			c, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			server.Shutdown(c)
		}()
		log.Printf("Relay Hub listening on %s", addr)
		err = server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	default:
		return errors.New("unknown command")
	}
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
