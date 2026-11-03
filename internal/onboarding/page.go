package onboarding

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

//go:embed page/*
var pageFiles embed.FS

var sessionPattern = regexp.MustCompile(`^sus_[a-f0-9]{32}$`)

var assetTypes = map[string]string{"page.js": "text/javascript; charset=utf-8", "page.css": "text/css; charset=utf-8"}

// page serves the static signup window. It holds no session data; the window
// reads its launch secret from the URL fragment and sends it on each API call.
func (s *Service) page(w http.ResponseWriter, r *http.Request) {
	if !sessionPattern.MatchString(r.PathValue("session")) {
		http.NotFound(w, r)
		return
	}
	body, _ := pageFiles.ReadFile("page/index.html")
	s.windowHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(body)
}

// setupLinkPage answers a setup link that was opened in a browser: the link is pasted in Relay, not opened.
func setupLinkPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Relay setup link</title></head>
<body style="font-family:system-ui,sans-serif;max-width:560px;margin:48px auto;padding:0 16px;line-height:1.5"><h1>Relay setup link</h1>
<p>Do not open this link. Copy it, open Relay on your site, choose <strong>Get started</strong> and paste the whole link in <strong>Setup code</strong>.</p></body></html>`)
}

func (s *Service) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	contentType, ok := assetTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, _ := pageFiles.ReadFile("page/" + name)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(body)
}

// windowHeaders keep the window out of caches, referrers and frames. Scripts
// come only from the Hub and the Facebook SDK origin.
func (s *Service) windowHeaders(w http.ResponseWriter) {
	sdk, _ := sourceOrigin(s.cfg.SDKURL)
	frames := sdk
	if s.cfg.SDKURL == defaultSDKURL {
		frames += " https://www.facebook.com https://web.facebook.com https://*.facebook.com"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' "+sdk+"; frame-src "+frames+
		"; connect-src 'self' "+sdk+"; img-src 'self' data: "+sdk+"; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
}

// windowRecord authenticates a window API call by its launch secret.
func (s *Service) windowRecord(w http.ResponseWriter, r *http.Request) (record, bool) {
	s.windowHeaders(w)
	id := r.PathValue("session")
	if !sessionPattern.MatchString(id) {
		http.NotFound(w, r)
		return record{}, false
	}
	found, err := s.launchRecord(r.Context(), id, r.Header.Get("X-Relay-Launch"))
	if err != nil {
		http.NotFound(w, r)
		return record{}, false
	}
	return found, true
}

func (s *Service) windowConfig(w http.ResponseWriter, r *http.Request) {
	found, ok := s.windowRecord(w, r)
	if !ok {
		return
	}
	opened, err := s.windowOpened(r.Context(), found.SessionID)
	if err != nil {
		writeFailure(w, unavailable())
		return
	}
	app, err := s.cfg.Store.SignupApp(r.Context())
	if err != nil {
		writeFailure(w, fail(503, "signup_unavailable", "Self-service WhatsApp signup is not available right now."))
		return
	}
	var siteName, siteOrigin string
	if err = s.cfg.DB.QueryRow(r.Context(), `SELECT name,origin FROM relay_sites WHERE site_id=$1`, opened.site).Scan(&siteName, &siteOrigin); err != nil {
		writeFailure(w, unavailable())
		return
	}
	writeJSON(w, map[string]any{"app_id": app.AppID, "config_id": app.ConfigID, "graph_version": app.GraphVersion, "sdk_url": s.cfg.SDKURL,
		"extras": extras(opened.Flow, opened.prefill), "site_name": siteName, "site_origin": strings.TrimSuffix(siteOrigin, "/"),
		"flow": opened.Flow, "state": opened.State, "session": opened.Session})
}

func (s *Service) windowState(w http.ResponseWriter, r *http.Request) {
	found, ok := s.windowRecord(w, r)
	if !ok {
		return
	}
	if s.expire(&found); found.State == "expired" {
		if updated, err := s.update(r.Context(), found.SessionID, func(*record) error { return nil }); err == nil {
			found = updated
		}
	}
	writeJSON(w, found.Session)
}

func (s *Service) windowEvent(w http.ResponseWriter, r *http.Request) {
	found, ok := s.windowRecord(w, r)
	if !ok {
		return
	}
	var in struct {
		Event  string         `json:"event"`
		Data   map[string]any `json:"data"`
		Origin string         `json:"origin"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains(metaEvents, in.Event) || !s.cfg.DialogOrigin(in.Origin) {
		writeFailure(w, fail(422, "invalid_request", "The Facebook message is not valid."))
		return
	}
	session, err := s.RecordEvent(r.Context(), found.SessionID, in.Event, in.Data)
	if err != nil {
		writeFailure(w, asFailure(err))
		return
	}
	writeJSON(w, session)
}

func (s *Service) windowCode(w http.ResponseWriter, r *http.Request) {
	found, ok := s.windowRecord(w, r)
	if !ok {
		return
	}
	var in struct {
		Code        string         `json:"code"`
		SessionInfo map[string]any `json:"session_info"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.SessionInfo != nil {
		if event, _ := in.SessionInfo["event"].(string); !slices.Contains(metaEvents, event) {
			in.SessionInfo = nil
		}
	}
	session, err := s.SubmitCode(r.Context(), found.SessionID, in.Code, in.SessionInfo)
	if err != nil {
		writeFailure(w, asFailure(err))
		return
	}
	writeJSON(w, session)
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(target) != nil {
		writeFailure(w, fail(422, "invalid_request", "The request is not valid."))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}

// extras is the Embedded Signup extras for a flow, built only on the Hub, in Meta's documented
// shape: setup, featureType ("" or coexistence) and sessionInfoVersion "3" for the FINISH message.
func extras(flow string, prefill map[string]any) map[string]any {
	featureType := ""
	if flow == "coexistence" {
		featureType = "whatsapp_business_app_onboarding"
	}
	return map[string]any{"setup": setup(prefill), "featureType": featureType, "sessionInfoVersion": "3"}
}

// setup maps Core's prefill to Meta's setup object. Only short strings pass.
func setup(prefill map[string]any) map[string]any {
	business, _ := prefill["business"].(map[string]any)
	phone, _ := prefill["phone"].(map[string]any)
	out := map[string]any{}
	metaBusiness := pick(business, map[string]string{"name": "name", "email": "email", "website": "website", "timezone": "timezone"})
	address := pick(business, map[string]string{"street_address1": "streetAddress1", "street_address2": "streetAddress2",
		"city": "city", "state": "state", "zip_postal": "zipPostal", "country": "country"})
	if len(address) > 0 {
		metaBusiness["address"] = address
	}
	number, _ := business["phone"].(map[string]any)
	if code, err := strconv.Atoi(strings.TrimPrefix(prefillText(number, "code"), "+")); err == nil && code > 0 && prefillText(number, "number") != "" {
		metaBusiness["phone"] = map[string]any{"code": code, "number": prefillText(number, "number")}
	}
	if len(metaBusiness) > 0 {
		out["business"] = metaBusiness
	}
	if metaPhone := pick(phone, map[string]string{"display_name": "displayName", "category": "category", "description": "description"}); len(metaPhone) > 0 {
		out["phone"] = metaPhone
	}
	return out
}

// pick copies the non-empty prefill strings under Meta's key names.
func pick(source map[string]any, keys map[string]string) map[string]any {
	out := map[string]any{}
	for key, target := range keys {
		if text := prefillText(source, key); text != "" {
			out[target] = text
		}
	}
	return out
}

// prefillText is a trimmed prefill string; Meta's longest field, the phone description, has 512 characters.
func prefillText(source map[string]any, key string) string {
	text, _ := source[key].(string)
	if len([]rune(text)) > 512 {
		return ""
	}
	return strings.TrimSpace(text)
}
