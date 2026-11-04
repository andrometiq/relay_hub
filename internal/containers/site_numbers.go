package containers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

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

// SetNumberPIN stores a new two-step verification PIN of one number of site.
// The PIN is encrypted with the other private values and is never returned.
func (s *Store) SetNumberPIN(ctx context.Context, site, key, pin string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cipher, nonce []byte
	err = tx.QueryRow(ctx, `SELECT config_ciphertext,config_nonce FROM relay_channel_credentials
 WHERE channel='WhatsApp' AND channel_key=$2 AND site_id=$1 FOR UPDATE`, site, key).Scan(&cipher, &nonce)
	if err != nil {
		return err
	}
	private, err := s.configSecrets(cipher, nonce)
	if err != nil {
		return err
	}
	private["pin"] = pin
	if cipher, nonce, err = s.boxMap(private); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_channel_credentials SET config_ciphertext=$3,config_nonce=$4,updated_at=now()
 WHERE channel='WhatsApp' AND channel_key=$2 AND site_id=$1`, site, key, cipher, nonce); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NewPIN makes a random six-digit two-step verification PIN.
func NewPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
