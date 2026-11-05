package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The fake Meta app, Embedded Signup configuration and verification code that
// the signup container uses.
const (
	esAppID          = "100000000000001"
	esAppSecret      = "sim-app-secret"
	esConfigID       = "sim-es-config"
	esOTP            = "123456"
	esCodeLifetime   = 30 * time.Second
	esRegisterLimit  = 10
	esRegisterWindow = 72 * time.Hour
)

// signupFaults are the one-shot Embedded Signup faults; each one is used at
// the first point where it applies.
var signupFaults = map[string]bool{"exchange_slow": true, "subscribe_503": true, "register_133016": true, "history_declined": true,
	"smb_already_requested": true, "debug_token_no_scope": true, "error_event": true, "onboarding_cap": true}

var pinPattern = regexp.MustCompile(`^[0-9]{6}$`)

type esWABA struct {
	ID           string           `json:"id"`
	Business     string           `json:"business"`
	Name         string           `json:"name"`
	Funding      bool             `json:"funding"`
	Subscribed   bool             `json:"subscribed"`
	PartnerAdded bool             `json:"partner_added"`
	Templates    []map[string]any `json:"templates"`
}

type esPhone struct {
	ID, WABA, Display, VerifiedName string
	NameStatus, Platform, Status    string
	Quality, Limit                  string
	Coexistence, ShareHistory       bool
	PIN                             string
	Registers                       []time.Time
	Synced                          map[string]bool
	Profile                         map[string]any
}

type esCode struct {
	WABA, Phone string
	Issued      time.Time
	Used        bool
	Exchanges   int
}

type esToken struct{ WABA, Phone string }

// esData is the stored fake Meta signup state: the assets that each finished
// dialog creates, the codes and business tokens, and the Hub calls.
type esData struct {
	Businesses map[string]string   `json:"businesses"`
	WABAs      map[string]*esWABA  `json:"wabas"`
	Phones     map[string]*esPhone `json:"phones"`
	Codes      map[string]*esCode  `json:"codes"`
	Tokens     map[string]*esToken `json:"tokens"`
	Calls      []map[string]any    `json:"calls"`
	Next       map[string]int      `json:"next"`
}

// signupState is the fake Meta side of Embedded Signup. It survives a
// simulation restart through its state file. Seeded simulation numbers stay
// in simulationNumbers.
type signupState struct {
	mu       sync.Mutex
	records  *recorder
	now      func() time.Time
	path     string
	webhooks *signupWebhooks
	media    map[string]*esMedia
	esData
}

func newSignupState(records *recorder) *signupState {
	return &signupState{records: records, now: time.Now, webhooks: newSignupWebhooks(), media: map[string]*esMedia{}, esData: esData{Businesses: map[string]string{}, WABAs: map[string]*esWABA{},
		Phones: map[string]*esPhone{}, Codes: map[string]*esCode{}, Tokens: map[string]*esToken{}, Calls: []map[string]any{},
		Next: map[string]int{"phone": 120, "waba": 230, "business": 330, "code": 1, "token": 1, "session": 1}}}
}

// openSignupState loads the stored signup state, or starts empty.
func openSignupState(path string, records *recorder) (*signupState, error) {
	s := newSignupState(records)
	s.path = path
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil || json.Unmarshal(raw, &s.esData) != nil || s.Next["phone"] < 120 || s.Phones == nil || s.WABAs == nil || s.Codes == nil || s.Tokens == nil {
		return nil, errors.New("invalid signup provider state")
	}
	return s, nil
}

// save writes the state atomically before a reply; callers hold s.mu.
func (s *signupState) save() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.Marshal(s.esData)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".signup-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

// saved replies 503 when the state did not reach disk; callers hold s.mu.
func (s *signupState) saved(status int, result any) (int, any) {
	if s.save() != nil {
		return 503, metaError(2, 0, "OAuthException", "Simulation state unavailable", true)
	}
	return status, result
}

// sequence returns the next identifier of one kind.
func (s *signupState) sequence(kind string) int {
	n := s.Next[kind]
	s.Next[kind] = n + 1
	return n
}

// bearer is the access token of a Graph call; the Resumable Upload API sends it as OAuth.
func bearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(value, "OAuth "); ok {
		return token
	}
	return strings.TrimPrefix(value, "Bearer ")
}

// owns reports whether a Graph request belongs to Embedded Signup.
func (s *signupState) owns(r *http.Request, node string) bool {
	if node == "oauth" || node == "debug_token" || strings.HasPrefix(bearer(r), "sim-biz-") {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Phones[node] != nil || s.WABAs[node] != nil
}

func metaError(code, subcode int, kind, message string, transient bool) map[string]any {
	e := map[string]any{"message": message, "type": kind, "code": code, "is_transient": transient, "fbtrace_id": "sim-trace"}
	if subcode != 0 {
		e["error_subcode"] = subcode
	}
	return map[string]any{"error": e}
}

func permissionError() map[string]any {
	return metaError(200, 0, "OAuthException", "(#200) Permissions error", false)
}

// graph serves G1 to G11 for signup assets and business tokens.
func (s *signupState) graph(r *http.Request, node, edge string, body []byte) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved(s.route(r, node, edge, body))
}

func (s *signupState) route(r *http.Request, node, edge string, body []byte) (int, any) {
	if node == "oauth" && edge == "access_token" && r.Method == "GET" {
		return s.exchange(r.URL.Query())
	}
	if node == "debug_token" && edge == "" && r.Method == "GET" {
		return s.debugToken(r)
	}
	if status, result, ok := s.mediaRoute(r, node, edge, body); ok {
		return status, result
	}
	token := s.Tokens[bearer(r)]
	if token == nil && bearer(r) != "sim-provider-token" {
		return 401, metaError(190, 0, "OAuthException", "Invalid OAuth access token - Cannot parse access token", false)
	}
	if token == nil || (node != token.WABA && node != token.Phone) {
		return 400, permissionError()
	}
	if waba := s.WABAs[node]; waba != nil {
		return s.wabaRoute(r, waba, edge, body)
	}
	return s.phoneRoute(r, s.Phones[node], edge, body)
}

// exchange is G1: one code gives one business token, once, within 30 seconds.
func (s *signupState) exchange(q url.Values) (int, any) {
	if q.Get("client_id") != esAppID || q.Get("client_secret") != esAppSecret {
		return 400, metaError(101, 0, "OAuthException", "Error validating application. Invalid application ID.", false)
	}
	code := s.Codes[q.Get("code")]
	if code == nil {
		return 400, metaError(100, 36008, "OAuthException", "Invalid verification code format.", false)
	}
	code.Exchanges++
	if code.Used {
		return 400, metaError(100, 36009, "OAuthException", "This authorization code has been used.", false)
	}
	if s.now().Sub(code.Issued) > esCodeLifetime {
		return 400, metaError(100, 36007, "OAuthException", "This authorization code has expired.", false)
	}
	code.Used = true
	token := fmt.Sprintf("sim-biz-%d", s.sequence("token"))
	s.Tokens[token] = &esToken{WABA: code.WABA, Phone: code.Phone}
	return 200, map[string]any{"access_token": token, "token_type": "bearer"}
}

// debugToken is G2, called with the app token.
func (s *signupState) debugToken(r *http.Request) (int, any) {
	if bearer(r) != esAppID+"|"+esAppSecret {
		return 400, metaError(190, 0, "OAuthException", "Invalid OAuth access token.", false)
	}
	token := s.Tokens[r.URL.Query().Get("input_token")]
	if token == nil {
		return 200, map[string]any{"data": map[string]any{"app_id": esAppID, "is_valid": false, "scopes": []string{},
			"error": map[string]any{"code": 190, "message": "Invalid OAuth access token."}}}
	}
	scopes := []string{"whatsapp_business_management", "whatsapp_business_messaging"}
	if s.records.take("signup", "debug_token_no_scope") {
		scopes = scopes[:1]
	}
	granular := []map[string]any{}
	for _, scope := range scopes {
		granular = append(granular, map[string]any{"scope": scope, "target_ids": []string{token.WABA}})
	}
	return 200, map[string]any{"data": map[string]any{"app_id": esAppID, "type": "SYSTEM_USER", "application": "Relay simulation", "is_valid": true,
		"scopes": scopes, "granular_scopes": granular, "expires_at": 0, "data_access_expires_at": 0, "user_id": "sim-system-user"}}
}

func (s *signupState) wabaRoute(r *http.Request, waba *esWABA, edge string, body []byte) (int, any) {
	switch {
	case r.Method == "GET" && edge == "":
		view := map[string]any{"id": waba.ID, "name": waba.Name, "currency": "INR", "timezone_id": "71", "account_review_status": "APPROVED",
			"business_verification_status": "not_verified", "owner_business_info": map[string]any{"id": waba.Business, "name": s.Businesses[waba.Business]}}
		if waba.Funding {
			view["primary_funding_id"] = "sim-funding-" + waba.ID
		}
		return 200, view
	case r.Method == "GET" && edge == "phone_numbers":
		rows := []map[string]any{}
		for _, phone := range s.Phones {
			if phone.WABA == waba.ID {
				rows = append(rows, phone.view())
			}
		}
		sortRows(rows)
		return 200, map[string]any{"data": rows, "paging": map[string]any{"cursors": map[string]any{"before": "start", "after": "end"}}}
	case edge == "subscribed_apps":
		return s.subscription(r.Method, waba)
	case r.Method == "GET" && edge == "message_templates":
		rows := []map[string]any{}
		for _, row := range waba.Templates {
			if name := r.URL.Query().Get("name"); name == "" || row["name"] == name {
				rows = append(rows, row)
			}
		}
		return 200, map[string]any{"data": rows}
	case r.Method == "POST" && edge == "message_templates":
		var row map[string]any
		if json.Unmarshal(body, &row) != nil || row["name"] == nil {
			return 400, metaError(100, 0, "OAuthException", "Invalid parameter", false)
		}
		if problem := templateButtonProblem(row["components"]); problem != "" {
			return 400, templateRejection(problem)
		}
		row["id"], row["status"] = fmt.Sprintf("%s%04d", waba.ID, len(waba.Templates)+1), "PENDING"
		waba.Templates = append(waba.Templates, row)
		return 200, map[string]any{"id": row["id"], "status": "PENDING", "category": row["category"]}
	}
	return 404, metaError(100, 0, "GraphMethodException", "Unsupported request - fake signup route", false)
}

// subscription is G5 and G6. The first subscription of the app also sends
// the account_update PARTNER_ADDED webhook.
func (s *signupState) subscription(method string, waba *esWABA) (int, any) {
	switch method {
	case "GET":
		data := []any{}
		if waba.Subscribed {
			data = append(data, map[string]any{"whatsapp_business_api_data": map[string]any{"id": esAppID, "name": "Relay simulation", "link": "https://www.facebook.com/games/?app_id=" + esAppID}})
		}
		return 200, map[string]any{"data": data}
	case "POST":
		s.call("subscribe", waba.ID, "")
		if s.records.take("signup", "subscribe_503") {
			return 503, metaError(2, 0, "OAuthException", "Service temporarily unavailable", true)
		}
		waba.Subscribed = true
		if !waba.PartnerAdded {
			waba.PartnerAdded = true
			s.webhooks.queue(accountUpdate(waba, "PARTNER_ADDED"))
		}
		return 200, map[string]any{"success": true}
	case "DELETE":
		waba.Subscribed = false
		return 200, map[string]any{"success": true}
	}
	return 405, metaError(100, 0, "GraphMethodException", "Unsupported method", false)
}

func (s *signupState) phoneRoute(r *http.Request, phone *esPhone, edge string, body []byte) (int, any) {
	switch {
	case r.Method == "GET" && edge == "":
		return 200, phone.view()
	case r.Method == "POST" && edge == "":
		return phone.setPIN(body)
	case r.Method == "POST" && edge == "deregister":
		s.call("deregister", phone.ID, "")
		phone.Platform, phone.Status = "NOT_APPLICABLE", "DISCONNECTED"
		return 200, map[string]any{"success": true}
	case r.Method == "POST" && edge == "register":
		return s.register(phone, body)
	case r.Method == "POST" && edge == "smb_app_data":
		return s.smbSync(phone, body)
	case r.Method == "POST" && edge == "messages":
		return s.send(phone, body)
	case r.Method == "POST" && edge == "media":
		return s.upload(r, phone, body)
	case edge == "whatsapp_business_profile":
		return phone.businessProfile(r.Method, body)
	}
	return 404, metaError(100, 0, "GraphMethodException", "Unsupported request - fake signup route", false)
}

// register is G7: at most 10 calls per number in 72 hours, one PIN.
func (s *signupState) register(phone *esPhone, body []byte) (int, any) {
	var in struct {
		Product string `json:"messaging_product"`
		PIN     string `json:"pin"`
	}
	s.call("register", phone.ID, "")
	if json.Unmarshal(body, &in) != nil || in.Product != "whatsapp" || !pinPattern.MatchString(in.PIN) {
		return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
	}
	if phone.Coexistence {
		return 400, metaError(100, 0, "OAuthException", "(#100) A WhatsApp Business app number is already connected; do not register it", false)
	}
	recent := []time.Time{}
	for _, at := range phone.Registers {
		if s.now().Sub(at) < esRegisterWindow {
			recent = append(recent, at)
		}
	}
	phone.Registers = append(recent, s.now())
	if len(recent) >= esRegisterLimit || s.records.take("signup", "register_133016") {
		return 400, metaError(133016, 0, "OAuthException", "Account register deregister rate limit exceeded", false)
	}
	if phone.PIN != "" && phone.PIN != in.PIN {
		return 400, metaError(133005, 0, "OAuthException", "Two step verification PIN mismatch", false)
	}
	phone.PIN, phone.Platform, phone.Status = in.PIN, "CLOUD_API", "CONNECTED"
	return 200, map[string]any{"success": true}
}

// smbSync is G9 and G10: each sync type of a Business app number starts once.
func (s *signupState) smbSync(phone *esPhone, body []byte) (int, any) {
	var in struct {
		Product  string `json:"messaging_product"`
		SyncType string `json:"sync_type"`
	}
	if json.Unmarshal(body, &in) != nil || in.Product != "whatsapp" || (in.SyncType != "smb_app_state_sync" && in.SyncType != "history") {
		return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
	}
	s.call("smb_app_data", phone.ID, in.SyncType)
	if !phone.Coexistence {
		return 400, metaError(100, 0, "OAuthException", "(#100) The number is not a WhatsApp Business app number", false)
	}
	if phone.Synced[in.SyncType] || s.records.take("signup", "smb_already_requested") {
		phone.Synced[in.SyncType] = true
		return 400, metaError(4, 2593111, "OAuthException", "Synchronization was already requested for this number", false)
	}
	phone.Synced[in.SyncType] = true
	if in.SyncType == "smb_app_state_sync" {
		s.webhooks.queue(stateSync(phone))
	} else if !phone.ShareHistory || s.records.take("signup", "history_declined") {
		s.webhooks.queue(historyDeclined(phone))
	} else {
		for phase := range 3 {
			s.webhooks.queue(historyPhase(phone, phase, s.now()))
		}
	}
	return 200, map[string]any{"success": true, "request_id": fmt.Sprintf("sim-sync-%s-%s", phone.ID, in.SyncType)}
}

// send is the G11 message send on a signup number; it reports sent and
// delivered through the signed webhook.
func (s *signupState) send(phone *esPhone, body []byte) (int, any) {
	var in struct {
		To        string `json:"to"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		MessageID string `json:"message_id"`
		Template  struct {
			Name     string `json:"name"`
			Language struct {
				Code string `json:"code"`
			} `json:"language"`
		} `json:"template"`
	}
	if json.Unmarshal(body, &in) != nil {
		return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
	}
	// A read receipt (with or without the typing indicator) marks an incoming message read.
	if in.Status == "read" && in.MessageID != "" {
		s.call("read", phone.ID, in.MessageID)
		return 200, map[string]any{"success": true}
	}
	if in.To == "" {
		return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
	}
	if phone.Status != "CONNECTED" {
		return 400, metaError(133010, 0, "OAuthException", "Account not registered", false)
	}
	if in.Type == "template" && !s.WABAs[phone.WABA].hasApproved(in.Template.Name, in.Template.Language.Code) {
		return 404, metaError(132001, 0, "OAuthException", "(#132001) Template name does not exist in the translation", false)
	}
	id := providerID("wamid.sim")
	for _, status := range []string{"sent", "delivered"} {
		s.webhooks.queue(messageStatus(phone, id, in.To, status, s.now()))
	}
	return 200, map[string]any{"messaging_product": "whatsapp", "contacts": []any{map[string]any{"input": in.To, "wa_id": in.To}}, "messages": []any{map[string]any{"id": id}}}
}

func (w *esWABA) hasApproved(name, language string) bool {
	for _, row := range w.Templates {
		if row["name"] == name && row["language"] == language && row["status"] == "APPROVED" {
			return true
		}
	}
	return false
}

func (p *esPhone) view() map[string]any {
	return map[string]any{"id": p.ID, "display_phone_number": p.Display, "verified_name": p.VerifiedName, "code_verification_status": "VERIFIED",
		"name_status": p.NameStatus, "platform_type": p.Platform, "status": p.Status, "quality_rating": p.Quality, "is_on_biz_app": p.Coexistence,
		"whatsapp_business_manager_messaging_limit": p.Limit, "throughput": map[string]any{"level": "STANDARD"}}
}

// call keeps one Hub call for the signup state dump; callers hold s.mu.
func (s *signupState) call(kind, node, detail string) {
	row := map[string]any{"kind": kind, "node": node, "at": s.now().UTC().Format(time.RFC3339Nano)}
	if detail != "" {
		row["detail"] = detail
	}
	s.Calls = append(s.Calls, row)
}

func secretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

// snapshot is the signup state without codes or tokens in clear text.
func (s *signupState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	phones, wabas, codes, tokens := []map[string]any{}, []map[string]any{}, []map[string]any{}, []map[string]any{}
	for _, p := range s.Phones {
		row := p.view()
		row["waba_id"], row["registers"], row["pin_set"], row["share_history"], row["synced"] = p.WABA, len(p.Registers), p.PIN != "", p.ShareHistory, p.Synced
		phones = append(phones, row)
	}
	for _, w := range s.WABAs {
		wabas = append(wabas, map[string]any{"id": w.ID, "business_id": w.Business, "name": w.Name, "payment_method_ready": w.Funding,
			"subscribed": w.Subscribed, "partner_added": w.PartnerAdded, "templates": len(w.Templates)})
	}
	for value, c := range s.Codes {
		codes = append(codes, map[string]any{"hash": secretHash(value), "waba_id": c.WABA, "phone_id": c.Phone, "used": c.Used, "exchange_calls": c.Exchanges,
			"expired": s.now().Sub(c.Issued) > esCodeLifetime})
	}
	for value, t := range s.Tokens {
		tokens = append(tokens, map[string]any{"hash": secretHash(value), "waba_id": t.WABA, "phone_id": t.Phone})
	}
	for _, rows := range [][]map[string]any{phones, wabas, codes, tokens} {
		sortRows(rows)
	}
	return map[string]any{"phones": phones, "wabas": wabas, "businesses": s.Businesses, "codes": codes, "tokens": tokens,
		"calls": append([]map[string]any{}, s.Calls...), "webhooks": s.webhooks.snapshot()}
}

// businessProfile reads or changes the number's public WhatsApp profile.
func (p *esPhone) businessProfile(method string, body []byte) (int, any) {
	if p.Profile == nil {
		p.Profile = map[string]any{"about": "Hey there! I am using WhatsApp.", "vertical": "UNDEFINED"}
	}
	if method == "GET" {
		return 200, map[string]any{"data": []any{p.Profile}}
	}
	var in map[string]any
	if json.Unmarshal(body, &in) != nil || in["messaging_product"] != "whatsapp" {
		return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
	}
	for _, name := range []string{"about", "address", "description", "email", "websites", "vertical"} {
		if value, ok := in[name]; ok {
			p.Profile[name] = value
		}
	}
	if handle, ok := in["profile_picture_handle"].(string); ok && handle != "" {
		p.Profile["profile_picture_url"] = "https://example.com/profile/" + p.ID + ".jpg"
	}
	return 200, map[string]any{"success": true}
}

// setPIN changes the number's two-step verification PIN.
func (p *esPhone) setPIN(body []byte) (int, any) {
	var in struct {
		PIN string `json:"pin"`
	}
	if json.Unmarshal(body, &in) != nil || !pinPattern.MatchString(in.PIN) {
		return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
	}
	p.PIN = in.PIN
	return 200, map[string]any{"success": true}
}
