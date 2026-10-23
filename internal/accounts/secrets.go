package accounts

import (
	"context"
	"encoding/json"
	"errors"
)

// These fields are write-only in account configuration. Blank edits preserve
// stored values; the UI receives presence indicators, never decrypted values.
var sensitiveKeys = []string{"app_secret", "verify_token", "core_token", "core_api_key", "core_api_secret", "callback_secret", "imap_password"}

var publicConfigKeys = map[string]map[string]bool{
	"WhatsApp": {"app_id": true, "phone_id": true, "waba_id": true, "graph_version": true, "templates": true, "groups": true, "calls": true, "messages_per_second": true, "sync_metadata": true, "connection_status": true, "verified_at": true},
	"Telegram": {},
	"Email":    {"host": true, "port": true, "username": true, "from": true, "imap_host": true, "imap_port": true, "imap_username": true, "imap_folder": true, "trusted_authserv_id": true, "trusted_dsn_domain": true},
}

func sensitive(key string) bool {
	for _, candidate := range sensitiveKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

func (s *Store) configSecrets(ciphertext, nonce []byte) (map[string]string, error) {
	result := map[string]string{}
	if len(ciphertext) == 0 {
		return result, nil
	}
	raw, err := s.unbox(ciphertext, nonce)
	if err != nil || json.Unmarshal([]byte(raw), &result) != nil {
		return nil, errors.New("stored configuration cannot be decrypted")
	}
	return result, nil
}

// ProtectLegacyConfig upgrades all rows, including disabled channels, atomically.
// Batches bound memory. It is repeatable and never replaces the encryption key.
// Any malformed/encrypted row aborts the transaction, preserving existing data.
func (s *Store) ProtectLegacyConfig(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(771842015)`); err != nil {
		return err
	}
	for {
		rows, e := tx.Query(ctx, `SELECT id,config,config_ciphertext,config_nonce FROM relay_channel_credentials WHERE config ?| $1::text[] ORDER BY id LIMIT 100 FOR UPDATE`, sensitiveKeys)
		if e != nil {
			return e
		}
		type item struct {
			id                 int64
			raw, cipher, nonce []byte
		}
		batch := []item{}
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.raw, &v.cipher, &v.nonce); e != nil {
				rows.Close()
				return e
			}
			batch = append(batch, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(batch) == 0 {
			break
		}
		for _, v := range batch {
			public := map[string]any{}
			if json.Unmarshal(v.raw, &public) != nil {
				return errors.New("invalid legacy configuration")
			}
			private, e := s.configSecrets(v.cipher, v.nonce)
			if e != nil {
				return e
			}
			for _, key := range sensitiveKeys {
				if value, exists := public[key]; exists {
					secret, ok := value.(string)
					if !ok {
						return errors.New("invalid legacy secret")
					}
					if secret != "" {
						private[key] = secret
					}
					delete(public, key)
				}
			}
			raw, e := json.Marshal(private)
			if e != nil {
				return e
			}
			cipher, nonce, e := s.box(string(raw))
			if e != nil {
				return e
			}
			safe, e := json.Marshal(public)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE relay_channel_credentials SET config=$2,config_ciphertext=$3,config_nonce=$4,updated_at=now() WHERE id=$1`, v.id, safe, cipher, nonce); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
