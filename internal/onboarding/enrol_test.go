package onboarding

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
)

func failureCode(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func TestOriginPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, origin string
		allowed         bool
	}{
		{"https://*.example.com", "https://acme.example.com", true},
		{"https://*.example.com", "https://a.b.example.com", true},
		{"https://*.example.com", "https://example.com", false},
		{"https://*.example.com", "http://acme.example.com", false},
		{"https://*.example.com", "https://acme.example.com.evil.test", false},
		{"https://*.example.com", "https://acme.example.com:8443", false},
		{"http://*.localhost:8001", "http://shop.localhost:8001", true},
		{"https://shop.example.com", "https://shop.example.com", true},
		{"https://shop.example.com", "https://other.example.com", false},
		{"", "https://any.test", true},
	} {
		if got := originAllowed(tc.pattern, tc.origin); got != tc.allowed {
			t.Errorf("%s vs %s: %v", tc.pattern, tc.origin, got)
		}
	}
}

func TestEnrolmentCreatesSiteServiceUserAndOneToken(t *testing.T) {
	h := newHarness(t)
	token := h.token("bench", h.core.server.URL, nil)
	first, err := h.service.Enrol(h.ctx, token, h.enrolRequest(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ReEnrolled || first.MCPURL != "https://hub.example.test/mcp" || !strings.HasPrefix(first.HubToken, "relay_") || first.SiteID == "" {
		t.Fatalf("enrolment %+v", first)
	}
	principal, err := h.auth.Authenticate(h.ctx, first.HubToken)
	if err != nil || principal.Site != first.SiteID {
		t.Fatalf("site token %+v %v", principal, err)
	}
	site, err := h.store.GetSite(h.ctx, first.SiteID)
	if err != nil || site.Name != "Acme Traders" || site.Origin != h.core.server.URL || site.ActiveTokens != 1 || len(site.ConfiguredSecrets) != 3 {
		t.Fatalf("site %+v %v", site, err)
	}
	credentials, _ := h.store.SiteCredentials(h.ctx, first.SiteID)
	if credentials.CallbackSecret != h.core.callbackSecret {
		t.Fatal("callback secret not stored")
	}
	if _, err = h.auth.Login(h.ctx, "site:"+first.SiteID, "", "test"); !errors.Is(err, auth.ErrDenied) {
		t.Fatal("service user logged in")
	}

	again, err := h.service.Enrol(h.ctx, token, h.enrolRequest(), "127.0.0.1")
	if err != nil || !again.ReEnrolled || again.SiteID != first.SiteID {
		t.Fatalf("re-enrolment %+v %v", again, err)
	}
	if _, err = h.auth.Authenticate(h.ctx, first.HubToken); err == nil {
		t.Fatal("old site token still works after re-enrolment")
	}
	if _, err = h.auth.Authenticate(h.ctx, again.HubToken); err != nil {
		t.Fatal("new site token rejected")
	}
	var audits int
	h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_operational_audit WHERE action IN ('site.enrol','site.re_enrol') AND account_id=$1`, first.SiteID).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit rows %d", audits)
	}
}

func TestEnrolmentDenials(t *testing.T) {
	h := newHarness(t)
	token := h.token("bench", h.core.server.URL, nil)
	request := h.enrolRequest()
	if _, err := h.service.Enrol(h.ctx, "relay_enrol_"+strings.Repeat("0", 64), request, ""); failureCode(err) != "token_invalid" {
		t.Fatalf("unknown token: %v", err)
	}
	other := h.token("bench", "https://*.example.com", nil)
	if _, err := h.service.Enrol(h.ctx, other, request, ""); failureCode(err) != "origin_not_allowed" {
		t.Fatalf("origin outside pattern: %v", err)
	}
	h.core.set("wrong")
	if _, err := h.service.Enrol(h.ctx, token, request, ""); failureCode(err) != "proof_failed" {
		t.Fatalf("wrong proof: %v", err)
	}
	h.core.set("500")
	if _, err := h.service.Enrol(h.ctx, token, request, ""); failureCode(err) != "site_unreachable" {
		t.Fatalf("proof endpoint down: %v", err)
	}
	h.core.set("")
	badKey := request
	badKey.CoreAPISecret = "other"
	if _, err := h.service.Enrol(h.ctx, token, badKey, ""); failureCode(err) != "proof_failed" {
		t.Fatalf("Core refused the key: %v", err)
	}
	var sites int
	h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_sites`).Scan(&sites)
	if sites != 0 {
		t.Fatal("denied enrolment created a site")
	}
	h.pool.Exec(h.ctx, `UPDATE relay_enrolment_tokens SET expires_at=now()-interval '1 minute' WHERE token_hash=$1`, auth.Hash(token))
	if _, err := h.service.Enrol(h.ctx, token, request, ""); failureCode(err) != "token_expired" {
		t.Fatalf("expired token: %v", err)
	}
	h.pool.Exec(h.ctx, `UPDATE relay_enrolment_tokens SET revoked_at=now() WHERE token_hash=$1`, auth.Hash(token))
	if _, err := h.service.Enrol(h.ctx, token, request, ""); failureCode(err) != "token_revoked" {
		t.Fatalf("revoked token: %v", err)
	}
	insecure := request
	insecure.Origin = "http://shop.example.com"
	if _, err := h.service.Enrol(h.ctx, other, insecure, ""); failureCode(err) != "invalid_request" {
		t.Fatalf("plain HTTP origin: %v", err)
	}
}

func TestSetupCodeEnrolsOneSite(t *testing.T) {
	h := newHarness(t)
	code := h.token("setup_code", "", nil)
	if !strings.HasPrefix(code, setupPrefix) {
		t.Fatalf("setup code format %q", code)
	}
	if _, err := h.service.Enrol(h.ctx, code, h.enrolRequest(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Enrol(h.ctx, code, h.enrolRequest(), ""); err != nil {
		t.Fatal("re-enrolment of the same site with its setup code", err)
	}
	second := newFakeCore(t)
	request := h.enrolRequest()
	request.Origin, request.CallbackSecret = second.server.URL, second.callbackSecret
	if _, err := h.service.Enrol(h.ctx, code, request, ""); failureCode(err) != "token_used_up" {
		t.Fatalf("setup code on another site: %v", err)
	}
	tokens, _ := h.service.ListTokens(h.ctx)
	if len(tokens) != 1 || tokens[0].UsedSites != 1 || tokens[0].Status != "used" {
		t.Fatalf("token list %+v", tokens)
	}
}

func TestParallelEnrolmentGivesOneSiteAndOneLiveToken(t *testing.T) {
	h := newHarness(t)
	token := h.token("bench", h.core.server.URL, nil)
	var wg sync.WaitGroup
	results := make([]EnrolResult, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = h.service.Enrol(h.ctx, token, h.enrolRequest(), "")
		}()
	}
	wg.Wait()
	live := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if _, err := h.auth.Authenticate(h.ctx, results[i].HubToken); err == nil {
			live++
		}
	}
	var sites, users int
	h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_sites`).Scan(&sites)
	h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_users WHERE kind='service'`).Scan(&users)
	if sites != 1 || users != 1 || live != 1 {
		t.Fatalf("sites %d service users %d live tokens %d", sites, users, live)
	}
}

func TestEnrolHTTP(t *testing.T) {
	h := newHarness(t)
	mux := http.NewServeMux()
	h.service.Register(mux)
	post := func(token string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/enrol", bytes.NewReader(raw))
		r.RemoteAddr = "192.0.2.10:5000"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := post("", h.enrolRequest()); w.Code != 401 || !strings.Contains(w.Body.String(), `"token_invalid"`) {
		t.Fatalf("no token %d %s", w.Code, w.Body)
	}
	token := h.token("bench", h.core.server.URL, nil)
	if w := post(token, map[string]string{"origin": "nope"}); w.Code != 422 {
		t.Fatalf("invalid body %d", w.Code)
	}
	w := post(token, h.enrolRequest())
	var created EnrolResult
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.SiteID == "" || created.HubToken == "" || created.HubOrigin != "https://hub.example.test" {
		t.Fatalf("enrol %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("enrolment response may be cached")
	}
	for range 18 {
		post(token, map[string]string{})
	}
	if w = post(token, h.enrolRequest()); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit %d", w.Code)
	}
}
