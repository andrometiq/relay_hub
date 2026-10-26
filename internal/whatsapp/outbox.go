package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/andrometiq/relay_hub/internal/deliverypolicy"
	"github.com/andrometiq/relay_hub/internal/lifecycle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Outbox retains every accepted intent. An interrupted provider request is never
// blindly retried: the outcome must be reconciled before creating a new intent.
type Outbox struct {
	pool    *pgxpool.Pool
	Workers int
	active  atomic.Int32
	pending atomic.Int64
	URL     string
	Service *Service
}
type Submission struct {
	Key     string `json:"key"`
	Message Input  `json:"message"`
}
type Delivery struct {
	DeliveryStatus string          `json:"delivery_status,omitempty"`
	ID             string          `json:"id"`
	Key            string          `json:"key"`
	State          string          `json:"state"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	ProviderError  json.RawMessage `json:"provider_error,omitempty"`
	Attempts       int             `json:"attempts"`
	NextAttempt    time.Time       `json:"next_attempt"`
}

// QueueLimit bounds queued and submitting intents per account.
const QueueLimit = 10000

// BulkQueueLimit bounds the bulk share of that queue, so conversational sends always have room.
const BulkQueueLimit = QueueLimit / 2

// queueRoom reports why one more intent cannot be admitted, or nil.
func queueRoom(count, bulkCount int, bulk bool) error {
	if count >= QueueLimit {
		return errors.New("account queue is full; nothing in this batch was accepted")
	}
	if bulk && bulkCount >= BulkQueueLimit {
		return errors.New("bulk queue is full; nothing in this batch was accepted")
	}
	return nil
}

func (o *Outbox) Enqueue(ctx context.Context, a Account, batch []Submission) ([]Delivery, error) {
	rows, _, err := o.enqueue(ctx, a, batch, false)
	return rows, err
}

// EnqueueBulk admits up to 50 template sends at once. A recipient that fails
// validation is returned as rejected without blocking the others; the queue
// depth after admission lets Core apply backpressure.
func (o *Outbox) EnqueueBulk(ctx context.Context, a Account, batch []Submission) ([]Delivery, int, error) {
	return o.enqueue(ctx, a, batch, true)
}

func (o *Outbox) enqueue(ctx context.Context, a Account, batch []Submission, bulk bool) ([]Delivery, int, error) {
	if len(batch) == 0 || len(batch) > 50 {
		return nil, 0, errors.New("submit between 1 and 50 messages")
	}
	seen := map[string]bool{}
	refused := map[string]string{}
	for _, item := range batch {
		if !identifier.MatchString(item.Key) || seen[item.Key] {
			return nil, 0, errors.New("unique request keys are required")
		}
		seen[item.Key] = true
		err := queueable(item.Message.Operation, bulk)
		if err == nil {
			_, err = o.Service.execute(ctx, a, item.Message, true)
		}
		if err != nil && !bulk {
			return nil, 0, err
		}
		var before *preSubmissionError
		if errors.As(err, &before) {
			err = before.error
		}
		if err != nil {
			refused[item.Key] = err.Error()
		}
	}
	c, release, err := o.connect(ctx)
	if err != nil {
		return nil, 0, errors.New("outbox unavailable")
	}
	defer release()
	tx, err := c.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(context.Background())
	// Serialize capacity accounting per account, across Hub processes.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", a.ID); err != nil {
		return nil, 0, err
	}
	var count, bulkCount int
	if err = tx.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE bulk) FROM relay_outbox WHERE account=$1 AND state IN ('queued','submitting')", a.ID).Scan(&count, &bulkCount); err != nil {
		return nil, 0, err
	}
	result := make([]Delivery, 0, len(batch))
	for _, item := range batch {
		if reason, ok := refused[item.Key]; ok {
			result = append(result, Delivery{Key: item.Key, State: "rejected", Error: reason})
			continue
		}
		raw, _ := json.Marshal(item.Message)
		var d Delivery
		var same bool
		err = tx.QueryRow(ctx, "SELECT id::text,request_key,state,payload=$3::jsonb FROM relay_outbox WHERE account=$1 AND request_key=$2", a.ID, item.Key, string(raw)).Scan(&d.ID, &d.Key, &d.State, &same)
		if err == nil {
			if !same {
				return nil, 0, errors.New("request key already used with another message")
			}
			result = append(result, d)
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, err
		}
		if err = queueRoom(count, bulkCount, bulk); err != nil {
			return nil, 0, err
		}
		err = tx.QueryRow(ctx, "INSERT INTO relay_outbox(account,request_key,payload,bulk) VALUES($1,$2,$3,$4) RETURNING id::text,request_key,state", a.ID, item.Key, string(raw), bulk).Scan(&d.ID, &d.Key, &d.State)
		if err != nil {
			return nil, 0, err
		}
		count++
		if bulk {
			bulkCount++
		}
		result = append(result, d)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, errors.New("submission outcome unknown; retry with the same request keys")
	}
	return result, count, nil
}

func queueable(operation string, bulk bool) error {
	if bulk && operation != "templates.send" {
		return errors.New("bulk sends accept approved templates only")
	}
	switch operation {
	case "templates.send", "groups.send", "groups.media", "groups.template", "groups.pin", "messages.send", "messages.react", "messages.media", "messages.location", "messages.contacts", "messages.flow":
		return nil
	}
	return errors.New("operation cannot be queued")
}
func (o *Outbox) List(ctx context.Context, a Account, after string) ([]Delivery, error) {
	cursor := int64(0)
	var err error
	if after != "" {
		cursor, err = strconv.ParseInt(after, 10, 64)
		if err != nil || cursor < 0 {
			return nil, errors.New("invalid cursor")
		}
	}
	c, release, err := o.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := c.Query(ctx, `WITH page AS (SELECT * FROM relay_outbox WHERE account=$1 AND id>$2 ORDER BY id LIMIT 50), sized AS (SELECT *,sum(octet_length(coalesce(result::text,''))) OVER (ORDER BY id) bytes FROM page) SELECT o.id::text,o.request_key,o.state,o.result,o.error,coalesce(s.status,''),o.provider_error,o.attempts,o.next_attempt FROM sized o LEFT JOIN relay_whatsapp_status s ON s.account=o.account AND s.provider_id=o.result->'messages'->0->>'id' WHERE o.bytes<=524288 ORDER BY o.id`, a.ID, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err = rows.Scan(&d.ID, &d.Key, &d.State, &d.Result, &d.Error, &d.DeliveryStatus, &d.ProviderError, &d.Attempts, &d.NextAttempt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (o *Outbox) Cancel(ctx context.Context, a Account, id string) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 {
		return errors.New("invalid delivery ID")
	}
	c, release, err := o.connect(ctx)
	if err != nil {
		return err
	}
	defer release()
	tag, err := c.Exec(ctx, "UPDATE relay_outbox SET state='cancelled',updated_at=now() WHERE account=$1 AND id=$2 AND state='queued'", a.ID, n)
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("delivery is not queued or does not belong to this account")
	}
	return err
}

// Run uses a bounded worker count and PostgreSQL row claiming, allowing several
// processes to share the queue without sending the same accepted intent twice.
func (o *Outbox) Run(ctx context.Context) {
	maximum := o.Workers
	if maximum < 1 {
		maximum = 4
	}
	o.active.Store(1)
	lifecycle.Go(ctx, func() {
		timer := time.NewTicker(500 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				countCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				c, release, err := o.connect(countCtx)
				if err == nil {
					var count int64
					if c.QueryRow(countCtx, "SELECT count(*) FROM relay_outbox WHERE state='queued'").Scan(&count) == nil {
						o.pending.Store(count)
						o.active.Store(int32(workerTarget(count, maximum)))
					}
					release()
				}
				cancel()
			}
		}
	})
	for worker := 0; worker < maximum; worker++ {
		lifecycle.Go(ctx, func() {
			index := worker

			for {
				delay := 10 * time.Millisecond
				if o.pending.Load() == 0 || index >= int(o.active.Load()) {
					delay = 500 * time.Millisecond
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if index >= int(o.active.Load()) {
					continue
				}
				jobCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				sent, _ := o.send(jobCtx)
				cancel()
				// Continue at once after a send; pacing is enforced by the claim.
				for sent && ctx.Err() == nil {
					jobCtx, cancel = context.WithTimeout(ctx, 20*time.Second)
					sent, _ = o.send(jobCtx)
					cancel()
				}
			}
		})
	}
}
func workerTarget(pending int64, maximum int) int { return min(maximum, max(1, int((pending+24)/25))) }
func NewOutbox(ctx context.Context, url string, service *Service, workers int) (*Outbox, error) {
	if workers < 1 || workers > 8 {
		return nil, errors.New("outbox workers must be between 1 and 8")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid outbox database configuration")
	}
	config.MaxConns = int32(workers + 2)
	config.MinConns = 0
	config.MaxConnIdleTime = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	// Leave two provider slots for interactive operations while bulk work runs.
	service.slots = make(chan struct{}, workers+2)
	return &Outbox{URL: url, Service: service, Workers: workers, pool: pool}, nil
}
func (o *Outbox) Close() {
	if o.pool != nil {
		o.pool.Close()
	}
}
func (o *Outbox) connect(ctx context.Context) (*pgx.Conn, func(), error) {
	if o.pool != nil {
		conn, err := o.pool.Acquire(ctx)
		if err != nil {
			return nil, nil, err
		}
		return conn.Conn(), conn.Release, nil
	}
	conn, err := pgx.Connect(ctx, o.URL)
	if err != nil {
		return nil, nil, err
	}
	return conn, func() { conn.Close(context.Background()) }, nil
}
func (o *Outbox) process(ctx context.Context) error {
	_, err := o.send(ctx)
	return err
}

// send claims and submits at most one delivery and reports whether it did.
func (o *Outbox) send(ctx context.Context) (bool, error) {
	c, release, err := o.connect(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	// Only claims that never reached message submission are safe to requeue.
	_, err = c.Exec(ctx, "UPDATE relay_outbox SET state=CASE WHEN submission_started THEN 'uncertain' ELSE 'queued' END,error=CASE WHEN submission_started THEN 'Worker interrupted; reconcile provider outcome' ELSE 'Worker interrupted before submission' END,updated_at=now() WHERE state='submitting' AND updated_at<now()-interval '2 minutes'")
	if err != nil {
		return false, err
	}
	var id int64
	var account string
	var raw []byte
	var attempts int
	var wait time.Duration
	tx, err := c.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	// Serialize only the short claim, so concurrent snapshots cannot claim two
	// queued rows in the same conversation before either update is visible.
	// The earlier-row check is a scalar subquery, not NOT EXISTS: with stale
	// statistics Postgres planned NOT EXISTS as an anti join that scanned every
	// pending row per candidate, which stalled sends at campaign start and resume.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351021)"); err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `UPDATE relay_outbox SET state='submitting',submission_started=false,updated_at=now()
 WHERE id=(SELECT o.id FROM relay_outbox o LEFT JOIN relay_account_send_rate r ON r.account=o.account
 LEFT JOIN relay_outbox_account_claim turn ON turn.account=o.account
 WHERE o.state='queued' AND o.next_attempt<=now() AND (r.next_at IS NULL OR r.next_at<=clock_timestamp()+$1::interval)
 AND (SELECT 1 FROM relay_outbox earlier WHERE earlier.account=o.account
 AND earlier.payload->>'to'=o.payload->>'to' AND earlier.id<o.id AND earlier.state IN ('queued','submitting','uncertain') LIMIT 1) IS NULL
 ORDER BY coalesce(turn.claimed_at,'epoch'::timestamptz),o.bulk,o.id FOR UPDATE OF o SKIP LOCKED LIMIT 1)
 RETURNING id,account,payload,attempts`, slotAhead.String()).Scan(&id, &account, &raw, &attempts)
	var a Account
	found := false
	if err == nil {
		for _, candidate := range o.Service.AccountsSnapshot() {
			if candidate.ID == account {
				a, found = candidate, true
				break
			}
		}
		// Reserve the account's next send slot with the claim, so concurrent
		// workers never claim a row they must then defer for pacing.
		permitted := true
		if found {
			permitted, wait, err = reserve(ctx, tx, a)
		}
		if err == nil && !permitted {
			return false, nil
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO relay_outbox_account_claim(account,claimed_at) VALUES($1,clock_timestamp()) ON CONFLICT(account) DO UPDATE SET claimed_at=excluded.claimed_at`, account)
		if err == nil {
			err = tx.Commit(ctx)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var in Input
	err = json.Unmarshal(raw, &in)
	in.tracking = strconv.FormatInt(id, 10)
	in.beforeSubmit = func(ctx context.Context) error {
		tag, err := c.Exec(ctx, "UPDATE relay_outbox SET submission_started=true,updated_at=now() WHERE id=$1 AND state='submitting'", id)
		if err == nil && tag.RowsAffected() != 1 {
			return errors.New("delivery already reconciled")
		}
		return err
	}
	var result map[string]any
	if err == nil && found {
		select {
		case <-time.After(wait):
			result, err = o.Service.Execute(ctx, a, in)
		case <-ctx.Done():
			err = &deferredError{ctx.Err()}
		}
	} else {
		err = &deferredError{errors.New("account configuration unavailable")}
	}
	state, message, delay, metadata := sendOutcome(err, attempts)
	var encoded []byte
	if err == nil {
		encoded, _ = json.Marshal(result)
		if len(encoded) > 65536 {
			summary := map[string]any{"accepted": true, "details_omitted": true}
			if messages, ok := result["messages"].([]any); ok && len(messages) > 0 {
				if first, ok := messages[0].(map[string]any); ok {
					if providerID, ok := first["id"].(string); ok && len(providerID) <= 300 {
						summary["messages"] = []map[string]string{{"id": providerID}}
					}
				}
			}
			encoded, _ = json.Marshal(summary)
		}
	}

	// Save even if shutdown cancelled the provider request's context.
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.Exec(saveCtx, "UPDATE relay_outbox SET state=$2,result=$3,error=$4,next_attempt=now()+$5::interval,provider_error=$6,attempts=attempts+$7,updated_at=now() WHERE id=$1 AND state='submitting'", id, state, encoded, message, delay.String(), metadata, retryAttempt(err))
	return true, err
}

func retryAttempt(err error) int {
	if errors.Is(err, ErrBusy) {
		return 0
	}
	return 1
}

func sendOutcome(err error, attempts int) (string, string, time.Duration, []byte) {
	if err == nil {
		return "submitted", "", 0, nil
	}
	if errors.Is(err, ErrBusy) {
		return "queued", "Account pacing; deferred before submission", time.Second, nil
	}
	var provider *ProviderError
	var before *preSubmissionError
	var deferred *deferredError
	var metadata []byte
	retry := errors.As(err, &deferred)
	if errors.As(err, &provider) {
		metadata, _ = json.Marshal(provider)
		retry = retry || provider.Retryable()
	}
	if retry {
		if attempts >= deliverypolicy.MaxAttempts-1 {
			return "rejected", "Safe retry limit exhausted; message was not accepted", 0, metadata
		}
		delay := deliverypolicy.RetryDelay(attempts, 0)
		if provider != nil {
			delay = deliverypolicy.RetryDelay(attempts, provider.RetryAfter)
		}
		return "queued", "Not accepted; retry scheduled", delay, metadata
	}
	if errors.As(err, &before) || (provider != nil && provider.Status < 500 && provider.Status != 408) {
		detail := ""
		if provider != nil {
			detail = provider.Detail()
		}
		return "rejected", "Message rejected before acceptance" + detail, 0, metadata
	}
	return "uncertain", "Provider outcome not confirmed; reconcile before resubmitting", 0, metadata
}

// SendRate is the account's provider pacing; 50 messages per second by default.
func (a Account) SendRate() int {
	if a.MessagesPerSecond == 0 {
		return 50
	}
	return a.MessagesPerSecond
}

// This shared PostgreSQL gate enforces account pacing across Hub replicas.
func permit(ctx context.Context, c *pgx.Conn, a Account) (bool, error) {
	rate := a.SendRate()
	var next time.Time
	err := c.QueryRow(ctx, `INSERT INTO relay_account_send_rate(account,next_at) VALUES($1,clock_timestamp()+$2::double precision*interval '1 second') ON CONFLICT(account) DO UPDATE SET next_at=clock_timestamp()+$2::double precision*interval '1 second' WHERE relay_account_send_rate.next_at<=clock_timestamp() RETURNING next_at`, a.ID, 1.0/float64(rate)).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// slotAhead is how far ahead a worker may reserve an account send slot.
const slotAhead = 50 * time.Millisecond

// reserve books the account's next send slot when it starts within slotAhead
// and returns how long to wait for it.
func reserve(ctx context.Context, tx pgx.Tx, a Account) (bool, time.Duration, error) {
	var seconds float64
	err := tx.QueryRow(ctx, `INSERT INTO relay_account_send_rate(account,next_at) VALUES($1,clock_timestamp()+$2::double precision*interval '1 second')
 ON CONFLICT(account) DO UPDATE SET next_at=greatest(relay_account_send_rate.next_at,clock_timestamp())+$2::double precision*interval '1 second'
 WHERE relay_account_send_rate.next_at<=clock_timestamp()+$3::interval
 RETURNING greatest(0,extract(epoch FROM next_at-$2::double precision*interval '1 second'-clock_timestamp()))::double precision`, a.ID, 1.0/float64(a.SendRate()), slotAhead.String()).Scan(&seconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, nil
	}
	return err == nil, time.Duration(seconds * float64(time.Second)), err
}

func (o *Outbox) Permit(ctx context.Context, a Account) (bool, error) {
	c, release, err := o.connect(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return permit(ctx, c, a)
}

// Lookup reads one stable intent without replaying a provider request.
func (o *Outbox) Lookup(ctx context.Context, a Account, key string) (Delivery, error) {
	var d Delivery
	if !identifier.MatchString(key) {
		return d, errors.New("invalid request key")
	}
	c, release, err := o.connect(ctx)
	if err != nil {
		return d, err
	}
	defer release()
	err = c.QueryRow(ctx, "SELECT id::text,request_key,state,result,error,provider_error,attempts,next_attempt FROM relay_outbox WHERE account=$1 AND request_key=$2", a.ID, key).Scan(&d.ID, &d.Key, &d.State, &d.Result, &d.Error, &d.ProviderError, &d.Attempts, &d.NextAttempt)
	return d, err
}

// Submit waits briefly for normal delivery; a slow or interrupted operation
// remains queryable by key, with no second provider submission.
func (o *Outbox) Submit(ctx context.Context, a Account, in Input) (map[string]any, error) {
	key := in.RequestKey
	in.RequestKey = ""
	if _, err := o.Enqueue(ctx, a, []Submission{{Key: key, Message: in}}); err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		d, err := o.Lookup(ctx, a, key)
		if err != nil {
			return nil, err
		}
		if d.State == "submitted" {
			var result map[string]any
			if err := json.Unmarshal(d.Result, &result); err != nil {
				return nil, err
			}
			return result, nil
		}
		if d.State != "queued" && d.State != "submitting" {
			return map[string]any{"delivery": d}, nil
		}
		select {
		case <-wait.Done():
			return map[string]any{"delivery": d}, nil
		case <-timer.C:
		}
	}
}

// Resolve records verified evidence, never a retry. Accepted non-reaction
// messages require a provider ID so Core can project the confirmed message.
func (o *Outbox) Resolve(ctx context.Context, a Account, id int64, outcome, note, providerID string) error {
	note = strings.TrimSpace(note)
	providerID = strings.TrimSpace(providerID)
	if id < 1 || (outcome != "submitted" && outcome != "rejected") || len(note) < 10 || len(note) > 2000 || len(providerID) > 300 || strings.ContainsAny(providerID, "\r\n\t ") {
		return errors.New("invalid verified outcome or evidence")
	}
	c, release, err := o.connect(ctx)
	if err != nil {
		return err
	}
	defer release()
	result := map[string]any{"manually_reconciled": true}
	if providerID != "" {
		result["messages"] = []map[string]string{{"id": providerID}}
	}
	raw, _ := json.Marshal(result)
	tag, err := c.Exec(ctx, `UPDATE relay_outbox SET state=$3,resolution_note=$4,resolved_at=now(),updated_at=now(),error='',result=COALESCE(result,'{}'::jsonb)||$5::jsonb WHERE account=$1 AND id=$2 AND state='uncertain' AND ($3='rejected' OR $6<>'' OR payload->>'operation'='messages.react')`, a.ID, id, outcome, note, string(raw), providerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("only uncertain deliveries can be resolved; accepted messages require their provider ID")
	}
	return nil
}
