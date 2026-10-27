package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const bulkUpdateLimit = 500

// DefaultBulkWindow is how long a bulk delivery change may wait for others
// before Core receives the batch.
const DefaultBulkWindow = 30 * time.Second

// BulkUpdate is the latest coalesced state of one bulk outbox row.
type BulkUpdate struct {
	ID         int64           `json:"id"`
	Key        string          `json:"key"`
	State      string          `json:"state"`
	MessageID  string          `json:"message_id"`
	Status     string          `json:"status"`
	Error      json.RawMessage `json:"error,omitempty"`
	UpdatedAt  time.Time       `json:"updated_at"`
	ChannelKey string          `json:"channel_key,omitempty"`
	To         string          `json:"to,omitempty"`
	changed    time.Time
}

// ParseBulkWindow reads RELAY_BULK_UPDATE_WINDOW; empty keeps the default.
func ParseBulkWindow(value string) (time.Duration, error) {
	if value == "" {
		return DefaultBulkWindow, nil
	}
	window, err := time.ParseDuration(value)
	if err != nil || window < time.Second || window > 5*time.Minute {
		return 0, errors.New("RELAY_BULK_UPDATE_WINDOW must be a duration between 1s and 5m")
	}
	return window, nil
}

func (d *CallbackDispatcher) bulkWindow() time.Duration {
	if d.BulkWindow > 0 {
		return d.BulkWindow
	}
	return DefaultBulkWindow
}

// deliverBulk sends one window of bulk changes. A row changed after it was
// read stays pending, so its newer state is reported in a later window.
func (d *CallbackDispatcher) deliverBulk(ctx context.Context, a Account) error {
	normalizeAccount(&a)
	started := time.Now()
	c, err := pgx.Connect(ctx, d.URL)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	var locked bool
	if err = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,1))", a.ID+":bulk").Scan(&locked); err != nil || !locked {
		return err
	}
	if _, err = c.Exec(ctx, "INSERT INTO relay_callback_cursor(account,lane) VALUES($1,'bulk') ON CONFLICT DO NOTHING", a.ID); err != nil {
		return err
	}
	var ready bool
	err = c.QueryRow(ctx, `SELECT (SELECT next_attempt<=now() FROM relay_callback_cursor WHERE account=$1 AND lane='bulk')
 AND (count(*)>=$3 OR coalesce(min(first_at)<=now()-$2::interval,false))
 FROM (SELECT first_at FROM relay_bulk_pending WHERE account=$1 ORDER BY first_at LIMIT $3) due`, a.ID, d.bulkWindow().String(), bulkUpdateLimit).Scan(&ready)
	if err != nil || !ready {
		return err
	}
	updates, err := pendingBulkUpdates(ctx, c, a.ID)
	if err != nil || len(updates) == 0 {
		return err
	}
	for i := range updates {
		updates[i].ChannelKey, updates[i].To = a.ID, a.To()
	}
	ack, err := d.post(ctx, a, "/api/method/relay_core.inbound.receive_bulk_updates", map[string]any{"account": a.SiteID, "channel_key": a.ID, "updates": updates}, len(updates), started)
	if err != nil {
		return err
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !ack {
		_, err = c.Exec(saveCtx, `UPDATE relay_callback_cursor SET failures=least(failures+1,100),
 next_attempt=now()+power(2,least(failures,6))*interval '1 second' WHERE account=$1 AND lane='bulk'`, a.ID)
		if err != nil {
			return err
		}
		return errors.New("Core did not acknowledge bulk updates")
	}
	if err = acknowledgeBulk(saveCtx, c, a.ID, updates); err != nil {
		return err
	}
	return touchSite(saveCtx, c, a.SiteID)
}

func pendingBulkUpdates(ctx context.Context, c *pgx.Conn, account string) ([]BulkUpdate, error) {
	rows, err := c.Query(ctx, `SELECT p.outbox_id,p.changed_at,o.request_key,o.state,coalesce(o.result->'messages'->0->>'id',''),
 coalesce(s.status,''),CASE WHEN s.status='failed' THEN s.error WHEN o.state<>'submitted' THEN o.provider_error END,
 greatest(o.updated_at,coalesce(s.updated_at,o.updated_at))
 FROM (SELECT outbox_id,changed_at FROM relay_bulk_pending WHERE account=$1 ORDER BY first_at,outbox_id LIMIT $2) p
 JOIN relay_outbox o ON o.id=p.outbox_id
 LEFT JOIN relay_whatsapp_status s ON s.account=o.account AND s.provider_id=o.result->'messages'->0->>'id'
 ORDER BY p.outbox_id`, account, bulkUpdateLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	updates := []BulkUpdate{}
	for rows.Next() {
		var u BulkUpdate
		var detail []byte
		if err = rows.Scan(&u.ID, &u.changed, &u.Key, &u.State, &u.MessageID, &u.Status, &detail, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Error = bulkError(detail)
		updates = append(updates, u)
	}
	return updates, rows.Err()
}

// bulkError reduces a provider error to the code and safe text Core shows.
func bulkError(raw []byte) json.RawMessage {
	var value struct {
		Code    int    `json:"code"`
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value.Code == 0 {
		return nil
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func acknowledgeBulk(ctx context.Context, c *pgx.Conn, account string, updates []BulkUpdate) error {
	ids := make([]int64, len(updates))
	changed := make([]time.Time, len(updates))
	for i, u := range updates {
		ids[i], changed[i] = u.ID, u.changed
	}
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM relay_bulk_pending p USING unnest($2::bigint[],$3::timestamptz[]) AS acked(id,changed)
 WHERE p.account=$1 AND p.outbox_id=acked.id AND p.changed_at=acked.changed`, account, ids, changed); err != nil {
		return err
	}
	// Rows that changed while Core was reading wait a fresh window.
	if _, err = tx.Exec(ctx, `UPDATE relay_bulk_pending SET first_at=clock_timestamp() WHERE account=$1 AND outbox_id=ANY($2)`, account, ids); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE relay_callback_cursor SET last_id=$2,failures=0,next_attempt=now() WHERE account=$1 AND lane='bulk'", account, ids[len(ids)-1]); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
