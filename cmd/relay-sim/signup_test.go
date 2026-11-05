package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// esHarness drives the fake Meta signup routes in process.
type esHarness struct {
	t      *testing.T
	meta   *fakeMeta
	signup *signupState
	clock  time.Time
}

func newESHarness(t *testing.T) *esHarness {
	records := newRecorder()
	h := &esHarness{t: t, signup: newSignupState(records), clock: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	h.signup.now = func() time.Time { return h.clock }
	h.meta = &fakeMeta{records: records, signup: h.signup, bulk: newBulkReceipts()}
	return h
}

func (h *esHarness) graph(method, path, token, body string, want int) map[string]any {
	h.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.meta.ServeHTTP(w, r)
	if w.Code != want {
		h.t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		h.t.Fatal(err)
	}
	return v
}

func (h *esHarness) dialog(path string, in esDialogInput, want int) map[string]any {
	h.t.Helper()
	if in.AppID == "" {
		in.AppID, in.ConfigID = esAppID, esConfigID
	}
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
	w := httptest.NewRecorder()
	h.signup.browser().ServeHTTP(w, r)
	if w.Code != want {
		h.t.Fatalf("%s: status %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	var v map[string]any
	json.Unmarshal(w.Body.Bytes(), &v)
	return v
}

// signup finishes one dialog and exchanges its code for a business token.
func (h *esHarness) signupNumber(in esDialogInput) (string, map[string]any) {
	h.t.Helper()
	finish := h.dialog("/es/dialog/finish", in, 200)
	code, _ := finish["code"].(string)
	if code == "" {
		h.t.Fatalf("no code: %v", finish)
	}
	token := h.graph("GET", "/v25.0/oauth/access_token?client_id="+esAppID+"&client_secret=sim-app-secret&code="+code, "", "", 200)["access_token"].(string)
	return token, finish["data"].(map[string]any)
}

// drain returns the queued webhook payloads.
func (h *esHarness) drain() [][]byte {
	out := [][]byte{}
	for {
		select {
		case payload := <-h.signup.webhooks.pending:
			out = append(out, payload)
		default:
			return out
		}
	}
}

func errorCode(v map[string]any) (float64, float64) {
	e, _ := v["error"].(map[string]any)
	code, _ := e["code"].(float64)
	sub, _ := e["error_subcode"].(float64)
	return code, sub
}

func TestSignupDialogScenarios(t *testing.T) {
	h := newESHarness(t)
	if v := h.dialog("/es/dialog/open", esDialogInput{AppID: esAppID, ConfigID: "other"}, 200); v["event"] != "ERROR" {
		t.Fatal("wrong configuration must end with ERROR", v)
	}
	if v := h.dialog("/es/dialog/open", esDialogInput{}, 200); v["ready"] != true {
		t.Fatal(v)
	}
	h.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "919876500001", OTP: "000000", DisplayName: "Acme"}, 400)
	h.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "12", OTP: esOTP, DisplayName: "Acme"}, 400)
	h.dialog("/es/dialog/finish", esDialogInput{Scenario: "migrate", Number: "919999999999", DisplayName: "Seeded"}, 400)
	newNumber := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "919876500001", OTP: esOTP, DisplayName: "Acme"}, 200)
	data := newNumber["data"].(map[string]any)
	if newNumber["event"] != "FINISH" || data["phone_number_id"] != "120" || data["waba_id"] != "230" || data["business_id"] != "330" {
		t.Fatal(newNumber)
	}
	coexistence := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "coexistence", Number: "919876500002", ShareHistory: true}, 200)
	if coexistence["event"] != "FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING" || coexistence["data"].(map[string]any)["phone_number_id"] != "121" {
		t.Fatal(coexistence)
	}
	again := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "migrate", Number: "919876500001", DisplayName: "Acme"}, 200)
	if again["data"].(map[string]any)["phone_number_id"] != "120" || again["code"] == newNumber["code"] {
		t.Fatal("a number signed up again keeps its ID and gets a new code", again)
	}
	cancel := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "cancel", CancelStep: "PHONE_NUMBER_VERIFICATION"}, 200)
	if cancel["event"] != "CANCEL" || cancel["code"] != nil || cancel["data"].(map[string]any)["current_step"] != "PHONE_NUMBER_VERIFICATION" {
		t.Fatal(cancel)
	}
	h.dialog("/es/dialog/finish", esDialogInput{Scenario: "cancel", CancelStep: "ANYWHERE"}, 400)
	failure := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "error", ErrorMessage: "Meta says no"}, 200)
	if failure["event"] != "ERROR" || failure["data"].(map[string]any)["error_message"] != "Meta says no" {
		t.Fatal(failure)
	}
	h.signup.records.faults["signup"] = []string{"onboarding_cap", "error_event"}
	for _, want := range []string{"SIM_ERROR", "SIM_ONBOARDING_CAP"} {
		v := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "919876500003", OTP: esOTP, DisplayName: "Acme"}, 200)
		if v["event"] != "ERROR" || v["data"].(map[string]any)["error_code"] != want {
			t.Fatal(want, v)
		}
	}
	numbers := strings.Join(h.signup.otherProviderNumbers(), ",")
	if !strings.HasPrefix(numbers, "919888800001,919888800002,919888800003,919876500001,919876500002") {
		t.Fatal(numbers)
	}
	for _, row := range h.signup.records.snapshot() {
		if row.Kind == "es_dialog" && strings.Contains(string(row.Response), "sim-es-code-") {
			t.Fatal("dialog capture leaked a code")
		}
	}
}

func TestSignupCodeExchangeAndTokenCheck(t *testing.T) {
	h := newESHarness(t)
	finish := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "919876500001", OTP: esOTP, DisplayName: "Acme"}, 200)
	code := finish["code"].(string)
	exchange := "/v25.0/oauth/access_token?client_id=" + esAppID + "&client_secret=sim-app-secret&code="
	if c, _ := errorCode(h.graph("GET", "/v25.0/oauth/access_token?client_id="+esAppID+"&client_secret=wrong&code="+code, "", "", 400)); c != 101 {
		t.Fatal("wrong app secret accepted")
	}
	token := h.graph("GET", exchange+code, "", "", 200)["access_token"].(string)
	if !strings.HasPrefix(token, "sim-biz-") {
		t.Fatal(token)
	}
	if c, sub := errorCode(h.graph("GET", exchange+code, "", "", 400)); c != 100 || sub != 36009 {
		t.Fatal("reused code accepted")
	}
	if c, _ := errorCode(h.graph("GET", exchange+"unknown", "", "", 400)); c != 100 {
		t.Fatal("unknown code accepted")
	}
	late := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "919876500009", OTP: esOTP, DisplayName: "Late"}, 200)["code"].(string)
	h.clock = h.clock.Add(31 * time.Second)
	if c, sub := errorCode(h.graph("GET", exchange+late, "", "", 400)); c != 100 || sub != 36007 {
		t.Fatal("expired code accepted")
	}
	h.signup.records.faults["signup"] = []string{"exchange_slow"}
	slow := h.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "919876500010", OTP: esOTP, DisplayName: "Slow"}, 200)["code"].(string)
	if _, sub := errorCode(h.graph("GET", exchange+slow, "", "", 400)); sub != 36007 {
		t.Fatal("exchange_slow must deliver an expired code")
	}

	h.graph("GET", "/v25.0/debug_token?input_token="+token, "sim-provider-token", "", 400)
	debug := h.graph("GET", "/v25.0/debug_token?input_token="+token, esAppID+"|"+esAppSecret, "", 200)["data"].(map[string]any)
	scopes := debug["granular_scopes"].([]any)
	if debug["is_valid"] != true || debug["app_id"] != esAppID || len(scopes) != 2 || scopes[1].(map[string]any)["target_ids"].([]any)[0] != "230" {
		t.Fatal(debug)
	}
	if v := h.graph("GET", "/v25.0/debug_token?input_token=sim-biz-404", esAppID+"|"+esAppSecret, "", 200)["data"].(map[string]any); v["is_valid"] != false {
		t.Fatal(v)
	}
	h.signup.records.faults["signup"] = []string{"debug_token_no_scope"}
	if v := h.graph("GET", "/v25.0/debug_token?input_token="+token, esAppID+"|"+esAppSecret, "", 200)["data"].(map[string]any); len(v["granular_scopes"].([]any)) != 1 {
		t.Fatal("debug_token_no_scope kept the messaging scope", v)
	}

	codes := h.signup.snapshot()["codes"].([]map[string]any)
	if len(codes) != 3 || codes[0]["hash"] == code || strings.Contains(mustJSON(t, h.signup.snapshot()), token) {
		t.Fatal("state dump must hash codes and tokens")
	}
	for _, row := range h.signup.records.snapshot() {
		if strings.Contains(string(row.Response), token) || strings.Contains(row.Query, code) || strings.Contains(row.Query, "sim-app-secret") {
			t.Fatal("capture leaked a secret", row.Query, string(row.Response))
		}
	}
}

func TestSignupAssetsAndPermissions(t *testing.T) {
	h := newESHarness(t)
	token, data := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500001", OTP: esOTP, DisplayName: "Acme"})
	other, _ := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500002", OTP: esOTP, DisplayName: "Other"})
	waba, phone := data["waba_id"].(string), data["phone_number_id"].(string)
	account := h.graph("GET", "/v25.0/"+waba+"?fields=id,name,owner_business_info,primary_funding_id,currency", token, "", 200)
	if account["owner_business_info"].(map[string]any)["id"] != "330" || account["primary_funding_id"] != nil {
		t.Fatal(account)
	}
	numbers := h.graph("GET", "/v25.0/"+waba+"/phone_numbers?limit=100", token, "", 200)["data"].([]any)
	if len(numbers) != 1 || numbers[0].(map[string]any)["platform_type"] != "NOT_APPLICABLE" || numbers[0].(map[string]any)["name_status"] != "PENDING_REVIEW" {
		t.Fatal(numbers)
	}
	for _, denied := range []struct{ path, token string }{{"/v25.0/231", token}, {"/v25.0/110", token}, {"/v25.0/" + waba, other}, {"/v25.0/" + phone, "sim-provider-token"}} {
		if c, _ := errorCode(h.graph("GET", denied.path, denied.token, "", 400)); c != 200 {
			t.Fatal("cross-WABA access allowed", denied)
		}
	}
	h.graph("GET", "/v25.0/"+phone, "sim-biz-unknown", "", 401)
	h.graph("GET", "/v25.0/110", "sim-provider-token", "", 200)

	h.signup.records.faults["signup"] = []string{"subscribe_503"}
	h.graph("POST", "/v25.0/"+waba+"/subscribed_apps", token, "", 503)
	if apps := h.graph("GET", "/v25.0/"+waba+"/subscribed_apps", token, "", 200)["data"].([]any); len(apps) != 0 {
		t.Fatal("failed subscribe must not subscribe")
	}
	h.graph("POST", "/v25.0/"+waba+"/subscribed_apps", token, "", 200)
	h.graph("POST", "/v25.0/"+waba+"/subscribed_apps", token, "", 200)
	apps := h.graph("GET", "/v25.0/"+waba+"/subscribed_apps", token, "", 200)["data"].([]any)
	if len(apps) != 1 || apps[0].(map[string]any)["whatsapp_business_api_data"].(map[string]any)["id"] != esAppID {
		t.Fatal(apps)
	}
	webhooks := h.drain()
	if len(webhooks) != 1 || !strings.Contains(string(webhooks[0]), `"PARTNER_ADDED"`) || webhookFields(webhooks[0])[0] != "account_update" {
		t.Fatal("PARTNER_ADDED must be sent once", len(webhooks))
	}

	templates := h.graph("GET", "/v25.0/"+waba+"/message_templates?name=hello_world", token, "", 200)["data"].([]any)
	// Meta template IDs are digits; Core refuses any other ID.
	if len(templates) != 1 || templates[0].(map[string]any)["status"] != "APPROVED" || !regexp.MustCompile(`^[0-9]+$`).MatchString(templates[0].(map[string]any)["id"].(string)) {
		t.Fatal(templates)
	}
	send := `{"messaging_product":"whatsapp","to":"919800000001","type":"template","template":{"name":"hello_world","language":{"code":"en_US"}}}`
	if c, _ := errorCode(h.graph("POST", "/v25.0/"+phone+"/messages", token, send, 400)); c != 133010 {
		t.Fatal("unregistered number sent")
	}
	h.graph("POST", "/v25.0/"+phone+"/register", token, `{"messaging_product":"whatsapp","pin":"12345"}`, 400)
	h.graph("POST", "/v25.0/"+phone+"/register", token, `{"messaging_product":"whatsapp","pin":"654321"}`, 200)
	h.graph("POST", "/v25.0/"+phone+"/register", token, `{"messaging_product":"whatsapp","pin":"654321"}`, 200)
	if c, _ := errorCode(h.graph("POST", "/v25.0/"+phone+"/register", token, `{"messaging_product":"whatsapp","pin":"111111"}`, 400)); c != 133005 {
		t.Fatal("PIN mismatch accepted")
	}
	if status := h.graph("GET", "/v25.0/"+phone+"?fields=platform_type,is_on_biz_app", token, "", 200); status["platform_type"] != "CLOUD_API" || status["status"] != "CONNECTED" || status["is_on_biz_app"] != false {
		t.Fatal(status)
	}
	sent := h.graph("POST", "/v25.0/"+phone+"/messages", token, send, 200)
	id := sent["messages"].([]any)[0].(map[string]any)["id"].(string)
	statuses := h.drain()
	if len(statuses) != 2 || !strings.Contains(string(statuses[0]), `"sent"`) || !strings.Contains(string(statuses[1]), `"delivered"`) || !strings.Contains(string(statuses[1]), id) {
		t.Fatal("sent and delivered statuses missing")
	}
	unknown := strings.Replace(send, "hello_world", "missing", 1)
	if c, _ := errorCode(h.graph("POST", "/v25.0/"+phone+"/messages", token, unknown, 404)); c != 132001 {
		t.Fatal("unknown template sent")
	}
	for _, row := range h.signup.records.snapshot() {
		if strings.Contains(string(row.Body), "654321") {
			t.Fatal("register PIN captured")
		}
	}
}

func TestSignupRegisterLimits(t *testing.T) {
	h := newESHarness(t)
	token, data := h.signupNumber(esDialogInput{Scenario: "migrate", Number: "919888800001", DisplayName: "Moved"})
	phone := data["phone_number_id"].(string)
	register := `{"messaging_product":"whatsapp","pin":"123123"}`
	h.signup.records.faults["signup"] = []string{"register_133016"}
	if c, _ := errorCode(h.graph("POST", "/v25.0/"+phone+"/register", token, register, 400)); c != 133016 {
		t.Fatal("register_133016 fault missing")
	}
	for range 9 {
		h.graph("POST", "/v25.0/"+phone+"/register", token, register, 200)
	}
	if c, _ := errorCode(h.graph("POST", "/v25.0/"+phone+"/register", token, register, 400)); c != 133016 {
		t.Fatal("eleventh register in 72 hours accepted")
	}
	h.clock = h.clock.Add(esRegisterWindow + time.Minute)
	h.graph("POST", "/v25.0/"+phone+"/register", token, register, 200)

	coexToken, coex := h.signupNumber(esDialogInput{Scenario: "coexistence", Number: "919876500002", ShareHistory: true})
	if c, _ := errorCode(h.graph("POST", "/v25.0/"+coex["phone_number_id"].(string)+"/register", coexToken, register, 400)); c != 100 {
		t.Fatal("Business app number registered")
	}
}

func TestSignupCoexistenceSyncs(t *testing.T) {
	h := newESHarness(t)
	token, data := h.signupNumber(esDialogInput{Scenario: "coexistence", Number: "919876500002", ShareHistory: true})
	phone := data["phone_number_id"].(string)
	if v := h.graph("GET", "/v25.0/"+phone, token, "", 200); v["is_on_biz_app"] != true || v["platform_type"] != "CLOUD_API" {
		t.Fatal(v)
	}
	contacts := `{"messaging_product":"whatsapp","sync_type":"smb_app_state_sync"}`
	history := `{"messaging_product":"whatsapp","sync_type":"history"}`
	h.graph("POST", "/v25.0/"+phone+"/smb_app_data", token, `{"messaging_product":"whatsapp","sync_type":"all"}`, 400)
	if v := h.graph("POST", "/v25.0/"+phone+"/smb_app_data", token, contacts, 200); v["request_id"] == nil {
		t.Fatal(v)
	}
	if c, sub := errorCode(h.graph("POST", "/v25.0/"+phone+"/smb_app_data", token, contacts, 400)); c != 4 || sub != 2593111 {
		t.Fatal("second contacts sync accepted")
	}
	h.graph("POST", "/v25.0/"+phone+"/smb_app_data", token, history, 200)
	webhooks := h.drain()
	if len(webhooks) != 4 {
		t.Fatalf("want state sync and 3 history phases, got %d", len(webhooks))
	}
	var sync struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Metadata  map[string]string `json:"metadata"`
					StateSync []struct {
						Type    string            `json:"type"`
						Action  string            `json:"action"`
						Contact map[string]string `json:"contact"`
					} `json:"state_sync"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(webhooks[0], &sync) != nil || len(sync.Entry[0].Changes[0].Value.StateSync) != 3 || sync.Entry[0].Changes[0].Value.Metadata["phone_number_id"] != phone {
		t.Fatal("state sync shape", string(webhooks[0]))
	}
	for phase, want := range []float64{30, 70, 100} {
		var body struct {
			Entry []struct {
				Changes []struct {
					Field string `json:"field"`
					Value struct {
						History []struct {
							Metadata struct {
								Phase    float64 `json:"phase"`
								Progress float64 `json:"progress"`
							} `json:"metadata"`
							Threads []struct {
								ID       string           `json:"id"`
								Messages []map[string]any `json:"messages"`
							} `json:"threads"`
						} `json:"history"`
					} `json:"value"`
				} `json:"changes"`
			} `json:"entry"`
		}
		raw := webhooks[phase+1]
		if json.Unmarshal(raw, &body) != nil {
			t.Fatal("history payload invalid")
		}
		chunk := body.Entry[0].Changes[0].Value.History[0]
		message := chunk.Threads[0].Messages[0]
		if body.Entry[0].Changes[0].Field != "history" || chunk.Metadata.Progress != want || int(chunk.Metadata.Phase) != phase || message["history_context"] == nil || message["id"] == "" {
			t.Fatal("history shape", phase)
		}
		if phase == 1 && len(raw) <= 2<<20 {
			t.Fatalf("phase 1 chunk is %d bytes, want over 2 MiB", len(raw))
		}
		if phase != 1 && len(raw) > 64<<10 {
			t.Fatal("small phases must stay small")
		}
	}
	if summary := string(webhookCapture(webhooks[2])); !strings.Contains(summary, `"bytes"`) || len(summary) > 200 {
		t.Fatal("large webhook capture must keep only its size")
	}

	declinedToken, declined := h.signupNumber(esDialogInput{Scenario: "coexistence", Number: "919876500003", ShareHistory: false})
	h.graph("POST", "/v25.0/"+declined["phone_number_id"].(string)+"/smb_app_data", declinedToken, history, 200)
	faultToken, faulted := h.signupNumber(esDialogInput{Scenario: "coexistence", Number: "919876500004", ShareHistory: true})
	h.signup.records.faults["signup"] = []string{"history_declined"}
	h.graph("POST", "/v25.0/"+faulted["phone_number_id"].(string)+"/smb_app_data", faultToken, history, 200)
	h.signup.records.faults["signup"] = []string{"smb_already_requested"}
	if c, _ := errorCode(h.graph("POST", "/v25.0/"+faulted["phone_number_id"].(string)+"/smb_app_data", faultToken, contacts, 400)); c != 4 {
		t.Fatal("smb_already_requested fault missing")
	}
	for _, payload := range h.drain() {
		if !strings.Contains(string(payload), `"code":2593109`) {
			t.Fatal("history decline must send error 2593109", string(payload))
		}
	}
	newToken, newNumber := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500005", OTP: esOTP, DisplayName: "New"})
	h.graph("POST", "/v25.0/"+newNumber["phone_number_id"].(string)+"/smb_app_data", newToken, history, 400)
}

func TestSignupStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signup.json")
	records := newRecorder()
	first, err := openSignupState(path, records)
	if err != nil {
		t.Fatal(err)
	}
	meta := &fakeMeta{records: records, signup: first}
	h := &esHarness{t: t, meta: meta, signup: first}
	token, data := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500001", OTP: esOTP, DisplayName: "Acme"})
	second, err := openSignupState(path, newRecorder())
	if err != nil {
		t.Fatal(err)
	}
	h2 := &esHarness{t: t, meta: &fakeMeta{records: second.records, signup: second}, signup: second}
	h2.graph("GET", "/v25.0/"+data["phone_number_id"].(string), token, "", 200)
	if next := h2.dialog("/es/dialog/finish", esDialogInput{Scenario: "new", Number: "919876500002", OTP: esOTP, DisplayName: "Next"}, 200); next["data"].(map[string]any)["phone_number_id"] != "121" {
		t.Fatal("restart reused an ID", next)
	}
	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = openSignupState(path, newRecorder()); err == nil {
		t.Fatal("corrupt signup state accepted")
	}
}

func TestSignupBrowserFiles(t *testing.T) {
	h := newESHarness(t)
	for path, want := range map[string]string{"/sdk.js": "window.FB", "/es/dialog": "Simulate scan", "/es/dialog.js": "WA_EMBEDDED_SIGNUP", "/es/dialog.css": "--brand"} {
		w := httptest.NewRecorder()
		h.signup.browser().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code)
		}
		if path == "/sdk.js" && w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("SDK without CORS cannot load in a crossorigin script tag")
		}
		if path == "/es/dialog" && !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("dialog CSP missing")
		}
	}
	w := httptest.NewRecorder()
	h.signup.browser().ServeHTTP(w, httptest.NewRequest("GET", "/es/dialog/numbers", nil))
	if !strings.Contains(w.Body.String(), "919888800001") {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	h.signup.browser().ServeHTTP(w, httptest.NewRequest("POST", "/es/dialog/finish", strings.NewReader("not json")))
	if w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
}
