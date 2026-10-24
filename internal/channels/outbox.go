package channels

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/lifecycle"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andrometiq/relay_hub/internal/deliverypolicy"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EmailInput = relayemail.Message

type Outbox struct {
	DB       *pgxpool.Pool
	Registry *Registry
}
type Delivery struct {
	ID        int64          `json:"id"`
	Operation string         `json:"operation"`
	State     string         `json:"state"`
	Error     string         `json:"error"`
	CreatedAt time.Time      `json:"created_at"`
	Result    map[string]any `json:"result"`
	Attempts  int            `json:"attempts"`
}

var requestKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`)

// Submit journals before attempting delivery. Reusing a key never repeats a
// dispatched mutation, including after a lost HTTP response or process crash.
func (o Outbox) Submit(ctx context.Context, account, key, operation string, input any) (map[string]any, error) {
	if operation == "telegram.message" {
		message, ok := input.(telegram.MessageInput)
		if !ok {
			return nil, errors.New("invalid Telegram payload")
		}
		if err := message.Validate(); err != nil {
			return nil, err
		}
	}
	if operation == "email.send" {
		message, ok := input.(EmailInput)
		if !ok {
			return nil, errors.New("invalid email payload")
		}
		if err := message.Validate(); err != nil {
			return nil, err
		}
	}
	payload, err := json.Marshal(input)
	if message, ok := input.(telegram.MessageInput); ok && message.Operation == "reaction" && message.Reaction != nil {
		// An explicit empty list means clear reactions; omitempty must not erase it
		// while the durable payload crosses its serialization boundary.
		var stored map[string]json.RawMessage
		if err == nil {
			err = json.Unmarshal(payload, &stored)
		}
		if err == nil {
			stored["reaction"], err = json.Marshal(message.Reaction)
		}
		if err == nil {
			payload, err = json.Marshal(stored)
		}
	}

	if err != nil || len(payload) > 6<<20 {
		return nil, errors.New("invalid delivery payload")
	}
	if key == "" {
		var nonce [24]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		key = hex.EncodeToString(nonce[:])
	}
	if !requestKeyRE.MatchString(key) || account == "" {
		return nil, errors.New("invalid request key")
	}
	if operation != "telegram.message" && operation != "email.send" {
		return nil, errors.New("unsupported delivery operation")
	}
	digest := sha256.Sum256(append([]byte(operation+":"), payload...))
	hash := hex.EncodeToString(digest[:])
	tx, err := o.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Serialize admission within an account so concurrent requests cannot bypass its cap.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,731))`, account); err != nil {
		return nil, err
	}
	var id int64
	var existing string
	err = tx.QueryRow(ctx, `SELECT id,payload_hash FROM relay_channel_outbox WHERE account=$1 AND request_key=$2`, account, key).Scan(&id, &existing)
	if err == nil {
		if existing != hash {
			return nil, errors.New("request key already used for different content")
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		var pending int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM relay_channel_outbox WHERE account=$1 AND state IN ('queued','dispatching')`, account).Scan(&pending); err != nil {
			return nil, err
		}
		if pending >= 10000 {
			return nil, errors.New("account queue is full")
		}
		err = tx.QueryRow(ctx, `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash,destinations) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, account, key, operation, payload, hash, sendDestinations(input)).Scan(&id)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return o.dispatch(ctx, id, account)
}

func (o Outbox) dispatch(ctx context.Context, id int64, account string) (map[string]any, error) {
	claim, err := o.claim(ctx, id, account)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return o.result(ctx, id, account)
	}
	return o.send(ctx, *claim)
}

type channelClaim struct {
	ID                 int64
	Account, Operation string
	Payload            []byte
	Attempts           int
	Version            int64
}

// All admission paths use the same fair scheduler. A caller cannot jump ahead
// of another account or an earlier unresolved delivery to the same destination.
func (o Outbox) claim(ctx context.Context, id int64, account string) (*channelClaim, error) {
	tx, err := o.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(726351022)`); err != nil {
		return nil, err
	}
	var c channelClaim
	err = tx.QueryRow(ctx, `SELECT o.id,o.account,o.operation,o.payload,o.attempts,o.claim_version FROM relay_channel_outbox o
 LEFT JOIN relay_channel_outbox_account_claim turn ON turn.account=o.account
 WHERE o.state='queued' AND o.next_attempt<=now()
 AND NOT EXISTS(SELECT 1 FROM relay_channel_outbox earlier WHERE earlier.account=o.account
 AND (earlier.destinations && o.destinations OR '*'=ANY(earlier.destinations) OR '*'=ANY(o.destinations)) AND earlier.id<o.id AND earlier.state IN ('queued','dispatching','uncertain'))
 ORDER BY coalesce(turn.claimed_at,'epoch'::timestamptz),o.id FOR UPDATE OF o SKIP LOCKED LIMIT 1`).Scan(&c.ID, &c.Account, &c.Operation, &c.Payload, &c.Attempts, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if id != 0 && (c.ID != id || c.Account != account) {
		return nil, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_channel_outbox SET state='dispatching',submission_started=false,claim_version=claim_version+1,updated_at=now() WHERE id=$1`, c.ID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_channel_outbox_account_claim(account,claimed_at) VALUES($1,clock_timestamp()) ON CONFLICT(account) DO UPDATE SET claimed_at=excluded.claimed_at`, c.Account); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	c.Version++
	return &c, nil
}

func (o Outbox) send(ctx context.Context, claim channelClaim) (map[string]any, error) {
	id, account, operation, payload := claim.ID, claim.Account, claim.Operation, claim.Payload
	// Fence stale workers at the durable submission boundary. Preparation and
	// SMTP envelope exchange cannot submit a message; the adapters mark before
	// the Telegram request or SMTP DATA command can reach the provider.
	var messageID string
	var destinations []string
	mark := func() error {
		tag, err := o.DB.Exec(ctx, `UPDATE relay_channel_outbox SET submission_started=true,updated_at=now(),
  result=CASE WHEN $3='' THEN result ELSE jsonb_build_object('message_id',$3::text) END,
  destinations=coalesce($4::text[],destinations)
  WHERE id=$1 AND state='dispatching' AND NOT submission_started AND claim_version=$2`, id, claim.Version, messageID, destinations)
		if err == nil && tag.RowsAffected() != 1 {
			return errors.New("delivery claim is no longer active")
		}
		return err
	}
	var err error
	before := true
	var markErr error
	ctx = deliverypolicy.WithSubmission(ctx, func() error {
		markErr = mark()
		if markErr == nil {
			before = false
		}
		return markErr
	})

	var result map[string]any
	if operation == "telegram.message" {
		var input telegram.MessageInput
		if err = json.Unmarshal(payload, &input); err == nil {
			err = input.Validate()
			if err == nil && (o.Registry == nil || o.Registry.snapshot().telegram[account] == nil) {
				err = errors.New("Telegram account is not configured")
			}
			if err == nil {
				result, err = o.Registry.TelegramMessage(ctx, account, input)
			}
		}
	} else {
		var input EmailInput
		if err = json.Unmarshal(payload, &input); err == nil {
			input.RequestKey = fmt.Sprintf("delivery-%d", id)
			err = input.Validate()
			var client *relayemail.Client
			if o.Registry != nil {
				client = o.Registry.snapshot().email[account]
			}
			if err == nil && client == nil {
				err = errors.New("email account is not configured")
			}
			if err == nil {
				messageID = client.MessageID(input.RequestKey)
				destinations = sendDestinations(input)
				result, err = sendEmailMessage(ctx, account, input, client)
			}
		}
	}
	if markErr != nil {
		return nil, markErr
	}
	state, detail, delay, metadata := channelSendOutcome(err, claim.Attempts, before)

	if result == nil && messageID != "" {
		result = map[string]any{"message_id": messageID}
	}
	raw, _ := json.Marshal(result)
	// Persist even if the caller went away. A failed final write remains dispatching
	// and recovery marks it uncertain rather than repeating an external send.
	writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tag, saveErr := o.DB.Exec(writeCtx, `UPDATE relay_channel_outbox SET state=$2,result=$3,error=$4,next_attempt=now()+$5::interval,provider_error=$6,attempts=attempts+1,updated_at=now() WHERE id=$1 AND state='dispatching' AND claim_version=$7`, id, state, raw, detail, delay.String(), metadata, claim.Version)
	if saveErr != nil || tag.RowsAffected() != 1 {
		return nil, errors.New("delivery journal update failed; outcome requires reconciliation")
	}
	if state == "queued" {
		return o.result(writeCtx, id, account)
	}
	if err != nil {
		return nil, err
	}
	result["delivery_id"] = id
	return result, nil
}

func (o Outbox) result(ctx context.Context, id int64, account string) (map[string]any, error) {
	var state, key string
	var raw []byte
	var nextAttempt time.Time
	if err := o.DB.QueryRow(ctx, `SELECT state,result,request_key,next_attempt FROM relay_channel_outbox WHERE id=$1 AND account=$2`, id, account).Scan(&state, &raw, &key, &nextAttempt); err != nil {
		return nil, err
	}
	if state == "queued" || state == "dispatching" {
		result := map[string]any{"delivery_id": id, "request_key": key, "state": state, "pending": true}
		if state == "queued" {
			result["next_attempt"] = nextAttempt
		}
		return result, nil
	}
	if state != "submitted" {
		return nil, errors.New("delivery is " + state + "; inspect its journal before retrying")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, errors.New("invalid delivery result")
	}
	result["delivery_id"] = id
	return result, nil
}

func (o Outbox) List(ctx context.Context, account string, after int64, channel ...string) ([]Delivery, error) {
	operation := ""
	if len(channel) > 0 {
		switch channel[0] {
		case "Telegram":
			operation = "telegram.message"
		case "Email":
			operation = "email.send"
		default:
			return nil, errors.New("invalid channel")
		}
	}
	rows, err := o.DB.Query(ctx, `SELECT id,operation,state,error,created_at,result,attempts FROM relay_channel_outbox WHERE account=$1 AND id>$2 AND ($3='' OR operation=$3) ORDER BY id LIMIT 51`, account, after, operation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Delivery{}
	for rows.Next() {
		var item Delivery
		if err = rows.Scan(&item.ID, &item.Operation, &item.State, &item.Error, &item.CreatedAt, &item.Result, &item.Attempts); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (o Outbox) Cancel(ctx context.Context, account string, id int64) error {
	tag, err := o.DB.Exec(ctx, `UPDATE relay_channel_outbox SET state='cancelled',updated_at=now() WHERE account=$1 AND id=$2 AND state='queued'`, account, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("only queued deliveries can be cancelled")
	}
	return nil
}
func (o Outbox) Recover(ctx context.Context) error {
	_, err := o.DB.Exec(ctx, `UPDATE relay_channel_outbox SET state=CASE WHEN submission_started THEN 'uncertain' ELSE 'queued' END,error=CASE WHEN submission_started THEN 'Worker stopped during submission. Reconcile before retrying.' ELSE 'Worker stopped before submission.' END,updated_at=now() WHERE state='dispatching' AND updated_at<now()-interval '2 minutes'`)
	return err
}
func (o Outbox) Run(ctx context.Context) {
	for range 2 {
		lifecycle.Go(ctx, func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					work, cancel := context.WithTimeout(ctx, 30*time.Second)
					o.Recover(work)
					if claim, err := o.claim(work, 0, ""); err == nil && claim != nil {
						_, _ = o.send(work, *claim)
					}
					cancel()
				}
			}
		})
	}
}

// Resolve records the operator's externally verified outcome without submitting
// another provider request. The original result remains available for auditing.
func (o Outbox) Resolve(ctx context.Context, account string, id int64, outcome, note string, evidence ...Resolution) error {
	if outcome != "submitted" && outcome != "rejected" {
		return errors.New("resolution must be submitted or rejected")
	}
	note = strings.TrimSpace(note)
	if len(note) < 10 || len(note) > 2000 {
		return errors.New("describe the verified provider outcome in 10–2000 characters")
	}
	tx, err := o.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var operation string
	var payload []byte
	if err = tx.QueryRow(ctx, `SELECT operation,payload FROM relay_channel_outbox WHERE account=$1 AND id=$2 AND state='uncertain' FOR UPDATE`, account, id).Scan(&operation, &payload); err != nil {
		return err
	}
	receipt := map[string]any{"manually_reconciled": true}
	if outcome == "submitted" {
		if len(evidence) != 1 {
			return errors.New("verified provider identifiers required")
		}
		e := evidence[0]
		if operation == "telegram.message" {
			var input telegram.MessageInput
			if json.Unmarshal(payload, &input) != nil {
				return errors.New("invalid stored delivery")
			}
			messageID, parseErr := strconv.ParseInt(e.ProviderID, 10, 64)
			if parseErr != nil || messageID < 1 || messageID >= 1<<52 || e.ChatID != input.ChatID || e.ChatID == 0 {
				return errors.New("verified Telegram chat and message identifiers required")
			}
			receipt["message_id"], receipt["chat_id"], receipt["operation"] = messageID, e.ChatID, input.Operation
			if input.Operation == "reaction" {
				if messageID != input.MessageID {
					return errors.New("reaction receipt must identify the original message")
				}
				receipt["result"] = true
			}
		} else {
			if !relayemail.ValidMessageID(e.ProviderID) {
				return errors.New("verified email Message-ID required")
			}
			receipt["message_id"], receipt["sent"] = e.ProviderID, true
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_channel_outbox SET state=$3,resolution_note=$4,resolved_at=now(),updated_at=now(),error='',result=COALESCE(NULLIF(result,'null'::jsonb),'{}'::jsonb)||$5::jsonb WHERE account=$1 AND id=$2 AND state='uncertain'`, account, id, outcome, note, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("only uncertain deliveries can be resolved")
	}
	return tx.Commit(ctx)
}

// Lookup reads a single account-scoped receipt without dispatching the request.
func (o Outbox) Lookup(ctx context.Context, account, key string) (map[string]any, error) {
	if account == "" || !requestKeyRE.MatchString(key) {
		return nil, errors.New("invalid delivery key")
	}
	var id int64
	var operation, state string
	var raw []byte
	err := o.DB.QueryRow(ctx, `SELECT id,operation,state,result FROM relay_channel_outbox WHERE account=$1 AND request_key=$2`, account, key).Scan(&id, &operation, &state, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &result) != nil {
			return nil, errors.New("invalid stored delivery receipt")
		}
	}
	return map[string]any{"id": id, "key": key, "operation": operation, "state": state, "result": result}, nil
}

// Resolution contains externally verified evidence; it never requests a send.
type Resolution struct {
	ProviderID string
	ChatID     int64
}

func channelSendOutcome(err error, attempts int, before bool) (string, string, time.Duration, []byte) {
	if err == nil {
		return "submitted", "", 0, nil
	}
	retry, known := false, before
	var metadata []byte
	var tg *telegram.APIError
	var smtp *relayemail.SendError
	if errors.As(err, &tg) {
		metadata, _ = json.Marshal(tg)
		known = !tg.OutcomeUnknown
		retry = known && (tg.StatusCode == 429 || tg.ErrorCode == 429 || tg.Retryable)
	}
	if errors.As(err, &smtp) {
		metadata, _ = json.Marshal(smtp)
		known, retry = !smtp.OutcomeUnknown, smtp.Retryable
	}
	if retry && attempts < deliverypolicy.MaxAttempts-1 {
		after := 0
		if tg != nil {
			after = tg.RetryAfter
		}
		return "queued", "Not accepted; retry scheduled", deliverypolicy.RetryDelay(attempts, after), metadata
	}
	if known {
		return "rejected", "Provider did not accept delivery", 0, metadata
	}
	return "uncertain", "Provider outcome is unknown. Reconcile before creating another delivery.", 0, metadata
}

// Inputs have already passed Validate. Canonicalize addresses with the same
// parser as SMTP, including display names, quoted local parts and comments.
func sendDestinations(input any) []string {
	switch in := input.(type) {
	case telegram.MessageInput:
		return []string{"telegram:" + strconv.FormatInt(in.ChatID, 10)}
	case EmailInput:
		destinations := []string{}
		for _, list := range [][]string{in.To, in.Cc, in.Bcc} {
			for _, value := range list {
				address, err := mail.ParseAddress(value)
				if err != nil {
					return []string{"*"}
				}
				destinations = append(destinations, "email:"+strings.ToLower(address.Address))
			}
		}
		slices.Sort(destinations)
		return slices.Compact(destinations)
	}
	return []string{"*"}
}
