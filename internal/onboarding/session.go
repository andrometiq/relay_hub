package onboarding

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Step is one signup step as Core and the Hub window show it.
type Step struct {
	Name   string     `json:"name"`
	Status string     `json:"status"`
	At     *time.Time `json:"at"`
}

// SessionError is the safe failure of a session.
type SessionError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	MetaCode  int    `json:"meta_code,omitempty"`
	MetaStep  string `json:"meta_step,omitempty"`
}

// Session is the public state of one signup. It holds no secrets.
type Session struct {
	SessionID          string         `json:"session_id"`
	Flow               string         `json:"flow"`
	FlowActual         *string        `json:"flow_actual"`
	State              string         `json:"state"`
	Steps              []Step         `json:"steps"`
	Error              *SessionError  `json:"error"`
	ChannelKey         *string        `json:"channel_key"`
	WABAID             *string        `json:"waba_id"`
	BusinessID         *string        `json:"business_id"`
	Phone              map[string]any `json:"phone"`
	PaymentMethodReady *bool          `json:"payment_method_ready"`
	Coexistence        map[string]any `json:"coexistence"`
	ExpiresAt          time.Time      `json:"expires_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	CreatedAt          time.Time      `json:"created_at"`
}

// record is a session row with its private fields.
type record struct {
	Session
	site, container, launchHash, actor string
	codeHash, phoneID                  *string
	prefill, metaEvent                 map[string]any
	tokenCipher, tokenNonce            []byte
	attempts                           int
	nextAttemptAt, lease               *time.Time
	// storedLease is lease_until as last read or written; writes require it.
	storedLease    *time.Time
	codeReceivedAt *time.Time
	// metaChanged writes meta_event; other writers keep the stored value,
	// because the Hub window may add a hint while a worker runs.
	metaChanged bool
}

var stepNames = []string{"exchange", "verify", "channel", "subscribe", "register", "sync_contacts", "sync_history"}

var flows = []string{"new", "coexistence", "migrate"}

// open states accept a code from the Hub window.
func (r *record) acceptsCode() bool {
	return r.State == "created" || r.State == "waiting_for_meta" || r.State == "cancelled" || (r.State == "failed" && !r.hasToken() && r.ChannelKey == nil)
}

func (r *record) hasToken() bool { return len(r.tokenCipher) > 0 }

func (r *record) coexistence() bool { return r.FlowActual != nil && *r.FlowActual == "coexistence" }

func (r *record) step(name string) *Step {
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			return &r.Steps[i]
		}
	}
	return nil
}

func (r *record) mark(name, status string, at time.Time) {
	if step := r.step(name); step != nil {
		step.Status, step.At = status, &at
	}
}

// nextStep is the first step that is not done or skipped.
func (r *record) nextStep() string {
	for _, step := range r.Steps {
		if step.Status != "done" && step.Status != "skipped" {
			return step.Name
		}
	}
	return ""
}

// currentStep names the step Core shows: the running or failed one, else the next one.
func (r *record) currentStep() string {
	for _, step := range r.Steps {
		if step.Status == "running" || step.Status == "failed" {
			return step.Name
		}
	}
	return r.nextStep()
}

func freshSteps() []Step {
	steps := make([]Step, len(stepNames))
	for i, name := range stepNames {
		steps[i] = Step{Name: name, Status: "pending"}
	}
	return steps
}

const sessionColumns = `session_id,site_id,container_id,flow,flow_actual,state,steps,launch_hash,code_hash,prefill,meta_event,waba_id,phone_id,business_id,
 channel_key,phone,token_ciphertext,token_nonce,error,coexistence,payment_method_ready,actor,attempts,next_attempt_at,lease_until,code_received_at,
 created_at,updated_at,expires_at`

func scanRecord(row pgx.Row) (record, error) {
	var r record
	var steps, prefill, meta, phone, failure, coexistence []byte
	err := row.Scan(&r.SessionID, &r.site, &r.container, &r.Flow, &r.FlowActual, &r.State, &steps, &r.launchHash, &r.codeHash, &prefill, &meta,
		&r.WABAID, &r.phoneID, &r.BusinessID, &r.ChannelKey, &phone, &r.tokenCipher, &r.tokenNonce, &failure, &coexistence, &r.PaymentMethodReady,
		&r.actor, &r.attempts, &r.nextAttemptAt, &r.lease, &r.codeReceivedAt, &r.CreatedAt, &r.UpdatedAt, &r.ExpiresAt)
	if err != nil {
		return r, err
	}
	r.storedLease = r.lease
	for _, field := range []struct {
		raw    []byte
		target any
	}{{steps, &r.Steps}, {prefill, &r.prefill}, {meta, &r.metaEvent}, {phone, &r.Phone}, {failure, &r.Error}, {coexistence, &r.Coexistence}} {
		if len(field.raw) > 0 && json.Unmarshal(field.raw, field.target) != nil {
			return r, errors.New("invalid stored signup session")
		}
	}
	return r, nil
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadRecord(ctx context.Context, q querier, id string, lock bool) (record, error) {
	query := `SELECT ` + sessionColumns + ` FROM relay_signup_sessions WHERE session_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanRecord(q.QueryRow(ctx, query, id))
}

// write stores the mutable fields and journals relay_signup for the site.
// With a lease it updates only while the lease is still held.
func (s *Service) write(ctx context.Context, tx pgx.Tx, r *record, keepLease bool) error {
	now := s.now()
	encode := func(value any) []byte {
		raw, _ := json.Marshal(value)
		if string(raw) == "null" {
			return nil
		}
		return raw
	}
	if !keepLease {
		r.lease = nil
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_signup_sessions SET flow_actual=$2,state=$3,steps=$4,code_hash=$5,meta_event=CASE WHEN $25 THEN $6 ELSE meta_event END,waba_id=$7,phone_id=$8,
 business_id=$9,channel_key=$10,phone=$11,token_ciphertext=$12,token_nonce=$13,error=$14,coexistence=$15,payment_method_ready=$16,attempts=$17,
 next_attempt_at=$18,lease_until=$19,code_received_at=$20,expires_at=$21,launch_hash=$22,updated_at=$23
 WHERE session_id=$1 AND lease_until IS NOT DISTINCT FROM $24`,
		r.SessionID, r.FlowActual, r.State, encode(r.Steps), r.codeHash, encode(r.metaEvent), r.WABAID, r.phoneID, r.BusinessID, r.ChannelKey,
		encode(r.Phone), r.tokenCipher, r.tokenNonce, encode(r.Error), encode(r.Coexistence), r.PaymentMethodReady, r.attempts,
		r.nextAttemptAt, r.lease, r.codeReceivedAt, r.ExpiresAt, r.launchHash, now, r.storedLease, r.metaChanged)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errLeaseLost
	}
	r.UpdatedAt, r.storedLease, r.metaChanged = now, r.lease, false
	return journal(ctx, tx, r.site, r.SessionID, r.State, r.currentStep())
}

var errLeaseLost = errors.New("signup session changed by another worker")

// errUnchanged tells update that the change wrote nothing.
var errUnchanged = errors.New("signup session unchanged")

// journal queues one relay_signup hint for the site. Core reads the session after it.
func journal(ctx context.Context, tx pgx.Tx, site, id, state, step string) error {
	payload, _ := json.Marshal(map[string]string{"object": "relay_signup", "session_id": id, "state": state, "step": step})
	_, err := tx.Exec(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id)
 VALUES('', 'relay_signup:' || $1 || ':' || txid_current(), $2, 'realtime', $3) ON CONFLICT(account,digest) DO NOTHING`, id, payload, site)
	return err
}

// update loads a session under a row lock, applies change and stores it.
func (s *Service) update(ctx context.Context, id string, change func(*record) error) (record, error) {
	tx, err := s.cfg.DB.Begin(ctx)
	if err != nil {
		return record{}, err
	}
	defer tx.Rollback(context.Background())
	r, err := loadRecord(ctx, tx, id, true)
	if err != nil {
		return r, err
	}
	s.expire(&r)
	if err = change(&r); errors.Is(err, errUnchanged) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err = s.write(ctx, tx, &r, r.lease != nil && r.lease.After(s.now())); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// expire ends a session that waited too long for Meta.
func (s *Service) expire(r *record) {
	if (r.State == "created" || r.State == "waiting_for_meta") && !r.ExpiresAt.After(s.now()) {
		r.State = "expired"
	}
}

// siteRecord loads a session of one site; a foreign or unknown id is not found.
func (s *Service) siteRecord(ctx context.Context, site, id string) (record, error) {
	r, err := loadRecord(ctx, s.cfg.DB, id, false)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.site != site) {
		return record{}, errNotFound
	}
	if err == nil && r.State != "expired" {
		if s.expire(&r); r.State == "expired" {
			return s.update(ctx, id, func(*record) error { return nil })
		}
	}
	return r, err
}

var errNotFound = fail(404, "not_found", "This signup was not found.")

// launchRecord loads a session for the Hub window. A wrong or missing launch secret is not found.
func (s *Service) launchRecord(ctx context.Context, id, secret string) (record, error) {
	r, err := loadRecord(ctx, s.cfg.DB, id, false)
	if err != nil || secret == "" || subtle.ConstantTimeCompare([]byte(auth.Hash(secret)), []byte(r.launchHash)) != 1 {
		return record{}, errNotFound
	}
	return r, nil
}

// SiteSignups lists the latest sessions of a site for the Hub admin, without secrets.
func (s *Service) SiteSignups(ctx context.Context, site string) ([]Session, error) {
	rows, err := s.cfg.DB.Query(ctx, `SELECT `+sessionColumns+` FROM relay_signup_sessions WHERE site_id=$1 ORDER BY created_at DESC LIMIT 20`, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Session{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r.Session)
	}
	return result, rows.Err()
}

func validFlow(flow string) bool { return slices.Contains(flows, flow) }
