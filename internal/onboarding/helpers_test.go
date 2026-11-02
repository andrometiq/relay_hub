package onboarding

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

type harness struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	store   *containers.Store
	auth    *auth.Store
	clock   *clock
	meta    *fakeMeta
	core    *fakeCore
	service *Service
	reloads int
	mu      sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RELAY_TEST_DATABASE_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("relay_onboarding_test_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); base.Close() })
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	store, _ := containers.New(pool, "onboarding-test-key")
	h := &harness{t: t, ctx: ctx, pool: pool, store: store, auth: &auth.Store{DB: pool}, clock: &clock{at: time.Now().UTC()}}
	h.meta = newFakeMeta(t)
	h.core = newFakeCore(t)
	h.service = h.newService()
	return h
}

func (h *harness) newService() *Service {
	service, err := New(Config{DB: h.pool, Store: h.store, Auth: h.auth, Client: &http.Client{Timeout: 5 * time.Second},
		PublicOrigin: "https://hub.example.test", GraphBase: h.meta.server.URL, Now: h.clock.now,
		Reload: func(context.Context) error { h.mu.Lock(); h.reloads++; h.mu.Unlock(); return nil }})
	if err != nil {
		h.t.Fatal(err)
	}
	return service
}

// signupContainer stores the signup container with the fake Meta app.
func (h *harness) signupContainer() {
	h.t.Helper()
	app := &containers.ProviderInput{Config: map[string]any{"app_id": "333", "graph_version": "v25.0", "es_config_id": "es-1", "app_secret": "app-secret", "verify_token": "verify"}}
	if err := h.store.SaveContainer(h.ctx, containers.Container{ID: "signup", Name: "Signup", Enabled: true, Signup: true}, app, true); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) token(kind, pattern string, maxSites *int) string {
	h.t.Helper()
	_, secret, err := h.service.CreateToken(h.ctx, auth.User{}, TokenInput{Name: "test " + kind, Kind: kind, OriginPattern: pattern, MaxSites: maxSites})
	if err != nil {
		h.t.Fatal(err)
	}
	return secret
}

func (h *harness) enrolRequest() EnrolRequest {
	return EnrolRequest{Origin: h.core.server.URL, SiteName: "acme.localhost", DisplayName: "Acme Traders", CoreAPIKey: "key", CoreAPISecret: "secret",
		CallbackSecret: h.core.callbackSecret, Nonce: strings.Repeat("ab", 32), CoreVersion: "1.0.0"}
}

// enrolledSite enrols the fake Core site and returns its site id and Hub token.
func (h *harness) enrolledSite() (string, string) {
	h.t.Helper()
	result, err := h.service.Enrol(h.ctx, h.token("bench", h.core.server.URL, nil), h.enrolRequest(), "127.0.0.1")
	if err != nil {
		h.t.Fatal(err)
	}
	return result.SiteID, result.HubToken
}

func (h *harness) session(id string) record {
	h.t.Helper()
	r, err := loadRecord(h.ctx, h.pool, id, false)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

type fakeCore struct {
	server         *httptest.Server
	callbackSecret string
	mu             sync.Mutex
	mode           string
	calls          int
}

func newFakeCore(t *testing.T) *fakeCore {
	core := &fakeCore{callbackSecret: strings.Repeat("c1", 32)}
	core.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		core.mu.Lock()
		mode := core.mode
		core.calls++
		core.mu.Unlock()
		if r.URL.Path != proofPath || r.Header.Get("Authorization") != "token key:secret" {
			http.Error(w, "forbidden", 403)
			return
		}
		if mode == "500" {
			http.Error(w, "down", 500)
			return
		}
		var in struct{ Nonce, Challenge string }
		json.NewDecoder(r.Body).Decode(&in)
		mac := hmac.New(sha256.New, []byte(core.callbackSecret))
		mac.Write([]byte(in.Nonce + ":" + in.Challenge + ":" + core.server.URL))
		proof := hex.EncodeToString(mac.Sum(nil))
		if mode == "wrong" {
			proof = strings.Repeat("0", 64)
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"proof": proof}})
	}))
	t.Cleanup(core.server.Close)
	return core
}

func (c *fakeCore) set(mode string) {
	c.mu.Lock()
	c.mode = mode
	c.mu.Unlock()
}

// fakeMeta fakes the Graph calls G1 to G10 for one WABA per code.
type fakeMeta struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  map[string]int
	faults map[string]int
	// phones maps a WABA to its numbers; onApp marks coexistence numbers.
	phones map[string][]string
	onApp  map[string]bool
	funded map[string]bool
	smb    map[string]int
}

func newFakeMeta(t *testing.T) *fakeMeta {
	m := &fakeMeta{calls: map[string]int{}, faults: map[string]int{}, phones: map[string][]string{"230": {"120"}, "231": {"121"}},
		onApp: map[string]bool{}, funded: map[string]bool{}, smb: map[string]int{}}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

func (m *fakeMeta) count(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[name]
}

func (m *fakeMeta) fault(name string, times int) {
	m.mu.Lock()
	m.faults[name] = times
	m.mu.Unlock()
}

func (m *fakeMeta) faulty(name string) bool {
	if m.faults[name] > 0 {
		m.faults[name]--
		return true
	}
	return false
}

// wabaOf is the WABA a business token may reach: biz-<code>, where code names the WABA.
func wabaOf(r *http.Request) string {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimPrefix(token, "biz-code-")
}

func metaFailure(w http.ResponseWriter, status, code, subcode int, message string) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "error_subcode": subcode, "message": message, "type": "OAuthException"}})
}

func (m *fakeMeta) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v25.0/"), "/")
	w.Header().Set("Content-Type", "application/json")
	name := parts[len(parts)-1]
	if len(parts) == 1 && m.phones[parts[0]] != nil {
		name = "waba"
	} else if len(parts) == 1 && parts[0] != "debug_token" {
		name = "phone"
	}
	if name == "subscribed_apps" && r.Method == http.MethodGet {
		name = "subscribed_check"
	}
	m.calls[name]++
	switch {
	case parts[0] == "oauth":
		code := r.URL.Query().Get("code")
		if code == "bad" || r.URL.Query().Get("client_secret") != "app-secret" {
			metaFailure(w, 400, 100, 36007, "Invalid verification code format.")
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "biz-" + code, "token_type": "bearer"})
	case parts[0] == "debug_token":
		if r.Header.Get("Authorization") != "Bearer 333|app-secret" {
			metaFailure(w, 400, 190, 0, "bad app token")
			return
		}
		waba := strings.TrimPrefix(r.URL.Query().Get("input_token"), "biz-code-")
		if m.faulty("no_scope") {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"app_id": "333", "is_valid": true, "granular_scopes": []any{}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"app_id": "333", "is_valid": true, "granular_scopes": []any{
			map[string]any{"scope": "whatsapp_business_management", "target_ids": []string{waba}},
			map[string]any{"scope": "whatsapp_business_messaging", "target_ids": []string{waba}}}}})
	case len(parts) == 1 && m.phones[parts[0]] != nil:
		if wabaOf(r) != parts[0] {
			metaFailure(w, 400, 200, 0, "Permissions error")
			return
		}
		result := map[string]any{"id": parts[0], "name": "Acme", "owner_business_info": map[string]any{"id": "330"}}
		if m.funded[parts[0]] {
			result["primary_funding_id"] = "fund-1"
		}
		json.NewEncoder(w).Encode(result)
	case len(parts) == 2 && parts[1] == "phone_numbers":
		rows := []any{}
		for _, id := range m.phones[parts[0]] {
			rows = append(rows, map[string]any{"id": id, "display_phone_number": "+1 555 0" + id, "verified_name": "Acme", "name_status": "APPROVED", "quality_rating": "GREEN", "whatsapp_business_manager_messaging_limit": "TIER_250"})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": rows})
	case len(parts) == 2 && parts[1] == "subscribed_apps":
		if r.Method == http.MethodPost && m.faulty("subscribe_503") {
			metaFailure(w, 503, 2, 0, "Service temporarily unavailable")
			return
		}
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	case len(parts) == 2 && parts[1] == "register":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if m.faulty("register_133016") {
			metaFailure(w, 400, 133016, 0, "Too many register attempts")
			return
		}
		if len(body["pin"]) != 6 {
			metaFailure(w, 400, 100, 0, "pin required")
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	case len(parts) == 2 && parts[1] == "smb_app_data":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		m.smb[parts[0]+":"+body["sync_type"]]++
		if m.smb[parts[0]+":"+body["sync_type"]] > 1 {
			metaFailure(w, 400, 4, 2593111, "Sync already requested")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "request_id": "req-1"})
	case len(parts) == 1:
		platform := "NOT_APPLICABLE"
		if m.onApp[parts[0]] {
			platform = "CLOUD_API"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": parts[0], "display_phone_number": "+1 555 0" + parts[0], "verified_name": "Acme", "platform_type": platform, "is_on_biz_app": m.onApp[parts[0]]})
	default:
		metaFailure(w, 404, 803, 0, "unknown path "+r.URL.Path)
	}
}
