package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed es
var esFiles embed.FS

var numberPattern = regexp.MustCompile(`^[1-9][0-9]{7,14}$`)

// esOtherProviderNumbers are the numbers the dialog offers as "on another provider".
var esOtherProviderNumbers = []string{"919888800001", "919888800002", "919888800003"}

// esCancelSteps are the dialog steps a cancel can report as current_step.
var esCancelSteps = map[string]bool{"BUSINESS_PORTFOLIO_SELECTION": true, "WABA_SELECTION": true, "PHONE_NUMBER_SETUP": true,
	"PHONE_NUMBER_VERIFICATION": true, "WHATSAPP_BUSINESS_APP_QR": true}

// esDialogInput is one request of the fake Embedded Signup dialog.
type esDialogInput struct {
	AppID        string `json:"app_id"`
	ConfigID     string `json:"config_id"`
	Scenario     string `json:"scenario"`
	Number       string `json:"number"`
	OTP          string `json:"otp"`
	DisplayName  string `json:"display_name"`
	ShareHistory bool   `json:"share_history"`
	CancelStep   string `json:"cancel_step"`
	ErrorMessage string `json:"error_message"`
}

// browser serves the fake Facebook SDK and Embedded Signup dialog.
func (s *signupState) browser() http.Handler {
	mux := http.NewServeMux()
	static := func(name, kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			raw, _ := esFiles.ReadFile("es/" + name)
			w.Header().Set("Content-Type", kind)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if strings.HasPrefix(kind, "text/html") {
				w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
			}
			w.Write(raw)
		}
	}
	mux.HandleFunc("GET /sdk.js", static("sdk.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /es/dialog", static("dialog.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /es/dialog.js", static("dialog.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /es/dialog.css", static("dialog.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /es/dialog/numbers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"numbers": s.otherProviderNumbers()})
	})
	mux.HandleFunc("POST /es/dialog/open", s.dialogHandler(s.open))
	mux.HandleFunc("POST /es/dialog/finish", s.dialogHandler(s.finish))
	return mux
}

func (s *signupState) dialogHandler(step func(esDialogInput) (int, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		var in esDialogInput
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		status, result := step(in)
		s.mu.Lock()
		if s.save() != nil {
			status, result = 503, map[string]any{"error": "Simulation state unavailable"}
		}
		s.mu.Unlock()
		reply, _ := json.Marshal(result)
		recorded, _ := json.Marshal(map[string]any{"scenario": in.Scenario, "number": in.Number})
		redacted := reply
		if code, ok := result["code"].(string); ok && code != "" {
			redacted = bytes.Replace(reply, []byte(code), []byte("[redacted]"), 1)
		}
		s.records.record(r, "es_dialog", recorded, status, redacted, started, "")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		w.Write(reply)
	}
}

// open checks the app and configuration the SDK passed. A wrong one ends the
// dialog with an ERROR event, as Meta does.
func (s *signupState) open(in esDialogInput) (int, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.AppID != esAppID || in.ConfigID != esConfigID {
		return 200, s.errorEvent("Invalid configuration. Check the app and configuration ID.", "SIM_INVALID_CONFIG")
	}
	return 200, map[string]any{"ready": true}
}

// finish ends the dialog: it creates the assets and returns the code and the
// WA_EMBEDDED_SIGNUP event for the chosen scenario.
func (s *signupState) finish(in esDialogInput) (int, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.AppID != esAppID || in.ConfigID != esConfigID {
		return 200, s.errorEvent("Invalid configuration. Check the app and configuration ID.", "SIM_INVALID_CONFIG")
	}
	switch in.Scenario {
	case "cancel":
		if !esCancelSteps[in.CancelStep] {
			return 400, map[string]any{"error": "Choose the step to cancel at."}
		}
		return 200, map[string]any{"event": "CANCEL", "code": nil, "data": map[string]any{"current_step": in.CancelStep, "session_id": s.esSession()}}
	case "error":
		message := strings.TrimSpace(in.ErrorMessage)
		if message == "" {
			message = "An unexpected error occurred. Please try again later."
		}
		return 200, s.errorEvent(message, "SIM_ERROR")
	case "new", "coexistence", "migrate":
	default:
		return 400, map[string]any{"error": "Choose how to connect the number."}
	}
	if s.records.take("signup", "error_event") {
		return 200, s.errorEvent("An unexpected error occurred. Please try again later.", "SIM_ERROR")
	}
	if s.records.take("signup", "onboarding_cap") {
		return 200, s.errorEvent("This business has reached its limit of WhatsApp Business accounts for this partner.", "SIM_ONBOARDING_CAP")
	}
	if problem := s.dialogProblem(in); problem != "" {
		return 400, map[string]any{"error": problem}
	}
	phone := s.asset(in)
	issued := s.now()
	if s.records.take("signup", "exchange_slow") {
		issued = issued.Add(-esCodeLifetime - time.Second)
	}
	code := fmt.Sprintf("sim-es-code-%d-%s", s.sequence("code"), strings.ToLower(rand.Text()[:12]))
	s.Codes[code] = &esCode{WABA: phone.WABA, Phone: phone.ID, Issued: issued}
	event := "FINISH"
	if in.Scenario == "coexistence" {
		event = "FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING"
	}
	return 200, map[string]any{"event": event, "code": code, "data": map[string]any{"phone_number_id": phone.ID, "waba_id": phone.WABA,
		"business_id": s.WABAs[phone.WABA].Business, "session_id": s.esSession()}}
}

func (s *signupState) dialogProblem(in esDialogInput) string {
	switch {
	case !numberPattern.MatchString(in.Number):
		return "Enter the number with country code, digits only."
	case seededID(in.Number) != "":
		return "This number is already registered on another WhatsApp Business account."
	case in.Scenario == "new" && in.OTP != esOTP:
		return "The code is not correct."
	case in.Scenario != "coexistence" && strings.TrimSpace(in.DisplayName) == "":
		return "Enter a display name."
	}
	return ""
}

// seededID finds the seeded phone ID of a display number.
func seededID(display string) string {
	for id, number := range simulationNumbers {
		if number.Display == display {
			return id
		}
	}
	return ""
}

func (s *signupState) errorEvent(message, code string) map[string]any {
	return map[string]any{"event": "ERROR", "code": nil, "data": map[string]any{"error_message": message, "error_code": code, "session_id": s.esSession()}}
}

func (s *signupState) esSession() string {
	return fmt.Sprintf("sim-es-session-%d", s.sequence("session"))
}

// asset returns the number for a finished dialog. A number signed up before
// keeps its ID and WABA, as Meta keeps one number in one WABA.
func (s *signupState) asset(in esDialogInput) *esPhone {
	for _, phone := range s.Phones {
		if phone.Display == in.Number {
			phone.Coexistence = phone.Coexistence || in.Scenario == "coexistence"
			phone.ShareHistory = in.ShareHistory
			return phone
		}
	}
	business := fmt.Sprint(s.sequence("business"))
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = "Business app " + in.Number[len(in.Number)-4:]
	}
	s.Businesses[business] = name
	waba := &esWABA{ID: fmt.Sprint(s.sequence("waba")), Business: business, Name: name}
	waba.Templates = []map[string]any{helloWorld(waba.ID)}
	s.WABAs[waba.ID] = waba
	phone := &esPhone{ID: fmt.Sprint(s.sequence("phone")), WABA: waba.ID, Display: in.Number, VerifiedName: name, Synced: map[string]bool{}}
	switch in.Scenario {
	case "new":
		phone.NameStatus, phone.Platform, phone.Status, phone.Quality, phone.Limit = "PENDING_REVIEW", "NOT_APPLICABLE", "PENDING", "UNKNOWN", "TIER_250"
	case "coexistence":
		phone.NameStatus, phone.Platform, phone.Status, phone.Quality, phone.Limit = "APPROVED", "CLOUD_API", "CONNECTED", "GREEN", "TIER_250"
		phone.Coexistence, phone.ShareHistory = true, in.ShareHistory
	case "migrate":
		phone.NameStatus, phone.Platform, phone.Status, phone.Quality, phone.Limit = "APPROVED", "NOT_APPLICABLE", "PENDING", "GREEN", "TIER_1K"
	}
	s.Phones[phone.ID] = phone
	return phone
}

// otherProviderNumbers are the preset numbers plus every signup number.
func (s *signupState) otherProviderNumbers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	numbers := append([]string{}, esOtherProviderNumbers...)
	signedUp := []string{}
	for _, phone := range s.Phones {
		if !slices.Contains(numbers, phone.Display) {
			signedUp = append(signedUp, phone.Display)
		}
	}
	slices.Sort(signedUp)
	return append(numbers, signedUp...)
}

func helloWorld(waba string) map[string]any {
	return map[string]any{"id": "sim-tpl-" + waba + "-1", "name": "hello_world", "language": "en_US", "category": "UTILITY", "status": "APPROVED",
		"parameter_format": "POSITIONAL", "quality_score": map[string]any{"score": "UNKNOWN"}, "components": []any{
			map[string]any{"type": "HEADER", "format": "TEXT", "text": "Hello World"},
			map[string]any{"type": "BODY", "text": "Welcome and congratulations!! This message demonstrates your ability to send a WhatsApp message notification from the Cloud API, hosted by Meta. Thank you for taking the time to test with us."},
			map[string]any{"type": "FOOTER", "text": "WhatsApp Business Platform sample message"},
		}}
}
