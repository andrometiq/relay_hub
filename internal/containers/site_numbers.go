package containers

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
)

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

// WABAInUse reports whether another number on the Hub still uses the WhatsApp Business Account.
func (s *Store) WABAInUse(ctx context.Context, waba, key string) (bool, error) {
	var used bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_channel_credentials WHERE channel='WhatsApp' AND config->>'waba_id'=$1
 AND channel_key<>$2 AND (enabled OR site_id IS NOT NULL))`, waba, key).Scan(&used)
	return used, err
}
