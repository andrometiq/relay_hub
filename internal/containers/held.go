package containers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNotLinked means a channel has no site to give its held events to.
var ErrNotLinked = errors.New("link the account to a site first")

// HeldEvents counts the inbound events a channel received while it had no site.
func (s *Store) HeldEvents(ctx context.Context, ref ChannelRef) (int, error) {
	if _, err := s.GetChannel(ctx, ref); err != nil {
		return 0, err
	}
	var count int
	err := s.DB.QueryRow(ctx, `SELECT CASE WHEN $1='WhatsApp'
 THEN (SELECT count(*) FROM relay_whatsapp_events WHERE account=$2 AND site_id IS NULL AND replayed_at IS NULL)
 ELSE (SELECT count(*) FROM relay_channel_events WHERE channel=$1 AND account=$2 AND site_id IS NULL AND replayed_at IS NULL) END`,
		ref.Channel, ref.Key).Scan(&count)
	return count, err
}

// ReplayHeld gives the held events of a channel to the site it is linked to now.
func (s *Store) ReplayHeld(ctx context.Context, ref ChannelRef) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var site string
	if err = tx.QueryRow(ctx, `SELECT coalesce(site_id,'') FROM relay_channel_credentials WHERE channel=$1 AND channel_key=$2 FOR UPDATE`, ref.Channel, ref.Key).Scan(&site); err != nil {
		return 0, err
	}
	if site == "" {
		return 0, ErrNotLinked
	}
	replayed, err := replayHeld(ctx, tx, site, []string{ref.Channel}, []string{ref.Key})
	if err != nil {
		return 0, err
	}
	return replayed, tx.Commit(ctx)
}

// replayHeld moves held events of the given channels to site. Each event is
// moved once, and Core drops a repeated Hub event id or provider message id.
func replayHeld(ctx context.Context, tx pgx.Tx, site string, providers, keys []string) (int64, error) {
	whatsapp, err := tx.Exec(ctx, `UPDATE relay_whatsapp_events e SET site_id=$1,replayed_at=now()
 FROM unnest($2::text[],$3::text[]) r(channel,channel_key)
 WHERE r.channel='WhatsApp' AND e.account=r.channel_key AND e.site_id IS NULL AND e.replayed_at IS NULL`, site, providers, keys)
	if err != nil {
		return 0, err
	}
	others, err := tx.Exec(ctx, `UPDATE relay_channel_events e SET site_id=$1,replayed_at=now()
 FROM unnest($2::text[],$3::text[]) r(channel,channel_key)
 WHERE e.channel=r.channel AND e.account=r.channel_key AND e.site_id IS NULL AND e.replayed_at IS NULL`, site, providers, keys)
	if err != nil {
		return 0, err
	}
	return whatsapp.RowsAffected() + others.RowsAffected(), nil
}
