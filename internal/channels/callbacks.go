package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/andrometiq/relay_hub/internal/callbackworker"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type oversizedCallback struct{ ID int64 }

func (e *oversizedCallback) Error() string { return "channel event exceeds callback limit" }

type CallbackDispatcher struct {
	URL      string
	DB       interface{}
	Accounts []RuntimeCallback
	mu       sync.RWMutex
}

func (d *CallbackDispatcher) Run(ctx context.Context, callbacks []RuntimeCallback) {
	d.Update(callbacks)
	d.runStatusCallbacks(ctx)
	go callbackworker.Run(ctx, 250*time.Millisecond, 8, func() []callbackworker.Job {
		d.mu.RLock()
		current := append([]RuntimeCallback(nil), d.Accounts...)
		d.mu.RUnlock()
		sort.Slice(current, func(i, j int) bool { return current[i].Account < current[j].Account })
		jobs := make([]callbackworker.Job, 0, len(current)*2)
		for _, a := range current {
			for _, channel := range []string{"Telegram", "Email"} {
				jobs = append(jobs, callbackworker.Job{Key: a.Account + ":" + channel, Run: func(jobCtx context.Context) { d.deliverOne(jobCtx, a, channel) }})
			}
		}
		return jobs
	})
}

func (d *CallbackDispatcher) Update(callbacks []RuntimeCallback) {
	d.mu.Lock()
	d.Accounts = append([]RuntimeCallback(nil), callbacks...)
	d.mu.Unlock()
}
func (d *CallbackDispatcher) deliver(ctx context.Context, a RuntimeCallback) {
	for _, channel := range []string{"Telegram", "Email"} {
		d.deliverOne(ctx, a, channel)
	}
}
func (d *CallbackDispatcher) deliverOne(ctx context.Context, a RuntimeCallback, channel string) {
	c, err := pgx.Connect(ctx, d.URL)
	if err != nil {
		return
	}
	defer c.Close(context.Background())
	var locked bool
	if err = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,2))", a.Account+":"+channel).Scan(&locked); err != nil || !locked {
		return
	}
	{
		var cursor int64
		if _, err = c.Exec(ctx, `INSERT INTO relay_channel_callback_cursor(account,channel) VALUES($1,$2) ON CONFLICT DO NOTHING`, a.Account, channel); err != nil {
			return
		}
		if err = c.QueryRow(ctx, `SELECT last_id FROM relay_channel_callback_cursor WHERE account=$1 AND channel=$2 AND next_attempt<=now()`, a.Account, channel).Scan(&cursor); err != nil {
			return
		}
		last, sendErr := d.deliverChannel(ctx, c, a, channel, cursor)
		if sendErr == nil && last > cursor {
			_, _ = c.Exec(ctx, `UPDATE relay_channel_callback_cursor SET last_id=$3,failures=0,next_attempt=now() WHERE account=$1 AND channel=$2`, a.Account, channel, last)
		} else if sendErr != nil {
			var oversized *oversizedCallback
			if errors.As(sendErr, &oversized) {
				// Never advance without preserving a durable operator-visible failure.
				tx, beginErr := c.Begin(ctx)
				if beginErr != nil {
					return
				}
				defer tx.Rollback(context.Background())
				_, err = tx.Exec(ctx, `INSERT INTO relay_channel_callback_quarantine(event_id,account,channel,reason) VALUES($1,$2,$3,'payload_too_large') ON CONFLICT(event_id) DO NOTHING`, oversized.ID, a.Account, channel)
				if err != nil {
					return
				}
				_, err = tx.Exec(ctx, `UPDATE relay_channel_callback_cursor SET last_id=$3,failures=0,next_attempt=now() WHERE account=$1 AND channel=$2 AND last_id=$4`, a.Account, channel, oversized.ID, cursor)
				if err != nil {
					return
				}
				_ = tx.Commit(ctx)
				return
			}
			_, _ = c.Exec(ctx, `UPDATE relay_channel_callback_cursor SET failures=least(failures+1,100),next_attempt=now()+interval '5 seconds' WHERE account=$1 AND channel=$2`, a.Account, channel)
		}
	}
}
func (d *CallbackDispatcher) deliverChannel(ctx context.Context, c *pgx.Conn, a RuntimeCallback, channel string, after int64) (int64, error) {
	return d.deliverRange(ctx, c, a, channel, after, 0)
}
func (d *CallbackDispatcher) deliverRange(ctx context.Context, c *pgx.Conn, a RuntimeCallback, channel string, after, through int64) (int64, error) {
	// pgx streams rows, while SQL limits both count and aggregate source size.
	// Always include the first row so an oversized event is surfaced, not skipped.
	rows, err := c.Query(ctx, `WITH candidates AS (
 SELECT id,account,channel,direction,provider_id,sender,recipient,subject,body,occurred_at,payload,
 octet_length(payload::text)::bigint+octet_length(body)+octet_length(subject)+octet_length(sender)+octet_length(recipient)+octet_length(provider_id)+512 AS bytes
 FROM relay_channel_events WHERE account=$1 AND ($2='' OR channel=$2) AND id>$3 AND ($4::bigint=0 OR id<=$4) ORDER BY id LIMIT 50
 ), bounded AS (
 SELECT *,row_number() OVER (ORDER BY id) AS ordinal,sum(bytes) OVER (ORDER BY id) AS total_bytes FROM candidates
 ) SELECT id,account,channel,direction,provider_id,sender,recipient,subject,body,occurred_at,payload
 FROM bounded WHERE ordinal=1 OR total_bytes<=2093056 ORDER BY id`, a.Account, channel, after, through)
	if err != nil {
		return after, err
	}
	defer rows.Close()
	events := []map[string]any{}
	last := after
	size := len(a.Account) + 100
	for rows.Next() {
		var id int64
		var account, ch, direction, pid, sender, recipient, subject, body string
		var occurred time.Time
		var payload []byte
		if err = rows.Scan(&id, &account, &ch, &direction, &pid, &sender, &recipient, &subject, &body, &occurred, &payload); err != nil {
			return after, err
		}
		event := map[string]any{"id": id, "payload": map[string]any{"channel": ch, "direction": direction, "provider_id": pid, "sender": sender, "recipient": recipient, "subject": subject, "body": body, "occurred_at": occurred, "payload": json.RawMessage(payload)}}
		encoded, encodeErr := json.Marshal(event)
		if encodeErr != nil {
			return after, encodeErr
		}
		if size+len(encoded)+1 > (2<<20)-4096 {
			if len(events) == 0 {
				return after, &oversizedCallback{ID: id}
			}
			break
		}
		size += len(encoded) + 1
		events = append(events, event)
		last = id
	}
	if err = rows.Err(); err != nil || len(events) == 0 {
		return after, err
	}
	raw, _ := json.Marshal(map[string]any{"account": a.Account, "events": events})
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(a.CallbackSecret))
	mac.Write([]byte(fmtInt(ts) + "."))
	mac.Write(raw)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.Site, "/")+"/api/method/relay_core.inbound.receive", bytes.NewReader(raw))
	if err != nil {
		return after, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+a.APIKey+":"+a.APISecret)
	req.Header.Set("X-Relay-Timestamp", fmtInt(ts))
	req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return after, err
	}
	defer resp.Body.Close()
	if !acknowledged(resp, len(events)) {
		return after, errors.New("Core callback rejected")
	}
	return last, nil
}

type RuntimeCallback struct{ Account, Site, APIKey, APISecret, CallbackSecret string }

// Deliver sends one bounded Telegram/email event batch to Core. The caller
// owns scheduling and durable cursor advancement; this method is idempotent
// because Core's Relay Hub Event key is unique.
func (d *CallbackDispatcher) Deliver(ctx context.Context, db *pgx.Conn, a RuntimeCallback, after int64) (int64, error) {
	return d.deliverChannel(ctx, db, a, "", after)
}

func stringify(v int64) string { return fmtInt(v) }
func fmtInt(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	b := make([]byte, 0, 20)
	for v > 0 {
		b = append([]byte{digits[v%10]}, b...)
		v /= 10
	}
	return string(b)
}

// A successful HTTP response alone is not a durable Core acknowledgement.
func acknowledged(resp *http.Response, count int) bool {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	var reply struct {
		Message struct {
			Accepted int `json:"accepted"`
		} `json:"message"`
	}
	return resp.StatusCode == http.StatusOK && err == nil && len(raw) <= 65536 && json.Unmarshal(raw, &reply) == nil && reply.Message.Accepted == count
}
