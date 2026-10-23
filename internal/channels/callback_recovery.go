package channels

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// RetryQuarantined never rewinds a production cursor or replays adjacent events.
// Core's event key handles a lost acknowledgement followed by a retry.
func (d *CallbackDispatcher) RetryQuarantined(ctx context.Context, account, channel string, id int64) error {
	if id < 1 || (channel != "Telegram" && channel != "Email") {
		return errors.New("invalid callback")
	}
	d.mu.RLock()
	var a RuntimeCallback
	for _, candidate := range d.Accounts {
		if candidate.Account == account {
			a = candidate
			break
		}
	}
	d.mu.RUnlock()
	if a.Account == "" {
		return errors.New("callback account not configured")
	}
	c, err := pgx.Connect(ctx, d.URL)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	var locked bool
	if err = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,2))", account+":"+channel).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("callback channel is busy")
	}
	var exists bool
	if err = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_channel_callback_quarantine WHERE event_id=$1 AND account=$2 AND channel=$3 AND resolved_at IS NULL)`, id, account, channel).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("callback is not quarantined for this account and channel")
	}
	last, err := d.deliverRange(ctx, c, a, channel, id-1, id)
	if err != nil {
		return err
	}
	if last != id {
		return errors.New("original callback event is unavailable")
	}
	_, err = c.Exec(ctx, `UPDATE relay_channel_callback_quarantine SET resolved_at=now() WHERE event_id=$1 AND account=$2 AND channel=$3 AND resolved_at IS NULL`, id, account, channel)
	return err
}
