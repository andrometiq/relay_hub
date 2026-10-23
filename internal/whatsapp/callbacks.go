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
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CallbackDispatcher separates realtime replies/calls from batched statuses.
// Each cursor advances only after Core confirms its durable transaction.
type CallbackDispatcher struct {
	URL     string
	Service *Service
}

func (d *CallbackDispatcher) Run(ctx context.Context) {
	for _, lane := range []string{"realtime", "status"} {
		interval := 250 * time.Millisecond
		if lane == "status" {
			interval = time.Second
		}
		go callbackworker.Run(ctx, interval, 8, func() []callbackworker.Job {
			accounts := []Account{}
			for _, a := range d.Service.AccountsSnapshot() {
				if a.CoreAPIKey != "" {
					accounts = append(accounts, a)
				}
			}
			sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
			jobs := make([]callbackworker.Job, 0, len(accounts))
			for _, a := range accounts {
				jobs = append(jobs, callbackworker.Job{Key: a.ID, Run: func(jobCtx context.Context) { _ = d.deliver(jobCtx, a, lane) }})
			}
			return jobs
		})
	}
}

func (d *CallbackDispatcher) deliver(ctx context.Context, a Account, lane string) error {
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
	var cursor int64
	var failures, offset int
	if err = c.QueryRow(ctx, "SELECT last_id,failures,status_offset FROM relay_callback_cursor WHERE account=$1 AND lane=$2 AND next_attempt<=now()", a.ID, lane).Scan(&cursor, &failures, &offset); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	eventLimit := 50
	if lane == "status" {
		eventLimit = 500
	}
	rows, err := c.Query(ctx, `WITH page AS (SELECT id,payload FROM relay_whatsapp_events WHERE account=$1 AND lane=$2 AND (id>$3 OR (id=$3 AND $4>0)) ORDER BY id LIMIT $5), sized AS (SELECT *,sum(octet_length(payload::text)) OVER(ORDER BY id) bytes,row_number() OVER(ORDER BY id) position FROM page) SELECT id,payload FROM sized WHERE bytes<=524288 OR position=1 ORDER BY id`, a.ID, lane, cursor, offset, eventLimit)
	if err != nil {
		return err
	}
	events := []Event{}
	for rows.Next() {
		var event Event
		if err = rows.Scan(&event.ID, &event.Payload); err != nil {
			rows.Close()
			return err
		}
		events = append(events, event)
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
		var statuses []CompactStatus
		statuses, next, err = buildStatusBatch(events, StatusCursor{EventID: cursor, Offset: offset}, statusBatchBytes)
		if err != nil {
			return err
		}
		if len(statuses) == 0 {
			_, err = c.Exec(ctx, "UPDATE relay_callback_cursor SET last_id=$3,status_offset=$4,failures=0,next_attempt=now() WHERE account=$1 AND lane=$2", a.ID, lane, next.EventID, next.Offset)
			return err
		}
		accepted = len(statuses)
		endpoint = "/api/method/relay_core.inbound.receive_statuses"
		values = map[string]any{"account": a.ID, "statuses": statuses}
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.Site, "/")+endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(a.CallbackSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+a.CoreAPIKey+":"+a.CoreAPISecret)
	req.Header.Set("X-Relay-Timestamp", timestamp)
	req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := d.Service.client.Do(req)
	ack := false
	if err == nil {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 65537))
		response.Body.Close()
		var reply struct {
			Message struct {
				Accepted int `json:"accepted"`
			} `json:"message"`
		}
		ack = response.StatusCode == 200 && readErr == nil && len(raw) <= 65536 && json.Unmarshal(raw, &reply) == nil && reply.Message.Accepted == accepted
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if ack {
		_, err = c.Exec(saveCtx, "UPDATE relay_callback_cursor SET last_id=$3,status_offset=$4,failures=0,next_attempt=now() WHERE account=$1 AND lane=$2", a.ID, lane, next.EventID, next.Offset)
	} else {
		delay := time.Second << min(failures, 8)
		_, err = c.Exec(saveCtx, "UPDATE relay_callback_cursor SET failures=least(failures+1,100),next_attempt=now()+$3::interval WHERE account=$1 AND lane=$2", a.ID, lane, delay.String())
		if err == nil {
			err = errors.New("Core did not acknowledge event batch")
		}
	}
	return err
}
