package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/andrometiq/relay_hub/internal/callbackworker"
	"github.com/andrometiq/relay_hub/internal/lifecycle"
	"github.com/jackc/pgx/v5"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CallbackDispatcher acknowledges immutable events independently in each lane.
type CallbackDispatcher struct {
	URL        string
	Service    *Service
	BulkWindow time.Duration
}

func (d *CallbackDispatcher) Run(ctx context.Context) {
	for _, lane := range []string{"realtime", "status", "bulk"} {
		interval := 250 * time.Millisecond
		var wake <-chan struct{} = d.Service.callbackWake
		if lane != "realtime" {
			interval = time.Second
			wake = nil
		}
		lifecycle.Go(ctx, func() {
			callbackworker.RunWithWake(ctx, interval, 8, func() []callbackworker.Job {
				accounts := []Account{}
				for _, a := range d.Service.AccountsSnapshot() {
					if a.CoreAPIKey != "" {
						accounts = append(accounts, a)
					}
				}
				sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
				jobs := make([]callbackworker.Job, 0, len(accounts))
				for _, a := range accounts {
					run := func(jobCtx context.Context) { _ = d.deliver(jobCtx, a, lane) }
					if lane == "bulk" {
						run = func(jobCtx context.Context) { _ = d.deliverBulk(jobCtx, a) }
					}
					jobs = append(jobs, callbackworker.Job{Key: a.ID, Run: run})
				}
				return jobs
			}, wake)
		})
	}
}

func (d *CallbackDispatcher) deliver(ctx context.Context, a Account, lane string) error {
	normalizeAccount(&a)
	started := time.Now()
	c, err := pgx.Connect(ctx, d.URL)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	// Session advisory lock avoids concurrent callbacks for the same account/lane
	// across replicas. No transaction is held across the network operation.
	var locked bool
	if err = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,1))", a.ID+":"+lane).Scan(&locked); err != nil || !locked {
		return err
	}
	if _, err = c.Exec(ctx, "INSERT INTO relay_callback_cursor(account,lane) VALUES($1,$2) ON CONFLICT DO NOTHING", a.ID, lane); err != nil {
		return err
	}
	eventLimit := 50
	if lane == "status" {
		eventLimit = 500
	}
	rows, err := c.Query(ctx, `WITH page AS (
 SELECT e.id,e.payload,d.status_offset FROM relay_whatsapp_callback_delivery d
 JOIN relay_whatsapp_events e ON e.id=d.event_id
 WHERE e.account=$1 AND e.site_id=$4 AND d.lane=$2 AND NOT d.acknowledged AND NOT d.quarantined
 AND d.next_attempt<=now() ORDER BY e.id LIMIT $3
 ), sized AS (SELECT *,sum(octet_length(payload::text)) OVER(ORDER BY id) bytes,
 row_number() OVER(ORDER BY id) position FROM page)
 SELECT id,payload,status_offset FROM sized WHERE bytes<=524288 OR position=1 ORDER BY id`, a.ID, lane, eventLimit, a.SiteID)
	if err != nil {
		return err
	}
	events := []Event{}
	offsets := map[int64]int{}
	for rows.Next() {
		var event Event
		var offset int
		if err = rows.Scan(&event.ID, &event.Payload, &offset); err != nil {
			rows.Close()
			return err
		}
		events = append(events, event)
		offsets[event.ID] = offset
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(events) == 0 {
		return err
	}
	next := StatusCursor{EventID: events[len(events)-1].ID}
	accepted := len(events)
	endpoint := "/api/method/relay_core.inbound.receive"
	values := map[string]any{"account": a.ID, "events": events}
	if lane == "status" {
		statuses := []CompactStatus{}
		size := 2
		consumed := []Event{}
		var receiptFailure error
		for _, event := range events {
			start := StatusCursor{}
			if offsets[event.ID] > 0 {
				start = StatusCursor{EventID: event.ID, Offset: offsets[event.ID]}
			}
			budget := statusBatchBytes - size + 2
			if len(statuses) > 0 {
				budget--
			}
			batch, cursor, buildErr := buildStatusBatchLimit([]Event{event}, start, budget, statusBatchLimit-len(statuses))
			if buildErr != nil {
				if errors.Is(buildErr, errStatusBatchFull) && len(statuses) > 0 {
					break
				}
				position := offsets[event.ID]
				var receiptErr *receiptError
				if errors.As(buildErr, &receiptErr) {
					position = receiptErr.Position
				}
				if err := callbackFailure(ctx, c, []Event{event}, lane, "Invalid delivery receipt", position); err != nil {
					return err
				}
				receiptFailure = buildErr
				continue
			}
			statuses = append(statuses, batch...)
			encoded, _ := json.Marshal(statuses)
			size = len(encoded)
			next = cursor
			consumed = append(consumed, event)
			if cursor.Offset > 0 || len(statuses) == statusBatchLimit || size >= statusBatchBytes {
				break
			}
		}
		if len(consumed) == 0 {
			return receiptFailure
		}
		events = consumed
		accepted = len(statuses)
		endpoint = "/api/method/relay_core.inbound.receive_statuses"
		values = map[string]any{"account": a.ID, "statuses": statuses}
	} else {
		for i := range events {
			events[i].Payload = realtimePayload(events[i].Payload)
		}
	}
	if accepted == 0 {
		return acknowledgeCallbacks(ctx, c, a.ID, lane, events, next)
	}
	ack, err := d.post(ctx, a, endpoint, values, accepted, started)
	if err != nil {
		return err
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if ack {
		return acknowledgeCallbacks(saveCtx, c, a.ID, lane, events, next)
	}
	if err := callbackFailure(saveCtx, c, events, lane, "Core did not acknowledge event batch", -1); err != nil {
		return err
	}
	return errors.New("Core did not acknowledge event batch")
}

// post signs one callback batch and reports whether Core durably accepted it.
func (d *CallbackDispatcher) post(ctx context.Context, a Account, endpoint string, values any, accepted int, started time.Time) (bool, error) {
	payload, err := json.Marshal(values)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.Site, "/")+endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(a.CallbackSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+a.CoreAPIKey+":"+a.CoreAPISecret)
	req.Header.Set("X-Relay-Timestamp", timestamp)
	req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Relay-Dispatch-Ms", strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', 3, 64))
	response, err := d.Service.client.Do(req)
	if err != nil {
		return false, nil
	}
	defer response.Body.Close()
	return callbackworker.Acknowledged(response, accepted), nil
}

func acknowledgeCallbacks(ctx context.Context, c *pgx.Conn, account, lane string, events []Event, next StatusCursor) error {
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	for _, event := range events {
		offset := 0
		if event.ID == next.EventID {
			offset = next.Offset
		}
		if _, err = tx.Exec(ctx, `INSERT INTO relay_whatsapp_callback_delivery(event_id,lane,acknowledged,status_offset)
   VALUES($1,$2,$3,$4) ON CONFLICT(event_id,lane) DO UPDATE SET acknowledged=excluded.acknowledged,
   status_offset=excluded.status_offset,failures=0,error='',error_position=-1,next_attempt=now(),updated_at=now()`, event.ID, lane, offset == 0, offset); err != nil {
			return err
		}
	}
	// Retain the legacy cursor for diagnostics only; never use it to select work.
	if _, err = tx.Exec(ctx, "UPDATE relay_callback_cursor SET last_id=$3,status_offset=$4,failures=0,next_attempt=now() WHERE account=$1 AND lane=$2", account, lane, next.EventID, next.Offset); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func callbackFailure(ctx context.Context, c *pgx.Conn, events []Event, lane, reason string, position int) error {
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	for _, event := range events {
		if _, err = tx.Exec(ctx, `INSERT INTO relay_whatsapp_callback_delivery(event_id,lane,failures,error,error_position,next_attempt)
   VALUES($1,$2,1,$3,$4,now()+interval '1 second') ON CONFLICT(event_id,lane) DO UPDATE SET
   failures=least(relay_whatsapp_callback_delivery.failures+1,100),error=excluded.error,error_position=excluded.error_position,
   next_attempt=now()+power(2,least(relay_whatsapp_callback_delivery.failures,8))*interval '1 second',updated_at=now()`, event.ID, lane, reason, position); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
