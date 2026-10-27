package containers

import (
	"context"
	"encoding/json"
	"errors"
)

// These fields are write-only. Blank edits keep stored values; responses
// carry presence indicators, never decrypted values.
var sensitiveKeys = []string{"app_secret", "verify_token", "core_token", "core_api_key", "core_api_secret", "callback_secret", "imap_password"}

// Container provider settings are shared by every number of one Meta app.
var providerPublicKeys = map[string]bool{"app_id": true, "graph_version": true}
var providerPrivateKeys = []string{"app_secret", "verify_token"}

// Site credentials sign and authorize every callback to one Frappe site.
var siteSecretKeys = []string{"core_api_key", "core_api_secret", "callback_secret"}

var channelPublicKeys = map[string]map[string]bool{
	"WhatsApp": {"phone_id": true, "waba_id": true, "display_number": true, "templates": true, "groups": true, "calls": true, "messages_per_second": true, "sync_metadata": true, "connection_status": true, "verified_at": true},
	"Telegram": {"username": true, "webhook_status": true, "checked_at": true},
	"Email":    {"connection_status": true, "checked_at": true, "host": true, "port": true, "username": true, "from": true, "imap_host": true, "imap_port": true, "imap_username": true, "imap_folder": true, "trusted_authserv_id": true, "trusted_dsn_domain": true},
}

// statusKeys are written by the Hub after a check. A save keeps the stored
// value unless the caller sets the key itself.
var statusKeys = []string{"connection_status", "verified_at", "webhook_status", "checked_at"}

// A WhatsApp number may override the container's app_secret and verify_token.
var channelPrivateKeys = map[string][]string{"WhatsApp": {"core_token", "app_secret", "verify_token"}, "Email": {"imap_password"}}

func sensitive(key string) bool { return contains(sensitiveKeys, key) }

func contains(values []string, key string) bool {
	for _, candidate := range values {
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

// mergeSecrets keeps stored private values and applies non-empty edits for allowed keys.
func mergeSecrets(stored map[string]string, edits map[string]any, allowed []string) (map[string]string, error) {
	result := map[string]string{}
	for _, key := range allowed {
		if stored[key] != "" {
			result[key] = stored[key]
		}
		value, exists := edits[key]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("invalid secret field")
		}
		if text != "" {
			result[key] = text
		}
	}
	return result, nil
}

func configured(private map[string]string, keys []string) []string {
	result := []string{}
	for _, key := range keys {
		if private[key] != "" {
			result = append(result, key)
		}
	}
	return result
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
			cipher, nonce, e := s.boxMap(private)
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
