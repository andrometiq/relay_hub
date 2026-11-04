package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/containers"
)

type window struct {
	h       *harness
	mux     *http.ServeMux
	id      string
	secret  string
	session Session
}

func (h *harness) startSignup(site, flow string) *window {
	h.t.Helper()
	session, launch, err := h.service.Start(h.ctx, site, flow, map[string]any{"business": map[string]any{"name": "Acme", "country": "IN"}, "phone": map[string]any{"display_name": "Acme"}}, "admin@acme.test")
	if err != nil {
		h.t.Fatal(err)
	}
	prefix := "https://hub.example.test/onboard/whatsapp/" + session.SessionID + "#"
	if !strings.HasPrefix(launch, prefix) {
		h.t.Fatalf("launch url %q", launch)
	}
	mux := http.NewServeMux()
	h.service.Register(mux)
	return &window{h: h, mux: mux, id: session.SessionID, secret: strings.TrimPrefix(launch, prefix), session: session}
}

func (w *window) call(method, path, secret string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "/onboard/whatsapp/"+w.id+path, reader)
	if secret != "" {
		r.Header.Set("X-Relay-Launch", secret)
	}
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, r)
	return rec
}

func (w *window) post(path string, body any) Session {
	w.h.t.Helper()
	rec := w.call("POST", path, w.secret, body)
	var session Session
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &session) != nil {
		w.h.t.Fatalf("%s %d %s", path, rec.Code, rec.Body)
	}
	return session
}

func (w *window) finish(event, waba, phone, code string) Session {
	w.h.t.Helper()
	w.post("/event", map[string]any{"event": event, "data": map[string]string{"waba_id": waba, "phone_number_id": phone}, "origin": "https://www.facebook.com"})
	return w.post("/code", map[string]any{"code": code})
}

func stepStatus(session Session, name string) string {
	for _, step := range session.Steps {
		if step.Name == name {
			return step.Status
		}
	}
	return ""
}

func TestNewNumberSignupThroughTheHubWindow(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")

	if rec := w.call("GET", "/config", "", nil); rec.Code != 404 {
		t.Fatalf("config without launch secret %d", rec.Code)
	}
	if rec := w.call("GET", "/config", "wrong", nil); rec.Code != 404 {
		t.Fatalf("config with a wrong secret %d", rec.Code)
	}
	page := w.call("GET", "", "", nil)
	if page.Code != 200 || !strings.Contains(page.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || page.Header().Get("Referrer-Policy") != "no-referrer" || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page headers %d %v", page.Code, page.Header())
	}
	rec := w.call("GET", "/config", w.secret, nil)
	var config map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &config) != nil {
		t.Fatalf("config %d %s", rec.Code, rec.Body)
	}
	extras := config["extras"].(map[string]any)
	if config["app_id"] != "333" || config["config_id"] != "es-1" || config["state"] != "waiting_for_meta" || config["site_origin"] != h.core.server.URL || extras["featureType"] != "" || extras["sessionInfoVersion"] != "3" || extras["version"] != nil || extras["features"] != nil {
		t.Fatalf("config %s", rec.Body)
	}
	if business := extras["setup"].(map[string]any)["business"].(map[string]any); business["address"].(map[string]any)["country"] != "IN" {
		t.Fatalf("prefill %v", extras["setup"])
	}
	if strings.Contains(rec.Body.String(), "app-secret") {
		t.Fatal("app secret reached the window")
	}
	if bad := w.call("POST", "/event", w.secret, map[string]any{"event": "FINISH", "data": map[string]string{}, "origin": "https://evil.test"}); bad.Code != 422 {
		t.Fatalf("event from a foreign origin %d", bad.Code)
	}

	done := w.finish("FINISH", "230", "120", "code-230")
	if done.State != "completed" || *done.ChannelKey != "wa-120" || *done.WABAID != "230" || *done.BusinessID != "330" || done.Phone["messaging_limit"] != "TIER_250" {
		t.Fatalf("signup %+v", done)
	}
	if stepStatus(done, "register") != "done" || stepStatus(done, "sync_history") != "skipped" || *done.FlowActual != "new" || done.PaymentMethodReady == nil || *done.PaymentMethodReady {
		t.Fatalf("steps %+v", done.Steps)
	}
	again := w.post("/code", map[string]any{"code": "code-230"})
	if again.State != "completed" || h.meta.count("access_token") != 1 || h.meta.count("register") != 1 {
		t.Fatalf("repeated code: %s, G1 %d, G7 %d", again.State, h.meta.count("access_token"), h.meta.count("register"))
	}
	if rec := w.call("POST", "/code", w.secret, map[string]any{"code": "code-other"}); rec.Code != 409 {
		t.Fatalf("second code %d", rec.Code)
	}
	channels, _ := h.store.Channels(h.ctx, containers.ChannelFilter{Site: site})
	if len(channels) != 1 || channels[0].Key != "wa-120" || channels[0].Config["onboarding"] != "new" || channels[0].Config["coexistence"] != false {
		t.Fatalf("site channels %+v", channels)
	}
	credential, _ := h.store.Credential(h.ctx, "WhatsApp", "wa-120")
	if credential.Secret != "biz-code-230" || len(credential.Config["pin"].(string)) != 6 {
		t.Fatal("number token or PIN not stored")
	}
	if stored := h.session(w.id); stored.hasToken() {
		t.Fatal("business token kept in the session after the channel took it")
	}
	h.meta.funded["230"] = true
	refreshed, err := h.service.Get(h.ctx, site, w.id, true)
	if err != nil || refreshed.PaymentMethodReady == nil || !*refreshed.PaymentMethodReady {
		t.Fatalf("payment refresh %+v %v", refreshed.PaymentMethodReady, err)
	}
	var events int
	h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE site_id=$1 AND payload->>'object'='relay_signup' AND payload->>'session_id'=$2`, site, w.id).Scan(&events)
	if events == 0 {
		t.Fatal("no relay_signup events journaled")
	}
}

func TestSignupHintIsCheckedAgainstTokenScopes(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	done := w.finish("FINISH", "231", "121", "code-230")
	if done.State != "completed" || *done.WABAID != "230" || *done.ChannelKey != "wa-120" {
		t.Fatalf("forged hint used: %+v", done)
	}
	other := h.startSignup(site, "migrate")
	h.meta.fault("no_scope", 1)
	failed := other.post("/code", map[string]any{"code": "code-231"})
	if failed.State != "failed" || failed.Error.Code != "app_not_approved" {
		t.Fatalf("token without scopes: %+v", failed.Error)
	}
}

func TestStartReturnsTheRunningSignupAndSupersedesOldOnes(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	first := h.startSignup(site, "new")
	second := h.startSignup(site, "new")
	if first.id == second.id || h.session(first.id).State != "superseded" {
		t.Fatal("unused signup was not superseded")
	}
	var hints int
	h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE site_id=$1 AND payload->>'object'='relay_signup' AND payload->>'session_id'=$2 AND payload->>'state'='superseded'`, site, first.id).Scan(&hints)
	if hints != 1 {
		t.Fatal("Core got no hint that the old signup was superseded", hints)
	}
	h.meta.fault("subscribe_503", 1)
	working := second.finish("FINISH", "230", "120", "code-230")
	if working.State != "working" || working.Error == nil || !working.Error.Retryable {
		t.Fatalf("transient failure %+v", working)
	}
	third := h.startSignup(site, "new")
	if third.id != second.id {
		t.Fatal("start opened a second signup while one was running")
	}
	if rec := first.call("GET", "/config", first.secret, nil); rec.Code != 200 {
		t.Fatal("superseded window cannot read its state")
	}
	if rec := second.call("GET", "/state", second.secret, nil); rec.Code != 404 {
		t.Fatal("rotated launch secret still works")
	}
	if _, _, err := h.service.Start(h.ctx, site, "phone", nil, ""); failureCode(err) != "invalid_request" {
		t.Fatalf("invalid flow: %v", err)
	}
}

func TestWorkerResumesAfterTransientFailureWithLease(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	h.meta.fault("subscribe_503", 1)
	working := w.finish("FINISH", "230", "120", "code-230")
	if working.State != "working" || stepStatus(working, "subscribe") != "failed" || h.meta.count("subscribed_check") != 1 {
		t.Fatalf("after 503: %+v", working.Steps)
	}
	h.service.Tick(h.ctx)
	if h.session(w.id).State != "working" {
		t.Fatal("worker ran before the backoff")
	}
	h.clock.add(31 * time.Second)
	if _, err := h.service.claim(h.ctx, w.id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.claim(h.ctx, w.id); err == nil {
		t.Fatal("two workers claimed one session")
	}
	h.clock.add(leaseTime)
	restarted := h.newService()
	restarted.Tick(h.ctx)
	done := h.session(w.id)
	if done.State != "completed" || h.meta.count("access_token") != 1 || h.meta.count("register") != 1 {
		t.Fatalf("resume after restart: %s G1 %d G7 %d", done.State, h.meta.count("access_token"), h.meta.count("register"))
	}
	channels, _ := h.store.Channels(h.ctx, containers.ChannelFilter{Site: site})
	if len(channels) != 1 {
		t.Fatalf("channels after resume %d", len(channels))
	}
}

func TestCodeCutOffContinuesInWorker(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	budget := codeBudget
	codeBudget = 0
	t.Cleanup(func() { codeBudget = budget })
	working := w.finish("FINISH", "230", "120", "code-230")
	if working.State != "working" || stepStatus(working, "exchange") != "done" || stepStatus(working, "verify") != "pending" {
		t.Fatalf("cut-off %+v", working.Steps)
	}
	h.service.Tick(h.ctx)
	if state := h.session(w.id).State; state != "completed" {
		t.Fatalf("worker did not continue: %s", state)
	}
}

func TestCodeInvalidCanRetryInTheSameWindow(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	failed := w.post("/code", map[string]any{"code": "bad"})
	if failed.State != "failed" || failed.Error.Code != "code_invalid" || failed.Error.Retryable {
		t.Fatalf("bad code %+v", failed.Error)
	}
	if resumed, _ := h.service.Resume(h.ctx, site, w.id); resumed.State != "failed" {
		t.Fatal("resume revived a session without a token")
	}
	if done := w.post("/code", map[string]any{"code": "code-230"}); done.State != "completed" {
		t.Fatalf("retry after a bad code: %s", done.State)
	}
}

func TestCancelAndMetaErrorBeforeTheCode(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	cancelled := w.post("/event", map[string]any{"event": "CANCEL", "data": map[string]string{"current_step": "PHONE_NUMBER_SETUP"}, "origin": "https://business.facebook.com"})
	if cancelled.State != "cancelled" || cancelled.Error.MetaStep != "PHONE_NUMBER_SETUP" {
		t.Fatalf("cancel %+v", cancelled)
	}
	failed := w.post("/event", map[string]any{"event": "ERROR", "data": map[string]string{"error_message": "Onboarding limit reached", "error_code": "2388001"}, "origin": "https://www.facebook.com"})
	if failed.State != "failed" || failed.Error.Message != "Onboarding limit reached" || failed.Error.MetaCode != 2388001 {
		t.Fatalf("meta error %+v", failed.Error)
	}
	if done := w.finish("FINISH", "230", "120", "code-230"); done.State != "completed" {
		t.Fatalf("Try again in the same window: %s", done.State)
	}
	if _, err := h.service.Cancel(h.ctx, site, w.id); failureCode(err) != "session_busy" {
		t.Fatalf("cancel after the code: %v", err)
	}
	fresh := h.startSignup(site, "new")
	if cancelled, err := h.service.Cancel(h.ctx, site, fresh.id); err != nil || cancelled.State != "cancelled" {
		t.Fatalf("cancel before the code %v", err)
	}
	h.clock.add(waitForMeta + time.Minute)
	expiring := h.startSignup(site, "new")
	h.clock.add(waitForMeta + time.Minute)
	h.service.Tick(h.ctx)
	if h.session(expiring.id).State != "expired" {
		t.Fatal("waiting signup did not expire")
	}
}

func TestNumberOfAnotherSiteIsRefused(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	if done := h.startSignup(site, "new").finish("FINISH", "230", "120", "code-230"); done.State != "completed" {
		t.Fatal(done.State)
	}
	otherCore := newFakeCore(t)
	request := h.enrolRequest()
	request.Origin, request.CallbackSecret = otherCore.server.URL, otherCore.callbackSecret
	other, err := h.service.Enrol(h.ctx, h.token("bench", otherCore.server.URL, nil), request, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := h.startSignup(other.SiteID, "new").finish("FINISH", "230", "120", "code-230")
	if failed.State != "failed" || failed.Error.Code != "number_in_use" || failed.Error.Retryable {
		t.Fatalf("number of another site: %+v", failed.Error)
	}
	channel, _ := h.store.GetChannel(h.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-120"})
	if channel.Site != site {
		t.Fatal("first site's channel changed")
	}
	if _, err := h.service.Get(h.ctx, site, failed.SessionID, false); failureCode(err) != "not_found" {
		t.Fatalf("foreign session readable: %v", err)
	}
}

func TestRegisterRateLimitIsNotRetried(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	h.meta.fault("register_133016", 1)
	failed := h.startSignup(site, "migrate").finish("FINISH", "230", "120", "code-230")
	if failed.State != "failed" || failed.Error.Code != "register_rate_limited" || failed.Error.MetaCode != 133016 {
		t.Fatalf("133016 %+v", failed.Error)
	}
	h.clock.add(time.Hour)
	h.service.Tick(h.ctx)
	if h.meta.count("register") != 1 {
		t.Fatal("rate-limited register retried")
	}
}

func TestCoexistenceSkipsRegisterAndSyncsOnce(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	h.meta.onApp["120"] = true
	w := h.startSignup(site, "coexistence")
	done := w.finish("FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING", "230", "120", "code-230")
	if done.State != "completed" || *done.FlowActual != "coexistence" || stepStatus(done, "register") != "skipped" || h.meta.count("register") != 0 {
		t.Fatalf("coexistence %+v", done.Steps)
	}
	if done.Coexistence["contacts"] != "requested" || done.Coexistence["history"] != "requested" || h.meta.count("smb_app_data") != 2 {
		t.Fatalf("smb sync %v calls %d", done.Coexistence, h.meta.count("smb_app_data"))
	}
	if _, err := h.service.Resume(h.ctx, site, w.id); err != nil || h.meta.count("smb_app_data") != 2 {
		t.Fatal("resume of a completed signup synced again")
	}
	channel, _ := h.store.GetChannel(h.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-120"})
	if channel.Config["coexistence"] != true || channel.Config["messages_per_second"] != float64(20) {
		t.Fatalf("coexistence channel %v", channel.Config)
	}
}

func TestCoexistenceSyncWindowAndAlreadyRequested(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	h.meta.onApp["120"] = true
	h.meta.smb["120:smb_app_state_sync"] = 1
	h.meta.fault("subscribe_503", 1)
	w := h.startSignup(site, "coexistence")
	if working := w.finish("FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING", "230", "120", "code-230"); working.State != "working" {
		t.Fatal(working.State)
	}
	h.clock.add(syncWindow + time.Hour)
	h.service.Tick(h.ctx)
	done := h.session(w.id)
	if done.State != "completed" || done.Error == nil || done.Error.Code != "sync_window_passed" || done.Coexistence["history"] != "failed" {
		t.Fatalf("after the window: %s %+v %v", done.State, done.Error, done.Coexistence)
	}

	h.clock.add(-syncWindow - time.Hour)
	second := newHarness(t)
	second.signupContainer()
	secondSite, _ := second.enrolledSite()
	second.meta.onApp["120"] = true
	second.meta.smb["120:smb_app_state_sync"] = 1
	if done := second.startSignup(secondSite, "coexistence").finish("FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING", "230", "120", "code-230"); done.Coexistence["contacts"] != "requested" || done.Error != nil {
		t.Fatalf("already requested: %v %+v", done.Coexistence, done.Error)
	}
}

func TestPartnerRemovedDisablesTheNumber(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	w.finish("FINISH", "230", "120", "code-230")
	if err := h.service.AccountUpdate(h.ctx, "230", "PARTNER_REMOVED"); err != nil {
		t.Fatal(err)
	}
	channel, _ := h.store.GetChannel(h.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-120"})
	if channel.Enabled || channel.Config["partner_status"] != "removed" {
		t.Fatalf("channel after removal %+v", channel)
	}
	if session := h.session(w.id); session.Error == nil || session.Error.Code != "partner_removed" {
		t.Fatal("signup does not show the removal")
	}
	var notices int
	h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE site_id=$1 AND payload->>'object'='relay_channels'`, site).Scan(&notices)
	if notices < 2 {
		t.Fatalf("site not told about the removal: %d", notices)
	}
}

func TestAClosedWindowDuringTheExchangeStillSavesTheResult(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	closed, cancel := context.WithCancel(h.ctx)
	h.meta.onExchange = cancel
	if _, err := h.service.SubmitCode(closed, w.id, "bad", nil); err != nil {
		t.Fatal(err)
	}
	if r := h.session(w.id); r.State != "failed" || r.Error == nil || r.Error.Code != "code_invalid" {
		t.Fatalf("failed exchange after a closed window: %s %+v", r.State, r.Error)
	}
	retry := h.startSignup(site, "new")
	closed, cancel = context.WithCancel(h.ctx)
	h.meta.onExchange = cancel
	h.service.SubmitCode(closed, retry.id, "code-230", nil)
	h.meta.onExchange = nil
	if r := h.session(retry.id); r.State != "completed" {
		t.Fatalf("a closed window stopped the steps: %s %+v", r.State, r.Steps)
	}
}

func TestACodeRequestThatNeverSavedIsFailedByTheWorker(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	w := h.startSignup(site, "new")
	// The Hub stops after it accepted the code and before the exchange result is saved.
	if _, err := h.service.update(h.ctx, w.id, func(r *record) error { return h.service.acceptCode(r, "lost-code", nil) }); err != nil {
		t.Fatal(err)
	}
	h.service.Tick(h.ctx)
	if h.session(w.id).State != "working" {
		t.Fatal("worker took the session while the code request held it")
	}
	h.clock.add(leaseTime)
	h.newService().Tick(h.ctx)
	r := h.session(w.id)
	if r.State != "failed" || r.Error == nil || r.Error.Code != "code_invalid" {
		t.Fatalf("lost exchange: %s %+v", r.State, r.Error)
	}
	if again := h.startSignup(site, "new"); again.id == w.id || h.session(w.id).State != "superseded" {
		t.Fatal("a new signup could not replace the failed one")
	}
}

func TestACodeForAReplacedSignupSaysSo(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	old := h.startSignup(site, "new")
	h.startSignup(site, "new")
	rec := old.call("POST", "/code", old.secret, map[string]any{"code": "code-230"})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "session_superseded") {
		t.Fatalf("code for a superseded signup: %d %s", rec.Code, rec.Body)
	}
}

func TestASignupNumberShowsAsConnected(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	if done := h.startSignup(site, "new").finish("FINISH", "230", "120", "code-230"); done.State != "completed" {
		t.Fatalf("signup %s", done.State)
	}
	channel, err := h.store.GetChannel(h.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-120"})
	if err != nil || channel.Config["connection_status"] != "connected" || channel.Config["verified_at"] == nil {
		t.Fatalf("signup number status %v %v", channel.Config, err)
	}
}

func TestSignupAdoptsTheSiteNumberAndKeepsItsKey(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, _ := h.enrolledSite()
	if err := h.store.SaveContainer(h.ctx, containers.Container{ID: "manual", Name: "Manual", Enabled: true}, nil, true); err != nil {
		t.Fatal(err)
	}
	manual := containers.Channel{Key: "shop-line", Channel: "WhatsApp", Container: "manual", Enabled: true, Config: map[string]any{"phone_id": "120", "waba_id": "230"}}
	if err := h.store.SaveChannel(h.ctx, manual, "old-token", true); err != nil {
		t.Fatal(err)
	}
	if err := h.store.LinkChannel(h.ctx, site, containers.ChannelRef{Channel: "WhatsApp", Key: "shop-line"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		done := h.startSignup(site, "new").finish("FINISH", "230", "120", "code-230")
		if done.State != "completed" || done.ChannelKey == nil || *done.ChannelKey != "shop-line" {
			t.Fatalf("adopting signup %s %v", done.State, done.ChannelKey)
		}
	}
	adopted, err := h.store.Credential(h.ctx, "WhatsApp", "shop-line")
	if err != nil || adopted.Container != "signup" || adopted.Secret != "biz-code-230" {
		t.Fatalf("adopted number %+v %v", adopted.Container, err)
	}
	if _, err = h.store.GetChannel(h.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-120"}); err == nil {
		t.Fatal("signup made a second channel for the number")
	}
}
