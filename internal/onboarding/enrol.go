package onboarding

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/jackc/pgx/v5"
)

// EnrolRequest is what a Core site sends to link itself to the Hub.
type EnrolRequest struct {
	Origin         string `json:"origin"`
	SiteName       string `json:"site_name"`
	DisplayName    string `json:"display_name"`
	CoreAPIKey     string `json:"core_api_key"`
	CoreAPISecret  string `json:"core_api_secret"`
	CallbackSecret string `json:"callback_secret"`
	Nonce          string `json:"nonce"`
	CoreVersion    string `json:"relay_core_version"`
}

// EnrolResult is returned once; hub_token is the site's MCP token.
type EnrolResult struct {
	SiteID     string `json:"site_id"`
	MCPURL     string `json:"mcp_url"`
	HubToken   string `json:"hub_token"`
	HubOrigin  string `json:"hub_origin"`
	ReEnrolled bool   `json:"re_enrolled"`
}

// Failure is a coded error with safe text, returned to Core and the Hub window.
type Failure struct {
	Status     int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter int    `json:"-"`
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

func fail(status int, code, message string) *Failure {
	return &Failure{Status: status, Code: code, Message: message}
}

var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var siteIDRun = regexp.MustCompile(`[^a-z0-9]+`)

const proofPath = "/api/method/relay_core.onboarding.api.enrolment_proof"

type enrolmentToken struct {
	id                 int64
	kind, pattern      string
	maxSites           *int
	usedSites          int
	expired            bool
	createdBy          *int64
	originAlreadyKnown bool
}

func (s *Service) enrol(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(token, enrolPrefix) && !strings.HasPrefix(token, setupPrefix) {
		writeFailure(w, fail(401, "token_invalid", "The enrolment token or setup code is not valid."))
		return
	}
	if ok, wait := s.limiter.allow(s.now(), "token:"+auth.Hash(token), "ip:"+clientIP(r)); !ok {
		failure := fail(429, "rate_limited", "Too many enrolment attempts. Try again later.")
		failure.RetryAfter = int(wait.Seconds()) + 1
		writeFailure(w, failure)
		return
	}
	var in EnrolRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&in) != nil {
		writeFailure(w, fail(422, "invalid_request", "The enrolment request is not valid."))
		return
	}
	result, err := s.Enrol(r.Context(), token, in, clientIP(r))
	if err != nil {
		writeFailure(w, asFailure(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result)
}

// Enrol checks the token and the proof that the caller serves the origin, then
// creates or updates the site, its service user and one live site token.
func (s *Service) Enrol(ctx context.Context, token string, in EnrolRequest, ip string) (EnrolResult, error) {
	origin, err := normalizeEnrolment(&in)
	if err != nil {
		return EnrolResult{}, err
	}
	conn, err := s.cfg.DB.Acquire(ctx)
	if err != nil {
		return EnrolResult{}, unavailable()
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('relay-enrol:'||$1,0))`, origin); err != nil {
		return EnrolResult{}, unavailable()
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('relay-enrol:'||$1,0))`, origin)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return EnrolResult{}, unavailable()
	}
	defer tx.Rollback(context.Background())
	enrolment, err := s.checkToken(ctx, tx, token, origin)
	if err != nil {
		return EnrolResult{}, err
	}
	if err = s.proveOrigin(ctx, origin, in); err != nil {
		return EnrolResult{}, err
	}
	result, err := s.saveEnrolment(ctx, tx, enrolment, origin, in, ip)
	if err != nil {
		return EnrolResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EnrolResult{}, unavailable()
	}
	// The new credentials are saved and the old token is revoked, so Core must get them even if the
	// runtime reload fails; a failure here would leave Core and the Hub on different secrets.
	if err = s.cfg.Reload(context.WithoutCancel(ctx)); err != nil {
		log.Printf("enrol %s: runtime reload failed: %v", result.SiteID, err)
	}
	return result, nil
}

func unavailable() *Failure {
	return fail(503, "hub_unavailable", "Relay Hub is not available. Try again in a few minutes.")
}

// normalizeEnrolment validates the request and returns the site origin.
func normalizeEnrolment(in *EnrolRequest) (string, error) {
	invalid := fail(422, "invalid_request", "The enrolment request is not valid.")
	u, err := url.Parse(strings.TrimSpace(in.Origin))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(in.Origin) > 300 {
		return "", invalid
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopbackHost(u.Hostname())) {
		return "", fail(422, "origin_not_https", "Relay Hub needs the site's public HTTPS address.")
	}
	in.SiteName, in.DisplayName = strings.TrimSpace(in.SiteName), strings.TrimSpace(in.DisplayName)
	if in.SiteName == "" || len(in.SiteName) > 200 || len(in.DisplayName) > 200 || len(in.CoreVersion) > 50 || !hex64.MatchString(in.Nonce) || !hex64.MatchString(in.CallbackSecret) {
		return "", invalid
	}
	for _, value := range []string{in.SiteName, in.DisplayName, in.CoreAPIKey, in.CoreAPISecret, in.CoreVersion} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", invalid
		}
	}
	if in.CoreAPIKey == "" || in.CoreAPISecret == "" || len(in.CoreAPIKey) > 200 || len(in.CoreAPISecret) > 200 {
		return "", invalid
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func (s *Service) checkToken(ctx context.Context, tx pgx.Tx, token, origin string) (enrolmentToken, error) {
	var t enrolmentToken
	var revokedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT id,kind,origin_pattern,max_sites,used_sites,expires_at<=$3,revoked_at,created_by,
 EXISTS(SELECT 1 FROM relay_sites WHERE rtrim(lower(origin),'/')=$2)
 FROM relay_enrolment_tokens WHERE token_hash=$1 FOR UPDATE`, auth.Hash(token), origin, s.now()).Scan(&t.id, &t.kind, &t.pattern, &t.maxSites, &t.usedSites, &t.expired, &revokedAt, &t.createdBy, &t.originAlreadyKnown)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fail(401, "token_invalid", "The enrolment token or setup code is not valid.")
	}
	if err != nil {
		return t, unavailable()
	}
	switch {
	case revokedAt != nil:
		return t, fail(401, "token_revoked", "This setup code was revoked. Ask your Relay provider for a new setup code.")
	case t.expired:
		return t, fail(401, "token_expired", "This setup code has expired. Ask your Relay provider for a new setup code.")
	case !originAllowed(t.pattern, origin):
		return t, fail(403, "origin_not_allowed", "This site's address is not allowed by the setup code.")
	case t.maxSites != nil && t.usedSites >= *t.maxSites && !t.originAlreadyKnown:
		return t, fail(409, "token_used_up", "This setup code was already used.")
	}
	return t, nil
}

// originAllowed matches scheme and port exactly. "*." matches one or more
// host labels; an empty pattern allows any origin.
func originAllowed(pattern, origin string) bool {
	if pattern == "" {
		return true
	}
	p, err := url.Parse(strings.ToLower(pattern))
	o, err2 := url.Parse(origin)
	if err != nil || err2 != nil || p.Scheme != o.Scheme || p.Port() != o.Port() {
		return false
	}
	host, want := o.Hostname(), p.Hostname()
	if suffix, wildcard := strings.CutPrefix(want, "*."); wildcard {
		return strings.HasSuffix(host, "."+suffix) && len(host) > len(suffix)+1
	}
	return host == want
}

// proveOrigin asks the Core site at origin to sign a challenge with the
// callback secret of this request, so a leaked token cannot claim another site.
func (s *Service) proveOrigin(ctx context.Context, origin string, in EnrolRequest) error {
	challenge := auth.Secret()
	body, _ := json.Marshal(map[string]string{"nonce": in.Nonce, "challenge": challenge})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+proofPath, bytes.NewReader(body))
	if err != nil {
		return fail(502, "site_unreachable", "Relay Hub could not reach this site.")
	}
	request.Header.Set("Authorization", "token "+in.CoreAPIKey+":"+in.CoreAPISecret)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.cfg.Client.Do(request)
	if err != nil {
		return fail(502, "site_unreachable", "Relay Hub could not reach this site.")
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 65536))
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return fail(403, "proof_failed", "This site could not prove its address to Relay Hub.")
	}
	if response.StatusCode != 200 {
		return fail(502, "site_unreachable", "Relay Hub could not reach this site.")
	}
	var answer struct {
		Message struct {
			Proof string `json:"proof"`
		} `json:"message"`
	}
	mac := hmac.New(sha256.New, []byte(in.CallbackSecret))
	mac.Write([]byte(in.Nonce + ":" + challenge + ":" + origin))
	if json.Unmarshal(raw, &answer) != nil || !hmac.Equal([]byte(answer.Message.Proof), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return fail(403, "proof_failed", "This site could not prove its address to Relay Hub.")
	}
	return nil
}

func (s *Service) saveEnrolment(ctx context.Context, tx pgx.Tx, token enrolmentToken, origin string, in EnrolRequest, ip string) (EnrolResult, error) {
	var siteID string
	var enabled bool
	var previous *int64
	err := tx.QueryRow(ctx, `SELECT site_id,enabled,enrolled_by FROM relay_sites WHERE rtrim(lower(origin),'/')=$1 ORDER BY id LIMIT 1 FOR UPDATE`, origin).Scan(&siteID, &enabled, &previous)
	existing := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return EnrolResult{}, unavailable()
	}
	if !existing {
		if siteID, err = freeSiteID(ctx, tx, origin); err != nil {
			return EnrolResult{}, unavailable()
		}
		enabled = true
		if _, err = tx.Exec(ctx, `UPDATE relay_enrolment_tokens SET used_sites=used_sites+1 WHERE id=$1`, token.id); err != nil {
			return EnrolResult{}, unavailable()
		}
	}
	site := containers.Site{ID: siteID, Name: in.DisplayName, Origin: origin, Enabled: enabled}
	if site.Name == "" {
		site.Name = in.SiteName
	}
	secrets := map[string]any{"core_api_key": in.CoreAPIKey, "core_api_secret": in.CoreAPISecret, "callback_secret": in.CallbackSecret}
	if err = s.cfg.Store.SaveSiteTx(ctx, tx, site, secrets); err != nil {
		return EnrolResult{}, fail(422, "invalid_request", "The enrolment request is not valid.")
	}
	user, err := auth.ServiceUser(ctx, tx, siteID)
	if err != nil {
		return EnrolResult{}, unavailable()
	}
	hubToken, err := auth.RotateServiceToken(ctx, tx, user, siteID)
	if err != nil {
		return EnrolResult{}, unavailable()
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_sites SET enrolled_by=$2,enrolled_at=$3,service_user_id=$4 WHERE site_id=$1`, siteID, token.id, s.now(), user); err != nil {
		return EnrolResult{}, unavailable()
	}
	action := "site.enrol"
	if existing {
		action = "site.re_enrol"
	}
	metadata, _ := json.Marshal(map[string]any{"token_id": token.id, "token_kind": token.kind, "origin": origin, "was_manual": existing && previous == nil, "relay_core_version": in.CoreVersion})
	if _, err = tx.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,actor_id,account_id,action,outcome,metadata,source_ip)
 VALUES('system',$1,$2,$3,'completed',$4,nullif($5,'')::inet)`, token.createdBy, siteID, action, metadata, validIP(ip)); err != nil {
		return EnrolResult{}, unavailable()
	}
	return EnrolResult{SiteID: siteID, MCPURL: s.cfg.PublicOrigin + "/mcp", HubToken: hubToken, HubOrigin: s.cfg.PublicOrigin, ReEnrolled: existing}, nil
}

// freeSiteID derives the site id from the host and adds -2, -3 when taken.
func freeSiteID(ctx context.Context, tx pgx.Tx, origin string) (string, error) {
	u, _ := url.Parse(origin)
	base := strings.Trim(siteIDRun.ReplaceAllString(strings.ToLower(u.Host), "-"), "-")
	if len(base) > 60 {
		base = strings.Trim(base[:60], "-")
	}
	if base == "" {
		base = "site"
	}
	for n := 1; n < 1000; n++ {
		candidate := base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			candidate = strings.Trim(base[:min(len(base), 60-len(suffix))], "-") + suffix
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_sites WHERE site_id=$1) OR EXISTS(SELECT 1 FROM relay_users WHERE username='site:'||$1)`, candidate).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", errors.New("no free site id")
}

func validIP(ip string) string {
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

func asFailure(err error) *Failure {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure
	}
	return unavailable()
}

func writeFailure(w http.ResponseWriter, failure *Failure) {
	if failure.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(failure.RetryAfter))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(failure.Status)
	json.NewEncoder(w).Encode(map[string]any{"error": failure})
}
