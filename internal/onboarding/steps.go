package onboarding

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/jackc/pgx/v5"
)

// stepError is a classified step failure. Transient failures are retried by
// the worker; others stop the session, and Retryable allows a manual resume.
type stepError struct {
	Code, Message string
	Transient     bool
	Retryable     bool
	MetaCode      int
}

func (e *stepError) Error() string { return e.Code + ": " + e.Message }

const syncWindow = 24 * time.Hour

// run executes steps of a claimed session until it completes, fails, waits
// for a retry or the deadline passes. The caller holds the lease.
func (s *Service) run(ctx context.Context, r *record, deadline time.Time) error {
	// Saves outlive a closed request, so a lease is never left behind.
	store := context.WithoutCancel(ctx)
	for r.State == "working" {
		name := r.nextStep()
		if name == "" {
			finishCtx, cancel := context.WithTimeout(store, 10*time.Second)
			s.finish(finishCtx, r)
			cancel()
			break
		}
		if time.Now().After(deadline) {
			now := s.now()
			r.nextAttemptAt = &now
			break
		}
		r.mark(name, "running", s.now())
		if err := s.save(store, r, true); err != nil {
			return err
		}
		stepCtx, cancel := context.WithDeadline(ctx, deadline.Add(3*time.Second))
		status, err := s.runStep(stepCtx, r, name)
		cancel()
		if err != nil {
			if s.stepFailed(r, name, err) {
				continue
			}
			break
		}
		r.mark(name, status, s.now())
		if r.Error != nil && r.Error.Code != "sync_window_passed" {
			r.Error = nil
		}
		r.attempts = 0
	}
	if r.State == "working" && r.nextAttemptAt == nil {
		now := s.now()
		r.nextAttemptAt = &now
	}
	if err := s.save(store, r, false); err != nil {
		return err
	}
	if r.State == "working" {
		s.wakeWorker()
	}
	return nil
}

// save writes the session in its own transaction.
func (s *Service) save(ctx context.Context, r *record, keepLease bool) error {
	tx, err := s.cfg.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err = s.write(ctx, tx, r, keepLease); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) runStep(ctx context.Context, r *record, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	app, err := s.cfg.Store.SignupApp(ctx)
	if err != nil || app.Container != r.container {
		return "", &stepError{Code: "signup_unavailable", Message: "Self-service signup is not available right now.", Retryable: true}
	}
	g := s.graph(app)
	switch name {
	case "exchange":
		if !r.hasToken() && r.ChannelKey == nil {
			return "", &stepError{Code: "code_invalid", Message: "The Facebook step timed out. Start again."}
		}
		return "done", nil
	case "verify":
		return "done", s.verify(ctx, g, r)
	case "channel":
		return "done", s.channel(ctx, r)
	case "subscribe":
		return "done", s.subscribe(ctx, g, r)
	case "register":
		if r.coexistence() {
			return "skipped", nil
		}
		return "done", s.register(ctx, g, r)
	case "sync_contacts", "sync_history":
		if !r.coexistence() {
			return "skipped", nil
		}
		return "done", s.smbSync(ctx, g, r, name)
	}
	return "", fmt.Errorf("unknown step %s", name)
}

func (s *Service) graph(app containers.SignupApp) graph {
	return graph{client: s.cfg.Client, base: s.cfg.GraphBase, version: app.GraphVersion, appID: app.AppID, secret: app.AppSecret}
}

// token is the business token: in the session until the channel holds it.
func (s *Service) token(ctx context.Context, r *record) (string, error) {
	if r.hasToken() {
		return s.cfg.Store.Open(r.tokenCipher, r.tokenNonce)
	}
	if r.ChannelKey == nil {
		return "", &stepError{Code: "code_invalid", Message: "The Facebook step timed out. Start again."}
	}
	credential, err := s.cfg.Store.Credential(ctx, "WhatsApp", *r.ChannelKey)
	if err != nil {
		return "", err
	}
	return credential.Secret, nil
}

// verify checks the token with Meta and picks the WABA and number. The
// browser's ids are hints; debug_token and the WABA's numbers decide.
func (s *Service) verify(ctx context.Context, g graph, r *record) error {
	token, err := s.token(ctx, r)
	if err != nil {
		return err
	}
	if fresh, err := loadRecord(ctx, s.cfg.DB, r.SessionID, false); err == nil {
		r.metaEvent = fresh.metaEvent
		if r.FlowActual == nil {
			r.FlowActual = fresh.FlowActual
		}
	}
	targets, err := g.tokenTargets(ctx, token)
	if err != nil {
		return metaStep(err, "app_not_approved", "Relay cannot reach this WhatsApp account yet.")
	}
	hint := r.hint()
	waba := hint["waba_id"]
	if !slices.Contains(targets, waba) {
		if len(targets) != 1 {
			return &stepError{Code: "waba_not_found", Message: "Pick one WhatsApp Business account in the Facebook window. Start again."}
		}
		waba = targets[0]
	}
	account, err := g.waba(ctx, token, waba)
	if err != nil {
		return metaStep(err, "app_not_approved", "Relay cannot reach this WhatsApp account yet.")
	}
	phones, err := g.phones(ctx, token, waba)
	if err != nil {
		return metaStep(err, "app_not_approved", "Relay cannot reach this WhatsApp account yet.")
	}
	phone := pickPhone(phones, hint["phone_number_id"])
	if phone == nil {
		return &stepError{Code: "phone_not_found", Message: "Meta has not added the number yet.", Transient: true, Retryable: true}
	}
	id := text(phone["id"])
	status, _ := g.phoneStatus(ctx, token, id)
	for key, value := range status {
		phone[key] = value
	}
	owner, _ := account["owner_business_info"].(map[string]any)
	business := text(owner["id"])
	if business == "" {
		business = hint["business_id"]
	}
	r.WABAID, r.phoneID, r.Phone, r.PaymentMethodReady = &waba, &id, phoneProfile(phone), g.fundingReady(ctx, token, waba)
	if business != "" {
		r.BusinessID = &business
	}
	s.decideFlow(r, status)
	return nil
}

func (r *record) hint() map[string]string {
	hint := map[string]string{}
	data, _ := r.metaEvent["data"].(map[string]any)
	for _, key := range []string{"waba_id", "phone_number_id", "business_id"} {
		hint[key] = text(data[key])
	}
	return hint
}

// pickPhone takes the hinted number, else the only number, else the last one listed.
func pickPhone(phones []map[string]any, hint string) map[string]any {
	for _, phone := range phones {
		if hint != "" && text(phone["id"]) == hint {
			return phone
		}
	}
	if len(phones) == 0 {
		return nil
	}
	return phones[len(phones)-1]
}

// decideFlow follows Meta's event; without one, a number still on the
// Business app is coexistence.
func (s *Service) decideFlow(r *record, status map[string]any) {
	if r.FlowActual == nil {
		actual := r.Flow
		if onApp, _ := status["is_on_biz_app"].(bool); onApp {
			actual = "coexistence"
		} else if actual == "coexistence" {
			actual = "new"
		}
		r.FlowActual = &actual
	}
	if r.coexistence() && r.Coexistence == nil {
		r.Coexistence = map[string]any{"contacts": "pending", "history": "pending", "history_progress": 0}
	}
}

// channel creates or updates wa-<phone id> in the signup container and links
// it to the site. A number the site already has keeps its key and its PIN.
func (s *Service) channel(ctx context.Context, r *record) error {
	token, err := s.token(ctx, r)
	if err != nil {
		return err
	}
	key := "wa-" + *r.phoneID
	err = s.cfg.DB.QueryRow(ctx, `SELECT channel_key FROM relay_channel_credentials WHERE channel='WhatsApp' AND address=$1 AND site_id=$2`, *r.phoneID, r.site).Scan(&key)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	pin := ""
	if stored, err := s.cfg.Store.Credential(ctx, "WhatsApp", key); err == nil {
		pin, _ = stored.Config["pin"].(string)
	}
	if pin == "" {
		if pin, err = containers.NewPIN(); err != nil {
			return err
		}
	}
	config := map[string]any{"phone_id": *r.phoneID, "waba_id": *r.WABAID, "display_number": text(r.Phone["display_phone_number"]),
		"templates": true, "sync_metadata": true, "groups": false, "calls": false, "onboarding": *r.FlowActual,
		"coexistence": r.coexistence(), "partner_status": "active", "pin": pin,
		// The verify step checked the token and the number with Meta, so the Hub shows the number as connected.
		"connection_status": "connected", "verified_at": s.now().Format(time.RFC3339)}
	if r.BusinessID != nil {
		config["business_id"] = *r.BusinessID
	}
	if r.coexistence() {
		config["messages_per_second"] = 20
	}
	label := text(r.Phone["verified_name"])
	if label == "" {
		label = text(r.Phone["display_phone_number"])
	}
	channel := containers.Channel{Key: key, Channel: "WhatsApp", Container: r.container, Label: label, Enabled: true, Config: config}
	key, err = s.cfg.Store.SaveSignupChannel(ctx, r.site, channel, token)
	if errors.Is(err, containers.ErrNumberInUse) || errors.Is(err, containers.ErrConflict) {
		return &stepError{Code: "number_in_use", Message: "This number is already connected to another Relay site."}
	}
	if err != nil {
		return err
	}
	r.ChannelKey, r.tokenCipher, r.tokenNonce = &key, nil, nil
	if err = s.cfg.Reload(ctx); err != nil {
		return err
	}
	_ = s.cfg.Metadata.MarkTemplatesStale(ctx, *r.WABAID)
	_ = s.cfg.Metadata.RefreshAccounts(ctx, *r.WABAID)
	return nil
}

// subscribe subscribes the app to the WABA; an unknown outcome is checked with G6.
func (s *Service) subscribe(ctx context.Context, g graph, r *record) error {
	token, err := s.token(ctx, r)
	if err != nil {
		return err
	}
	err = g.subscribe(ctx, token, *r.WABAID)
	if err == nil {
		return nil
	}
	var meta *metaError
	if errors.Is(err, errUnavailable) || (errors.As(err, &meta) && meta.retryLater()) {
		if ok, checkErr := g.subscribed(ctx, token, *r.WABAID); checkErr == nil && ok {
			return nil
		}
	}
	return metaStep(err, "subscribe_failed", "Meta did not turn on messages for this number.")
}

// register registers a new or moved number with the stored PIN, at most 8 times in 72 hours.
func (s *Service) register(ctx context.Context, g graph, r *record) error {
	token, err := s.token(ctx, r)
	if err != nil {
		return err
	}
	allowed, err := s.cfg.Store.RecordRegisterAttempt(ctx, *r.ChannelKey, s.now(), 72*time.Hour, 8)
	if err != nil {
		return err
	}
	if !allowed {
		return &stepError{Code: "register_rate_limited", Message: "Meta blocks number registration for 72 hours after too many tries.", MetaCode: registerRateLimited}
	}
	credential, err := s.cfg.Store.Credential(ctx, "WhatsApp", *r.ChannelKey)
	if err != nil {
		return err
	}
	pin, _ := credential.Config["pin"].(string)
	err = g.register(ctx, token, *r.phoneID, pin)
	var meta *metaError
	if errors.As(err, &meta) && meta.Code == registerRateLimited {
		return &stepError{Code: "register_rate_limited", Message: "Meta blocks number registration for 72 hours after too many tries.", MetaCode: meta.Code}
	}
	return metaStep(err, "register_failed", "Meta did not register the number.")
}

// smbSync requests the coexistence contacts or history copy once, within 24 hours of the code.
func (s *Service) smbSync(ctx context.Context, g graph, r *record, name string) error {
	field, kind := "contacts", "smb_app_state_sync"
	if name == "sync_history" {
		field, kind = "history", "history"
	}
	if r.codeReceivedAt == nil || s.now().Sub(*r.codeReceivedAt) > syncWindow {
		r.Coexistence[field] = "failed"
		return &stepError{Code: "sync_window_passed", Message: "Meta allows copying chats and contacts only in the first 24 hours."}
	}
	token, err := s.token(ctx, r)
	if err != nil {
		return err
	}
	err = g.smbSync(ctx, token, *r.phoneID, kind)
	var meta *metaError
	if err == nil || (errors.As(err, &meta) && meta.alreadyRequested()) {
		r.Coexistence[field] = "requested"
		return nil
	}
	return metaStep(err, "sync_failed", "Meta did not start copying from the WhatsApp Business app.")
}

// metaStep classifies a Graph failure: transient errors retry, others stop with Meta's text.
func metaStep(err error, code, message string) error {
	var classified *stepError
	if errors.As(err, &classified) || err == nil {
		return err
	}
	var meta *metaError
	if errors.Is(err, errUnavailable) || (errors.As(err, &meta) && meta.retryLater()) {
		return &stepError{Code: "meta_unavailable", Message: "Meta is not answering. Relay tries again.", Transient: true, Retryable: true}
	}
	if errors.As(err, &meta) {
		if meta.Message != "" {
			message += " Meta says: " + meta.Message
		}
		return &stepError{Code: code, Message: message, Retryable: true, MetaCode: meta.Code}
	}
	return err
}

// stepFailed stores a failure. Transient failures back off; others stop the
// session. It reports whether the session goes on with the next step.
func (s *Service) stepFailed(r *record, name string, err error) bool {
	now := s.now()
	var failure *stepError
	if !errors.As(err, &failure) {
		failure = &stepError{Code: "hub_unavailable", Message: "Relay Hub could not finish this step. Relay tries again.", Transient: true, Retryable: true}
	}
	r.mark(name, "failed", now)
	r.Error = &SessionError{Code: failure.Code, Message: failure.Message, Retryable: failure.Retryable, MetaCode: failure.MetaCode}
	if failure.Code == "sync_window_passed" {
		r.mark(name, "skipped", now)
		return true
	}
	if !failure.Transient {
		r.State = "failed"
		return false
	}
	r.attempts++
	if r.attempts >= 10 {
		r.State = "failed"
		return false
	}
	next := now.Add(backoff(r.attempts))
	r.nextAttemptAt = &next
	return false
}

// backoff is 30 s, 2 min, 10 min, 1 h, then every 6 h.
func backoff(attempt int) time.Duration {
	return []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}[min(attempt, 5)-1]
}

// finish completes the session and reads the payment method once.
func (s *Service) finish(ctx context.Context, r *record) {
	r.State = "completed"
	r.nextAttemptAt = nil
	app, err := s.cfg.Store.SignupApp(ctx)
	if err != nil || r.WABAID == nil {
		return
	}
	token, err := s.token(ctx, r)
	if err != nil {
		return
	}
	if ready := s.graph(app).fundingReady(ctx, token, *r.WABAID); ready != nil {
		r.PaymentMethodReady = ready
	}
}
