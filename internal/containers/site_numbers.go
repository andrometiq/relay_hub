package containers

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// UpdateSiteNumber writes public settings or check results of one WhatsApp
// number of site and tells the site. A nil enabled keeps the flag. A number of
// another site is pgx.ErrNoRows.
func (s *Store) UpdateSiteNumber(ctx context.Context, site, key string, enabled *bool, values map[string]any) error {
	for name := range values {
		if !channelPublicKeys["WhatsApp"][name] {
			return errors.New("unsupported number field")
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE relay_channel_credentials SET enabled=coalesce($3,enabled),config=config||$4::jsonb,updated_at=now()
 WHERE channel='WhatsApp' AND channel_key=$2 AND site_id=$1`, site, key, enabled, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err = notifyChannels(ctx, tx, site); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
