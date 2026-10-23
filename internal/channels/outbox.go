package channels

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	ID        int64     `json:"id"`
	Operation string    `json:"operation"`
	State     string    `json:"state"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
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
		err = tx.QueryRow(ctx, `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash) VALUES($1,$2,$3,$4,$5) RETURNING id`, account, key, operation, payload, hash).Scan(&id)
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
	var operation string
	var payload []byte
	err := o.DB.QueryRow(ctx, `UPDATE relay_channel_outbox SET state='dispatching',updated_at=now() WHERE id=$1 AND account=$2 AND state='queued' RETURNING operation,payload`, id, account).Scan(&operation, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return o.result(ctx, id, account)
	}
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if operation == "telegram.message" {
		var input telegram.MessageInput
		if err = json.Unmarshal(payload, &input); err == nil {
			result, err = o.Registry.TelegramMessage(ctx, account, input)
		}
	} else {
		var input EmailInput
		if err = json.Unmarshal(payload, &input); err == nil {
			input.RequestKey = fmt.Sprintf("delivery-%d", id)
			result, err = o.Registry.SendEmailMessage(ctx, account, input)
		}
	}
	state, detail := "submitted", ""
	if err != nil {
		state, detail = "uncertain", "Provider outcome is unknown. Reconcile before creating another delivery."
		var provider *telegram.APIError
		if errors.As(err, &provider) && !provider.OutcomeUnknown {
			state, detail = "rejected", "Provider rejected the delivery."
		}
	}
	raw, _ := json.Marshal(result)
	// Persist even if the caller went away. A failed final write remains dispatching
	// and recovery marks it uncertain rather than repeating an external send.
	writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, saveErr := o.DB.Exec(writeCtx, `UPDATE relay_channel_outbox SET state=$2,result=$3,error=$4,updated_at=now() WHERE id=$1 AND state='dispatching'`, id, state, raw, detail)
	if saveErr != nil {
		return nil, errors.New("delivery journal update failed; outcome requires reconciliation")
	}
	if err != nil {
		return nil, err
	}
	result["delivery_id"] = id
	return result, nil
}

func (o Outbox) result(ctx context.Context, id int64, account string) (map[string]any, error) {
	var state string
	var raw []byte
	if err := o.DB.QueryRow(ctx, `SELECT state,result FROM relay_channel_outbox WHERE id=$1 AND account=$2`, id, account).Scan(&state, &raw); err != nil {
		return nil, err
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

func (o Outbox) List(ctx context.Context, account string, after int64) ([]Delivery, error) {
	rows, err := o.DB.Query(ctx, `SELECT id,operation,state,error,created_at FROM relay_channel_outbox WHERE account=$1 AND id>$2 ORDER BY id LIMIT 51`, account, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Delivery{}
	for rows.Next() {
		var item Delivery
		if err = rows.Scan(&item.ID, &item.Operation, &item.State, &item.Error, &item.CreatedAt); err != nil {
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
	_, err := o.DB.Exec(ctx, `UPDATE relay_channel_outbox SET state='uncertain',error='Worker stopped during submission. Reconcile before retrying.',updated_at=now() WHERE state='dispatching' AND updated_at<now()-interval '2 minutes'`)
	return err
}
func (o Outbox) Run(ctx context.Context) {
	for range 2 {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					work, cancel := context.WithTimeout(ctx, 30*time.Second)
					o.Recover(work)
					var id int64
					var account string
					if err := o.DB.QueryRow(work, `SELECT id,account FROM relay_channel_outbox WHERE state='queued' ORDER BY id LIMIT 1`).Scan(&id, &account); err == nil {
						o.dispatch(work, id, account)
					}
					cancel()
				}
			}
		}()
	}
}

// Resolve records the operator's externally verified outcome without submitting
// another provider request. The original result remains available for auditing.
func (o Outbox) Resolve(ctx context.Context, account string, id int64, outcome, note string) error {
	if outcome != "submitted" && outcome != "rejected" {
		return errors.New("resolution must be submitted or rejected")
	}
	note = strings.TrimSpace(note)
	if len(note) < 10 || len(note) > 2000 {
		return errors.New("describe the verified provider outcome in 10–2000 characters")
	}
	tag, err := o.DB.Exec(ctx, `UPDATE relay_channel_outbox SET state=$3,resolution_note=$4,resolved_at=now(),updated_at=now(),error='',result=COALESCE(result,'{}'::jsonb)||'{"manually_reconciled":true}'::jsonb WHERE account=$1 AND id=$2 AND state='uncertain'`, account, id, outcome, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("only uncertain deliveries can be resolved")
	}
	return nil
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
