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
	"strings"
	"time"
)

// Separate worker slots prevent slow status acknowledgements from occupying
// the realtime callback worker pool.
func (d *CallbackDispatcher) runStatusCallbacks(ctx context.Context) {
	go callbackworker.Run(ctx, time.Second, 8, func() []callbackworker.Job {
		d.mu.RLock()
		accounts := append([]RuntimeCallback(nil), d.Accounts...)
		d.mu.RUnlock()
		jobs := []callbackworker.Job{}
		seen := map[string]bool{}
		for _, a := range accounts {
			if seen[a.Account] {
				continue
			}
			seen[a.Account] = true
			jobs = append(jobs, callbackworker.Job{Key: a.Account + ":statuses", Run: func(jobCtx context.Context) { _ = d.deliverStatuses(jobCtx, a) }})
		}
		return jobs
	})
}
func (d *CallbackDispatcher) deliverStatuses(ctx context.Context, a RuntimeCallback) error {
	c, err := pgx.Connect(ctx, d.URL)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	var locked bool
	if err = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,732))", a.Account).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	if _, err = c.Exec(ctx, `INSERT INTO relay_channel_status_schedule(account) VALUES($1) ON CONFLICT DO NOTHING`, a.Account); err != nil {
		return err
	}
	var ready bool
	if err = c.QueryRow(ctx, `SELECT next_attempt<=now() FROM relay_channel_status_schedule WHERE account=$1`, a.Account).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return nil
	}
	batch, err := PendingStatusReceipts(ctx, c, a.Account)
	if err != nil || len(batch) == 0 {
		return err
	}
	err = postStatusBatch(ctx, a, batch)
	// The caller timing out must not prevent a durable ACK or retry schedule.
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err != nil {
		_, _ = c.Exec(saveCtx, `UPDATE relay_channel_status_schedule SET failures=least(failures+1,100),next_attempt=now()+interval '5 seconds' WHERE account=$1`, a.Account)
		return err
	}
	ids := make([]int64, len(batch))
	for i, row := range batch {
		ids[i] = row.ID
	}
	if err = AcknowledgeStatusReceipts(saveCtx, c, a.Account, ids); err != nil {
		return err
	}
	_, err = c.Exec(saveCtx, `UPDATE relay_channel_status_schedule SET failures=0,next_attempt=now() WHERE account=$1`, a.Account)
	return err
}
func postStatusBatch(ctx context.Context, a RuntimeCallback, batch []StatusReceipt) error {
	raw, err := json.Marshal(map[string]any{"account": a.Account, "statuses": batch})
	if err != nil {
		return err
	}
	ts := fmtInt(time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(a.CallbackSecret))
	mac.Write([]byte(ts + "."))
	mac.Write(raw)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.Site, "/")+"/api/method/relay_core.inbound.receive_channel_statuses", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+a.APIKey+":"+a.APISecret)
	req.Header.Set("X-Relay-Timestamp", ts)
	req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !acknowledged(resp, len(batch)) {
		return errors.New("Core did not acknowledge channel statuses")
	}
	return nil
}
