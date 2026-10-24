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
	"net/http"
	"sort"
	"strconv"
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
	Wake     <-chan struct{}
	mu       sync.RWMutex
}

func (d *CallbackDispatcher) Run(ctx context.Context, callbacks []RuntimeCallback) {
	d.Update(callbacks)
	d.runStatusCallbacks(ctx)
	go callbackworker.RunWithWake(ctx, 250*time.Millisecond, 8, func() []callbackworker.Job {
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
	}, d.Wake)
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
	_, _ = d.deliverPending(ctx, c, a, channel)
}

func (d *CallbackDispatcher) deliverPending(ctx context.Context, c *pgx.Conn, a RuntimeCallback, channel string) (int64, error) {
	// Retry failed batches one event at a time. Fresh events stay batched, and
	// backed-off failures never prevent later committed messages from progressing.
	var id int64
	var failures int
	if err := c.QueryRow(ctx, `SELECT e.id,d.failures FROM relay_channel_events e
 JOIN relay_channel_callback_delivery d ON d.event_id=e.id
 WHERE e.account=$1 AND ($2='' OR e.channel=$2) AND NOT d.acknowledged AND NOT d.quarantined
 AND d.next_attempt<=now() ORDER BY e.id LIMIT 1`, a.Account, channel).Scan(&id, &failures); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	through := int64(0)
	if failures > 0 {
		through = id
	}
	return d.deliverBatch(ctx, c, a, channel, through)
}

func (d *CallbackDispatcher) deliverBatch(ctx context.Context, c *pgx.Conn, a RuntimeCallback, channel string, through int64) (int64, error) {
	started := time.Now()
	// pgx streams rows, while SQL limits both count and aggregate source size.
	// Always include the first row so an oversized event is surfaced, not skipped.
	rows, err := c.Query(ctx, `WITH candidates AS (
 SELECT id,account,channel,direction,provider_id,sender,recipient,subject,body,occurred_at,payload,
 octet_length(payload::text)::bigint+octet_length(body)+octet_length(subject)+octet_length(sender)+octet_length(recipient)+octet_length(provider_id)+512 AS bytes
 FROM relay_channel_events e JOIN relay_channel_callback_delivery d ON d.event_id=e.id
 WHERE account=$1 AND ($2='' OR channel=$2) AND ($3::bigint=0 OR id=$3)
 AND NOT d.acknowledged AND NOT d.quarantined AND d.next_attempt<=now()
 AND ($3::bigint<>0 OR d.failures=0) ORDER BY id LIMIT 50
 ), bounded AS (
 SELECT *,row_number() OVER (ORDER BY id) AS ordinal,sum(bytes) OVER (ORDER BY id) AS total_bytes FROM candidates
 ) SELECT id,account,channel,direction,provider_id,sender,recipient,subject,body,occurred_at,payload
 FROM bounded WHERE ordinal=1 OR total_bytes<=2093056 ORDER BY id`, a.Account, channel, through)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	events := []map[string]any{}
	ids := []int64{}
	last := int64(0)
	size := len(a.Account) + 100
	for rows.Next() {
		var id int64
		var account, ch, direction, pid, sender, recipient, subject, body string
		var occurred time.Time
		var payload []byte
		if err = rows.Scan(&id, &account, &ch, &direction, &pid, &sender, &recipient, &subject, &body, &occurred, &payload); err != nil {
			return 0, err
		}
		event := map[string]any{"id": id, "payload": map[string]any{"channel": ch, "direction": direction, "provider_id": pid, "sender": sender, "recipient": recipient, "subject": subject, "body": body, "occurred_at": occurred, "payload": json.RawMessage(payload)}}
		encoded, encodeErr := json.Marshal(event)
		if encodeErr != nil {
			if len(events) > 0 {
				break
			}
			rows.Close()
			if err := recordCallbackFailure(ctx, c, []int64{id}, "invalid_event_encoding", false); err != nil {
				return 0, err
			}
			return 0, encodeErr
		}
		if size+len(encoded)+1 > (2<<20)-4096 {
			if len(events) == 0 {
				rows.Close()
				if err := recordCallbackFailure(ctx, c, []int64{id}, "payload_too_large", true); err != nil {
					return 0, err
				}
				return 0, &oversizedCallback{ID: id}
			}
			break
		}
		size += len(encoded) + 1
		events = append(events, event)
		ids = append(ids, id)
		last = id
	}
	if err = rows.Err(); err != nil || len(events) == 0 {
		return 0, err
	}
	raw, _ := json.Marshal(map[string]any{"account": a.Account, "events": events})
	// Finish the query before issuing journal writes on this connection.
	rows.Close()
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(a.CallbackSecret))
	mac.Write([]byte(fmtInt(ts) + "."))
	mac.Write(raw)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.Site, "/")+"/api/method/relay_core.inbound.receive", bytes.NewReader(raw))
	if err != nil {
		if saveErr := recordCallbackFailure(ctx, c, ids, "Invalid Core callback endpoint", false); saveErr != nil {
			return 0, saveErr
		}
		return 0, err
	}
	req.Header.Set("X-Relay-Dispatch-Ms", strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', 3, 64))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+a.APIKey+":"+a.APISecret)
	req.Header.Set("X-Relay-Timestamp", fmtInt(ts))
	req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, err := a.client().Do(req)
	ack := false
	if err == nil {
		ack = acknowledged(resp, len(events))
		resp.Body.Close()
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !ack {
		if err := recordCallbackFailure(saveCtx, c, ids, "Core did not acknowledge event batch", false); err != nil {
			return 0, err
		}
		return 0, errors.New("Core callback rejected")
	}
	if err := acknowledgeChannelEvents(saveCtx, c, a.Account, channel, ids); err != nil {
		return 0, err
	}
	return last, nil
}

type RuntimeCallback struct {
	Account, Site, APIKey, APISecret, CallbackSecret string
	HTTPClient                                       *http.Client
}

func (a RuntimeCallback) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Deliver sends one bounded Telegram/email event batch to Core. The caller
// owns scheduling; this method persists per-event acknowledgements and is idempotent
// because Core's Relay Hub Event key is unique. The legacy after value is only
// diagnostic; it never filters pending events.
func (d *CallbackDispatcher) Deliver(ctx context.Context, db *pgx.Conn, a RuntimeCallback, after int64) (int64, error) {
	last, err := d.deliverPending(ctx, db, a, "")
	return max(after, last), err
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

var acknowledged = callbackworker.Acknowledged

func recordCallbackFailure(ctx context.Context, c *pgx.Conn, ids []int64, reason string, quarantine bool) error {
	_, err := c.Exec(ctx, `UPDATE relay_channel_callback_delivery SET failures=least(failures+1,100),error=$2,
 next_attempt=now()+power(2,least(failures,8))*interval '1 second',
 quarantined=($3 OR failures>=7),updated_at=now() WHERE event_id=ANY($1) AND NOT acknowledged`, ids, reason, quarantine)
	return err
}
