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

// RemoveSiteNumber takes one number away from site. The token, the PIN and
// the number's own app values are wiped, the number is turned off and unlinked,
// and the site gets a relay_channels event. The row stays for history and the
// outbox. It refuses while the site has open sends on the number.
func (s *Store) RemoveSiteNumber(ctx context.Context, site, key string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT channel_key FROM relay_channel_credentials WHERE channel='WhatsApp' AND channel_key=$2 AND site_id=$1 FOR UPDATE`, site, key).Scan(&key); err != nil {
		return err
	}
	if err = numberLeaving(ctx, tx, site, key); err != nil {
		return err
	}
	cipher, nonce, err := s.boxMap(map[string]string{})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_channel_credentials SET enabled=false,site_id=NULL,assigned_at=NULL,
 secret_ciphertext=''::bytea,secret_nonce=''::bytea,config_ciphertext=$2,config_nonce=$3,
 config=(config-'register_attempts')||'{"partner_status":"removed","connection_status":"disabled"}'::jsonb,updated_at=now()
 WHERE channel='WhatsApp' AND channel_key=$1`, key, cipher, nonce); err != nil {
		return err
	}
	if err = notifyChannels(ctx, tx, site); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// numberLeaving refuses to take a number from site while the site's sends on it are open.
func numberLeaving(ctx context.Context, tx pgx.Tx, site, key string) error {
	rows, err := tx.Query(ctx, `SELECT channel,channel_key FROM relay_channel_credentials WHERE site_id=$1 AND NOT (channel='WhatsApp' AND channel_key=$2)`, site, key)
	if err != nil {
		return err
	}
	var providers, keys []string
	var provider, other string
	if _, err = pgx.ForEachRow(rows, []any{&provider, &other}, func() error {
		providers, keys = append(providers, provider), append(keys, other)
		return nil
	}); err != nil {
		return err
	}
	return pendingSends(ctx, tx, site, providers, keys)
}

// WABAInUse reports whether another number on the Hub still uses the WhatsApp Business Account.
func (s *Store) WABAInUse(ctx context.Context, waba, key string) (bool, error) {
	var used bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_channel_credentials WHERE channel='WhatsApp' AND config->>'waba_id'=$1
 AND channel_key<>$2 AND (enabled OR site_id IS NOT NULL))`, waba, key).Scan(&used)
	return used, err
}
