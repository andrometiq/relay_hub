package channels

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RecoverCallback keeps the original event and receipts immutable. Replay
// reuses their identities, so a lost Core acknowledgement cannot duplicate them.
func (d *CallbackDispatcher) RecoverCallback(ctx context.Context, account, lane string, id, actor int64, action, note string) error {
	if id < 1 || actor < 1 || (lane != "Telegram" && lane != "Email") || (action != "quarantine" && action != "replay") || len(strings.TrimSpace(note)) < 10 || len(note) > 2000 {
		return errors.New("callback lane, action and recovery evidence required")
	}
	c, err := pgx.Connect(ctx, d.URL)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var locked bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,2))", account+":"+lane).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("callback lane is busy")
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_channel_callback_delivery d SET quarantined=$4,acknowledged=false,next_attempt=now(),failures=CASE WHEN $4 THEN d.failures ELSE 1 END,updated_at=now()
 FROM relay_channel_events e WHERE e.id=d.event_id AND e.account=$1 AND d.event_id=$2 AND e.channel=$3
 AND NOT d.acknowledged AND (d.failures>0 OR d.quarantined)`, account, id, lane, action == "quarantine")
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("failed callback not found")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_channel_callback_audit(event_id,actor,action,note) VALUES($1,$2,$3,$4)`, id, actor, action, note); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
