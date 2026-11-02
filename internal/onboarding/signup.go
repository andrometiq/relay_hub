package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/jackc/pgx/v5"
)

const (
	waitForMeta  = 30 * time.Minute
	codeLifetime = 7 * 24 * time.Hour
)

// codeBudget is how long a code request runs steps before the worker takes over.
var codeBudget = 25 * time.Second

var metaEvents = []string{"FINISH", "FINISH_ONLY_WABA", "FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING", "FINISH_GRANT_ONLY_API_ACCESS", "CANCEL", "ERROR"}

var codePattern = regexp.MustCompile(`^[A-Za-z0-9_.~#-]+$`)

// Start opens a signup for the site and returns the Hub window URL. A
// running signup is returned instead of a new one; older signups that never
// got a code are superseded.
func (s *Service) Start(ctx context.Context, site, flow string, prefill map[string]any, actor string) (Session, string, error) {
	invalid := fail(422, "invalid_request", "The signup request is not valid.")
	raw, _ := json.Marshal(prefill)
	if !validFlow(flow) || len(raw) > 8192 || len(actor) > 200 {
		return Session{}, "", invalid
	}
	app, err := s.cfg.Store.SignupApp(ctx)
	if errors.Is(err, containers.ErrNoSignupContainer) {
		return Session{}, "", fail(503, "signup_unavailable", "Self-service WhatsApp signup is not set up on this Hub.")
	}
	if err != nil {
		return Session{}, "", unavailable()
	}
	tx, err := s.cfg.DB.Begin(ctx)
	if err != nil {
		return Session{}, "", unavailable()
	}
	defer tx.Rollback(context.Background())
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled FROM relay_sites WHERE site_id=$1 FOR UPDATE`, site).Scan(&enabled); err != nil || !enabled {
		return Session{}, "", fail(403, "site_unavailable", "This site is not active on Relay Hub.")
	}
	secret := auth.Secret()
	var id string
	err = tx.QueryRow(ctx, `UPDATE relay_signup_sessions SET launch_hash=$2,updated_at=$3 WHERE session_id=(
 SELECT session_id FROM relay_signup_sessions WHERE site_id=$1 AND state='working' ORDER BY created_at DESC LIMIT 1) RETURNING session_id`, site, auth.Hash(secret), s.now()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = s.insertSession(ctx, tx, site, app.Container, flow, raw, actor, secret)
	}
	if err != nil {
		return Session{}, "", unavailable()
	}
	r, err := loadRecord(ctx, tx, id, false)
	if err == nil {
		err = journal(ctx, tx, site, id, r.State, r.currentStep())
	}
	if err != nil || tx.Commit(ctx) != nil {
		return Session{}, "", unavailable()
	}
	return r.Session, s.cfg.PublicOrigin + "/onboard/whatsapp/" + id + "#" + secret, nil
}

func (s *Service) insertSession(ctx context.Context, tx pgx.Tx, site, container, flow string, prefill []byte, actor, secret string) (string, error) {
	now := s.now()
	rows, err := tx.Query(ctx, `UPDATE relay_signup_sessions SET state='superseded',updated_at=$2 WHERE site_id=$1
 AND (state IN ('created','waiting_for_meta','cancelled') OR (state='failed' AND channel_key IS NULL AND token_ciphertext IS NULL)) RETURNING session_id`, site, now)
	if err != nil {
		return "", err
	}
	superseded, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	// Core keeps a copy of each session; the hint lets it close the old ones too.
	for _, old := range superseded {
		if err = journal(ctx, tx, site, old, "superseded", ""); err != nil {
			return "", err
		}
	}
	id := "sus_" + auth.Secret()[:32]
	steps, _ := json.Marshal(freshSteps())
	_, err = tx.Exec(ctx, `INSERT INTO relay_signup_sessions(session_id,site_id,container_id,flow,steps,launch_hash,prefill,actor,created_at,updated_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$10)`, id, site, container, flow, steps, auth.Hash(secret), prefill, actor, now, now.Add(waitForMeta))
	return id, err
}

// Get reads a site's session. Refresh reads the payment method and number status from Meta once.
func (s *Service) Get(ctx context.Context, site, id string, refresh bool) (Session, error) {
	r, err := s.siteRecord(ctx, site, id)
	if err != nil || !refresh || r.ChannelKey == nil || r.WABAID == nil || r.phoneID == nil {
		return r.Session, err
	}
	app, err := s.cfg.Store.SignupApp(ctx)
	if err != nil {
		return r.Session, nil
	}
	token, err := s.token(ctx, &r)
	if err != nil {
		return r.Session, nil
	}
	g := s.graph(app)
	account, wabaErr := g.waba(ctx, token, *r.WABAID)
	status, phoneErr := g.phoneStatus(ctx, token, *r.phoneID)
	updated, err := s.update(ctx, id, func(row *record) error {
		if wabaErr == nil {
			ready := text(account["primary_funding_id"]) != ""
			row.PaymentMethodReady = &ready
		}
		if phoneErr == nil {
			row.Phone = phoneProfile(status)
		}
		return nil
	})
	if err != nil {
		return r.Session, nil
	}
	return updated.Session, nil
}

// Resume runs the next step now. Repeating it is safe.
func (s *Service) Resume(ctx context.Context, site, id string) (Session, error) {
	r, err := s.siteRecord(ctx, site, id)
	if err != nil {
		return Session{}, err
	}
	resumable := r.State == "working" || (r.State == "failed" && r.Error != nil && r.Error.Retryable && (r.hasToken() || r.ChannelKey != nil))
	if !resumable {
		return r.Session, nil
	}
	if _, err = s.update(ctx, id, func(row *record) error {
		if row.State == "failed" {
			row.State, row.attempts = "working", 0
		}
		now := s.now()
		row.nextAttemptAt = &now
		return nil
	}); err != nil {
		return r.Session, unavailable()
	}
	s.advance(ctx, id, codeBudget)
	r, err = s.siteRecord(ctx, site, id)
	return r.Session, err
}

// Cancel ends a session before Meta returned a code.
func (s *Service) Cancel(ctx context.Context, site, id string) (Session, error) {
	if _, err := s.siteRecord(ctx, site, id); err != nil {
		return Session{}, err
	}
	r, err := s.update(ctx, id, func(row *record) error {
		if row.codeHash != nil && row.State != "cancelled" && !row.acceptsCode() {
			return fail(409, "session_busy", "This signup already reached Meta and cannot be cancelled.")
		}
		if row.State != "expired" && row.State != "superseded" {
			row.State = "cancelled"
		}
		return nil
	})
	if err != nil {
		return Session{}, asFailure(err)
	}
	return r.Session, nil
}

// windowOpened moves a new session to waiting_for_meta when the Hub window loads.
func (s *Service) windowOpened(ctx context.Context, id string) (record, error) {
	return s.update(ctx, id, func(r *record) error {
		if r.State != "created" {
			return errUnchanged
		}
		r.State = "waiting_for_meta"
		return nil
	})
}

// RecordEvent stores an Embedded Signup message from the Hub window. FINISH
// events are hints; CANCEL and ERROR end a session that has no code yet.
func (s *Service) RecordEvent(ctx context.Context, id, event string, data map[string]any) (Session, error) {
	clean := map[string]any{}
	for _, key := range []string{"phone_number_id", "waba_id", "business_id", "current_step", "error_message", "error_code", "session_id"} {
		if value := safeText(text(data[key])); value != "" && len(value) <= 300 {
			clean[key] = value
		}
	}
	r, err := s.update(ctx, id, func(r *record) error {
		r.metaEvent, r.metaChanged = map[string]any{"event": event, "data": clean, "at": s.now()}, true
		if !r.acceptsCode() {
			s.followEvent(r, event)
			return nil
		}
		switch event {
		case "CANCEL":
			step := text(clean["current_step"])
			r.State = "cancelled"
			r.Error = &SessionError{Code: "cancelled", Message: "You closed Facebook at " + stepLabel(step) + ".", Retryable: true, MetaStep: step}
		case "ERROR":
			message := text(clean["error_message"])
			if message == "" {
				message = "Meta could not finish the signup."
			}
			r.State = "failed"
			r.Error = &SessionError{Code: "meta_error", Message: message, Retryable: true, MetaCode: atoi(text(clean["error_code"]))}
		default:
			s.followEvent(r, event)
		}
		return nil
	})
	return r.Session, err
}

// followEvent takes Meta's choice of flow while the number is not verified yet.
func (s *Service) followEvent(r *record, event string) {
	if r.WABAID != nil || !strings.HasPrefix(event, "FINISH") {
		return
	}
	actual := r.Flow
	if event == "FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING" {
		actual = "coexistence"
	} else if actual == "coexistence" {
		actual = "new"
	}
	r.FlowActual = &actual
}

func stepLabel(step string) string {
	if step == "" {
		return "the first step"
	}
	return strings.ToLower(strings.ReplaceAll(step, "_", " "))
}

func atoi(value string) int {
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' || n > 1e8 {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// SubmitCode exchanges Meta's code once and runs the steps for up to 25
// seconds; the worker does the rest. The same code again returns the state.
func (s *Service) SubmitCode(ctx context.Context, id, code string, hint map[string]any) (Session, error) {
	if len(code) > 2048 || !codePattern.MatchString(code) {
		return Session{}, fail(422, "invalid_request", "The Facebook code is not valid.")
	}
	hash := auth.Hash(code)
	accepted := false
	r, err := s.update(ctx, id, func(r *record) error {
		if r.codeHash != nil && *r.codeHash == hash {
			return errUnchanged
		}
		if !r.acceptsCode() {
			return fail(409, "code_conflict", "This signup already has a Facebook code. Start again to connect another number.")
		}
		now := s.now()
		lease, expires := now.Add(2*time.Minute), now.Add(codeLifetime)
		r.codeHash, r.State, r.codeReceivedAt, r.ExpiresAt, r.lease = &hash, "working", &now, expires, &lease
		r.Steps, r.Error, r.attempts, r.nextAttemptAt = freshSteps(), nil, 0, nil
		if event, _ := hint["event"].(string); event != "" && r.metaEvent == nil {
			data, _ := hint["data"].(map[string]any)
			r.metaEvent, r.metaChanged = map[string]any{"event": event, "data": data, "at": now}, true
			s.followEvent(r, event)
		}
		accepted = true
		return nil
	})
	if err != nil || !accepted {
		return r.Session, asFailureOr(err)
	}
	deadline := time.Now().Add(codeBudget)
	app, err := s.cfg.Store.SignupApp(ctx)
	var token string
	if err == nil {
		token, err = s.exchange(ctx, s.graph(app), code)
	}
	if err != nil {
		r.mark("exchange", "failed", s.now())
		r.State = "failed"
		r.Error = &SessionError{Code: "code_invalid", Message: "The Facebook step timed out. Start again."}
		if saveErr := s.save(ctx, &r, false); saveErr != nil {
			return r.Session, unavailable()
		}
		return r.Session, nil
	}
	if r.tokenCipher, r.tokenNonce, err = s.cfg.Store.Seal(token); err != nil {
		return r.Session, unavailable()
	}
	r.mark("exchange", "done", s.now())
	if err = s.run(ctx, &r, deadline); err != nil {
		return r.Session, unavailable()
	}
	return r.Session, nil
}

// exchange is G1 with one retry when Meta cannot be reached; the code lives 30 seconds.
func (s *Service) exchange(ctx context.Context, g graph, code string) (string, error) {
	token, err := g.exchange(ctx, code)
	if errors.Is(err, errUnavailable) {
		token, err = g.exchange(ctx, code)
	}
	return token, err
}

func asFailureOr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	return asFailure(err)
}

// AccountUpdate handles account_update webhooks. PARTNER_REMOVED disables the
// WABA's signup numbers and tells their sites; PARTNER_ADDED is recorded.
func (s *Service) AccountUpdate(ctx context.Context, wabaID, event string) error {
	if wabaID == "" {
		return nil
	}
	if event == "PARTNER_REMOVED" {
		if _, err := s.cfg.Store.DisableWABA(ctx, wabaID); err != nil {
			return err
		}
		if err := s.cfg.Reload(ctx); err != nil {
			return err
		}
	}
	if event != "PARTNER_REMOVED" && event != "PARTNER_ADDED" {
		return nil
	}
	var id string
	err := s.cfg.DB.QueryRow(ctx, `SELECT session_id FROM relay_signup_sessions WHERE waba_id=$1 ORDER BY created_at DESC LIMIT 1`, wabaID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.update(ctx, id, func(r *record) error {
		if event == "PARTNER_ADDED" {
			if r.metaEvent == nil {
				r.metaEvent = map[string]any{}
			}
			r.metaEvent["partner_added_at"], r.metaChanged = s.now(), true
			return nil
		}
		r.Error = &SessionError{Code: "partner_removed", Message: "Relay lost access to this WhatsApp account in Meta. Reconnect WhatsApp."}
		return nil
	})
	return err
}
