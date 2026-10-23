// relay-sim is a loopback-only acceptance launcher, never a production server.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/andrometiq/relay_hub/internal/accounts"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/andrometiq/relay_hub/internal/envconfig"
	"github.com/andrometiq/relay_hub/internal/hubmcp"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"
)

func simulationDSN(dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid simulation database configuration")
	}
	if cfg.Database != "relay_hub_sim" || cfg.Host != "127.0.0.1" || cfg.Port != 18433 || len(cfg.Fallbacks) > 1 {
		return errors.New("DATABASE_URL must select relay_hub_sim at 127.0.0.1:18433")
	}
	for _, f := range cfg.Fallbacks {
		if f.Host != cfg.Host || f.Port != cfg.Port {
			return errors.New("nonlocal database fallback refused")
		}
	}
	if cfg.RuntimeParams["search_path"] != "" {
		return errors.New("custom search_path refused")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

type simulation struct {
	db            *pgxpool.Pool
	wa            *whatsapp.Service
	transport     *simTransport
	meta          *fakeMeta
	hub           *url.URL
	token         string
	credentialKey string
	registry      *channels.Registry
	callbacks     *channels.CallbackDispatcher
	telegram      *fakeTelegram
	email         *fakeEmail
}

func (s *simulation) resume(ctx context.Context) error {
	store, err := accounts.New(s.db, s.credentialKey)
	if err != nil {
		return err
	}
	rows, err := store.Runtime(ctx)
	if err != nil {
		return errors.New("cannot load simulation account")
	}
	for _, row := range rows {
		if row.AccountID != "sim-v1" || row.Channel != "WhatsApp" {
			continue
		}
		var a whatsapp.Account
		raw, err := json.Marshal(row.Config)
		if err != nil || json.Unmarshal(raw, &a) != nil {
			return errors.New("invalid simulation account configuration")
		}
		a.ID, a.Site, a.AccessToken = row.AccountID, row.Site, row.Secret
		if a.AccessToken != "sim-provider-token" || a.AppSecret != "sim-app-secret" || a.PhoneID != "110" || a.WABAID != "220" || a.Version != "v25.0" {
			return errors.New("refusing non-simulation provider configuration")
		}
		a.Groups, a.Calls, a.SyncMetadata = false, false, false
		s.transport.site = a.Site
		if err := s.activateChannels(a); err != nil {
			return err
		}
		return s.wa.ReplaceAccounts([]whatsapp.Account{a})
	}
	return errors.New("no enabled simulation account to resume")
}

func (s *simulation) control(w http.ResponseWriter, r *http.Request) {
	if !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) {
		http.Error(w, "unauthorized", 401)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_sim/telegram/") || strings.HasPrefix(r.URL.Path, "/_sim/email/") || r.URL.Path == "/_sim/providers" {
		s.providerControl(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/_sim/state" {
		state := map[string]any{}
		for key, query := range map[string]string{
			"channel_callbacks":  "SELECT d.event_id,d.acknowledged,d.failures FROM relay_channel_callback_delivery d JOIN relay_channel_events e ON e.id=d.event_id WHERE e.account='sim-v1' ORDER BY d.event_id",
			"channel_events":     "SELECT id,channel,provider_id,payload FROM relay_channel_events WHERE account='sim-v1' ORDER BY id",
			"channel_deliveries": "SELECT id,request_key,state,result FROM relay_channel_outbox WHERE account='sim-v1' ORDER BY id",
			"events":             "SELECT id,lane,payload FROM relay_whatsapp_events WHERE account='sim-v1' ORDER BY id",
			"incoming":           "SELECT id,provider_id FROM relay_whatsapp_incoming WHERE account='sim-v1' ORDER BY id",
			"deliveries":         "SELECT id,request_key,state,result->'messages'->0->>'id' AS provider_message_id FROM relay_outbox WHERE account='sim-v1' ORDER BY id",
			"callbacks":          "SELECT d.event_id,d.lane,d.acknowledged,d.status_offset,d.failures FROM relay_whatsapp_callback_delivery d JOIN relay_whatsapp_events e ON e.id=d.event_id WHERE e.account='sim-v1' ORDER BY d.event_id,d.lane",
		} {
			rows, err := s.db.Query(r.Context(), query)
			if err != nil {
				http.Error(w, "snapshot unavailable", 503)
				return
			}
			values, err := pgx.CollectRows(rows, pgx.RowToMap)
			if err != nil {
				http.Error(w, "snapshot unavailable", 503)
				return
			}
			state[key] = values
		}
		// Transport captures precede durable callback ACKs. Read them last so
		// every ACK observed above has its corresponding capture in this response.
		state["records"] = s.meta.records.snapshot()
		writeJSON(w, state)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Site             string          `json:"site"`
		APIKey           string          `json:"api_key"`
		APISecret        string          `json:"api_secret"`
		Secret           string          `json:"callback_secret"`
		Payload          json.RawMessage `json:"payload"`
		InvalidSignature bool            `json:"invalid_signature"`
		Target           string          `json:"target"`
		Faults           []string        `json:"faults"`
		Hold             bool            `json:"hold"`
		ID               string          `json:"id"`
		Status           string          `json:"status"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	switch r.URL.Path {
	case "/_sim/provision":
		u, err := url.Parse(in.Site)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || len(in.Secret) < 32 || in.APIKey == "" || in.APISecret == "" {
			http.Error(w, "invalid Core binding", 400)
			return
		}
		// The canonical site name is logical identity only; the transport dials core-origin.
		a := whatsapp.Account{ID: "sim-v1", Site: in.Site, AccessToken: "sim-provider-token", PhoneID: "110", WABAID: "220", Version: "v25.0", Templates: true, AppSecret: "sim-app-secret", VerifyToken: "sim-verify-token", CoreAPIKey: in.APIKey, CoreAPISecret: in.APISecret, CallbackSecret: in.Secret}
		store, err := accounts.New(s.db, s.credentialKey)
		if err != nil {
			http.Error(w, "account store unavailable", 503)
			return
		}
		err = store.Save(r.Context(), accounts.Account{ID: a.ID, Site: a.Site, Enabled: true, Channels: simulationChannels(a)}, map[string]string{"WhatsApp": a.AccessToken, "Telegram": simulationBotToken, "Email": simulationMailPassword})
		if err != nil {
			http.Error(w, "account provisioning failed", 503)
			return
		}
		authStore := &auth.Store{DB: s.db}
		var count int
		if err = s.db.QueryRow(r.Context(), "SELECT count(*) FROM relay_users").Scan(&count); err != nil {
			http.Error(w, "auth unavailable", 503)
			return
		}
		if count == 0 {
			if err = authStore.Bootstrap(r.Context(), "sim-admin", auth.Secret()); err != nil {
				http.Error(w, "bootstrap failed", 503)
				return
			}
		}
		var admin auth.User
		if err = s.db.QueryRow(r.Context(), "SELECT id,username,role,auth_version FROM relay_users WHERE username='sim-admin' AND enabled").Scan(&admin.ID, &admin.Username, &admin.Role, &admin.Version); err != nil {
			http.Error(w, "simulation admin unavailable", 503)
			return
		}
		if err = authStore.Grant(r.Context(), admin, admin.ID, a.ID, true); err != nil {
			http.Error(w, "grant failed", 503)
			return
		}
		rows, err := s.db.Query(r.Context(), "SELECT id FROM relay_api_tokens WHERE user_id=$1 AND account_id=$2 AND name='simulation' AND revoked_at IS NULL", admin.ID, a.ID)
		if err != nil {
			http.Error(w, "token rotation unavailable", 503)
			return
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			http.Error(w, "token rotation unavailable", 503)
			return
		}
		for _, id := range ids {
			if err = authStore.Revoke(r.Context(), admin, id); err != nil {
				http.Error(w, "token rotation failed", 503)
				return
			}
		}
		token, err := authStore.Issue(r.Context(), admin, "simulation", a.ID, []string{"account:read", "messages:send", "messages:read", "deliveries:read", "templates:read", "templates:write"}, 1)
		if err != nil {
			http.Error(w, "token issue failed", 503)
			return
		}
		s.transport.mu.Lock()
		s.transport.site = a.Site
		s.transport.mu.Unlock()
		if err = s.wa.ReplaceAccounts([]whatsapp.Account{a}); err != nil {
			http.Error(w, "activation failed", 503)
			return
		}
		if err = s.activateChannels(a); err != nil {
			http.Error(w, "channel activation failed", 503)
			return
		}
		writeJSON(w, map[string]any{"token": token, "account": a.ID})
	case "/_sim/faults":
		if in.Target != "status" && in.Target != "graph" {
			http.Error(w, "invalid fault target", 400)
			return
		}
		for _, f := range in.Faults {
			if f != "429" && f != "503" && !(f == "wrong_ack" && in.Target == "status") {
				http.Error(w, "invalid fault", 400)
				return
			}
		}
		s.meta.records.mu.Lock()
		s.meta.records.faults[in.Target] = in.Faults
		if in.Target == "status" {
			s.meta.records.hold = in.Hold
		}
		s.meta.records.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	case "/_sim/emit":
		if !json.Valid(in.Payload) {
			http.Error(w, "invalid payload", 400)
			return
		}
		s.emit(w, r, in.Payload, in.InvalidSignature)
	case "/_sim/template":
		if in.Status != "APPROVED" && in.Status != "REJECTED" && in.Status != "PENDING" {
			http.Error(w, "invalid status", 400)
			return
		}
		value, ok := s.meta.approve(in.ID, in.Status)
		if !ok {
			http.Error(w, "unknown template", 404)
			return
		}
		payload, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "220", "changes": []any{map[string]any{"field": "message_template_status_update", "value": value}}}}})
		s.emit(w, r, payload, false)
	default:
		http.NotFound(w, r)
	}
}
func (s *simulation) emit(w http.ResponseWriter, r *http.Request, payload []byte, invalid bool) {
	req, _ := http.NewRequestWithContext(r.Context(), "POST", s.hub.String()+"/webhooks/whatsapp/sim-v1", bytes.NewReader(payload))
	mac := hmac.New(sha256.New, []byte("sim-app-secret"))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))
	if invalid {
		sig = "invalid"
	}
	req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	var monotonic unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &monotonic); err != nil {
		http.Error(w, "clock unavailable", 503)
		return
	}
	client := &http.Client{Transport: s.transport.base, Timeout: 10 * time.Second, CheckRedirect: denyRedirect}
	res, err := client.Do(req)
	if err != nil {
		http.Error(w, "webhook failed", 502)
		return
	}
	defer res.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(res.Body, 65536))
	s.meta.records.record(req, "webhook", payload, res.StatusCode, reply, started, "")
	writeJSON(w, map[string]any{"status": res.StatusCode, "started_ns": started.Sub(s.meta.records.start).Nanoseconds(), "monotonic_ns": monotonic.Nano()})
}
func denyRedirect(*http.Request, []*http.Request) error {
	return errors.New("simulation refuses redirects")
}
func run() error { return runSimulation(simulationDSN) }

func runSimulation(validateDatabase func(string) error) error {
	addr := flag.String("addr", env("RELAY_SIM_ADDR", "127.0.0.1:18091"), "Hub loopback address")
	metaAddr := flag.String("meta-addr", env("RELAY_SIM_META_ADDR", "127.0.0.1:18094"), "fake Meta/control loopback address")
	coreOrigin := flag.String("core-origin", env("RELAY_SIM_CORE_ORIGIN", "http://127.0.0.1:8001"), "Core loopback origin")
	smtpAddr := flag.String("smtp-addr", "127.0.0.1:18095", "fake SMTP loopback address")
	imapAddr := flag.String("imap-addr", "127.0.0.1:18096", "fake IMAP loopback address")
	stateDir := flag.String("state-dir", ".runtime/relay-sim-state", "private provider state directory; reuse on restart")
	migrate := flag.Bool("migrate", false, "apply Hub migrations to relay_hub_sim")
	resume := flag.Bool("resume", false, "resume the stored simulation account without provisioning")
	flag.Parse()
	if _, err := os.Stat(".env"); err == nil {
		if err = envconfig.Load(".env"); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect project .env")
	}
	hub, err := localURL("http://" + *addr)
	if err != nil {
		return err
	}
	meta, err := localURL("http://" + *metaAddr)
	if err != nil {
		return err
	}
	core, err := localURL(*coreOrigin)
	if err != nil {
		return err
	}
	if len(os.Getenv("RELAY_SIM_CONTROL_TOKEN")) < 32 {
		return errors.New("RELAY_SIM_CONTROL_TOKEN requires at least 32 characters")
	}
	dsn := os.Getenv("DATABASE_URL")
	if err = validateDatabase(dsn); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return errors.New("simulation database connection failed")
	}
	if *migrate {
		err = database.Migrate(ctx, conn)
	} else {
		err = database.Ready(ctx, conn)
	}
	conn.Close(ctx)
	if err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	cfg.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("simulation pool failed")
	}
	defer pool.Close()
	if err = os.MkdirAll(*stateDir, 0700); err != nil {
		return errors.New("provider state directory unavailable")
	}
	records := newRecorder()
	fakeTelegram, err := newFakeTelegram(filepath.Join(*stateDir, "telegram.json"), records)
	if err != nil {
		return err
	}
	fakeEmail, err := newFakeEmail(filepath.Join(*stateDir, "email.json"), records)
	if err != nil {
		return err
	}
	if err = fakeEmail.start(*smtpAddr, *imapAddr); err != nil {
		return err
	}
	defer fakeEmail.close()
	transport := newTransport(meta, core, records)
	defer transport.base.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: denyRedirect}
	wa, err := whatsapp.NewWithHTTPClient(nil, client)
	if err != nil {
		return err
	}
	wa.Events = whatsapp.PostgresEvents{URL: dsn}
	wa.Outbox, err = whatsapp.NewOutbox(ctx, dsn, wa, 1)
	if err != nil {
		return err
	}
	defer wa.Outbox.Close()
	authStore := &auth.Store{DB: pool}
	registry, err := channels.Load(nil)
	if err != nil {
		return err
	}
	handler, err := hubmcp.NewAuthenticatedChannels(pool.Ping, wa, authStore.Authenticate, registry, channels.Store{DB: pool})
	if err != nil {
		return err
	}
	browser, err := auth.NewHTTP(authStore, hub.String(), "", []string{"sim-v1"})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/auth/", browser.Headers(browser))
	mux.HandleFunc("/webhooks/whatsapp/{account}", wa.Webhook)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if pool.Ping(r.Context()) != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		writeJSON(w, map[string]bool{"ready": true})
	})
	fake := &fakeMeta{records: records, pageSize: 1}
	sim := &simulation{db: pool, wa: wa, transport: transport, meta: fake, hub: hub, token: os.Getenv("RELAY_SIM_CONTROL_TOKEN"), credentialKey: os.Getenv("RELAY_CREDENTIAL_KEY"), registry: registry, telegram: fakeTelegram, email: fakeEmail, callbacks: &channels.CallbackDispatcher{URL: dsn}}
	sim.callbacks.Run(ctx, nil)
	mux.HandleFunc("POST /webhooks/telegram/{account}", sim.channelWebhook("telegram"))
	mux.HandleFunc("POST /webhooks/email/{account}", sim.channelWebhook("email"))
	if sim.credentialKey == "" {
		return errors.New("RELAY_CREDENTIAL_KEY is required")
	}
	if *resume {
		if err := sim.resume(ctx); err != nil {
			return err
		}
	}
	mm := http.NewServeMux()
	mm.HandleFunc("/_sim/", sim.control)
	mm.Handle("/ai/chat/completions", &fakeAI{records: records})
	mm.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bot") || strings.HasPrefix(r.URL.Path, "/file/bot") {
			fakeTelegram.ServeHTTP(w, r)
		} else {
			fake.ServeHTTP(w, r)
		}
	})
	servers := []*http.Server{{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}, {Addr: *metaAddr, Handler: mm, ReadHeaderTimeout: 5 * time.Second}}
	listeners := []net.Listener{}
	for _, server := range servers {
		l, e := net.Listen("tcp", server.Addr)
		if e != nil {
			for _, old := range listeners {
				old.Close()
			}
			return errors.New("simulation port unavailable")
		}
		listeners = append(listeners, l)
	}
	failures := make(chan error, 2)
	for i, server := range servers {
		go func(s *http.Server, l net.Listener) { failures <- s.Serve(l) }(server, listeners[i])
	}
	mailboxPool, err := channels.NewMailboxPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer mailboxPool.Close()
	channels.Outbox{DB: pool, Registry: registry}.Run(ctx)
	channels.MailboxPoller{DB: mailboxPool, Registry: registry}.Run(ctx)
	wa.Outbox.Run(ctx)
	(&whatsapp.CallbackDispatcher{URL: dsn, Service: wa}).Run(ctx)
	fmt.Println("relay-sim ready (loopback only)")
	select {
	case <-ctx.Done():
	case err = <-failures:
		cancel()
	}
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	for _, server := range servers {
		server.Shutdown(shutdown)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
