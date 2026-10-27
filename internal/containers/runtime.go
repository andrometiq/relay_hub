package containers

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// RuntimeCredential is one enabled channel with its container app and its
// site joined in. Config merges the container app, the channel and private
// values. Secret is the channel secret, or the container system-user token
// for WhatsApp numbers without their own token. It never leaves the process.
type RuntimeCredential struct {
	Key         string
	Channel     string
	Container   string
	Site        string
	Origin      string
	SiteEnabled bool
	Label       string
	Address     string
	Enabled     bool
	Config      map[string]any
	Secret      string
	Core        CoreCredentials
	// WebhookID is the random public path id: the container's for WhatsApp,
	// the channel's own for Telegram and Email. LegacyPath also accepts the key.
	WebhookID  string
	LegacyPath bool
}

const runtimeQuery = `SELECT c.channel_key,c.channel,c.container_id,coalesce(c.site_id,''),coalesce(s.origin,''),coalesce(s.enabled,false),
 c.label,coalesce(c.address,''),c.enabled AND k.enabled,c.config,c.secret_ciphertext,c.secret_nonce,c.config_ciphertext,c.config_nonce,
 coalesce(p.config,'{}'::jsonb),coalesce(p.config_ciphertext,''::bytea),coalesce(p.config_nonce,''::bytea),
 coalesce(p.secret_ciphertext,''::bytea),coalesce(p.secret_nonce,''::bytea),
 coalesce(s.secret_ciphertext,''::bytea),coalesce(s.secret_nonce,''::bytea),
 CASE WHEN c.channel='WhatsApp' THEN k.public_id ELSE c.public_id END,CASE WHEN c.channel='WhatsApp' THEN k.legacy_path ELSE c.legacy_path END
 FROM relay_channel_credentials c JOIN relay_containers k ON k.container_id=c.container_id
 LEFT JOIN relay_container_providers p ON p.container_id=c.container_id AND p.provider=c.channel
 LEFT JOIN relay_sites s ON s.site_id=c.site_id`

// Runtime returns every enabled channel of an enabled container, assigned or not.
func (s *Store) Runtime(ctx context.Context) ([]RuntimeCredential, error) {
	rows, err := s.DB.Query(ctx, runtimeQuery+` WHERE c.enabled AND k.enabled ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RuntimeCredential{}
	for rows.Next() {
		value, err := s.scanRuntime(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// Credential returns one decrypted channel for validation and connection tests.
// Enabled reflects the channel and container flags.
func (s *Store) Credential(ctx context.Context, channel, key string) (RuntimeCredential, error) {
	return s.scanRuntime(s.DB.QueryRow(ctx, runtimeQuery+` WHERE c.channel=$1 AND c.channel_key=$2`, channel, key))
}

func (s *Store) scanRuntime(row pgx.Row) (RuntimeCredential, error) {
	var v RuntimeCredential
	var raw, secret, secretNonce, configCipher, configNonce []byte
	var providerRaw, providerCipher, providerNonce, token, tokenNonce, siteCipher, siteNonce []byte
	if err := row.Scan(&v.Key, &v.Channel, &v.Container, &v.Site, &v.Origin, &v.SiteEnabled, &v.Label, &v.Address, &v.Enabled,
		&raw, &secret, &secretNonce, &configCipher, &configNonce, &providerRaw, &providerCipher, &providerNonce, &token, &tokenNonce, &siteCipher, &siteNonce, &v.WebhookID, &v.LegacyPath); err != nil {
		return v, err
	}
	v.Config = map[string]any{}
	for _, part := range [][]byte{providerRaw, raw} {
		values := map[string]any{}
		if len(part) > 0 && json.Unmarshal(part, &values) != nil {
			return v, errors.New("invalid channel configuration")
		}
		for key, value := range values {
			v.Config[key] = value
		}
	}
	for _, encrypted := range [][2][]byte{{providerCipher, providerNonce}, {configCipher, configNonce}} {
		private, err := s.configSecrets(encrypted[0], encrypted[1])
		if err != nil {
			return v, err
		}
		for key, value := range private {
			v.Config[key] = value
		}
	}
	core, err := s.configSecrets(siteCipher, siteNonce)
	if err != nil {
		return v, err
	}
	v.Core = CoreCredentials{APIKey: core["core_api_key"], APISecret: core["core_api_secret"], CallbackSecret: core["callback_secret"]}
	encrypted, nonce := secret, secretNonce
	if len(encrypted) == 0 {
		encrypted, nonce = token, tokenNonce
	}
	if len(encrypted) > 0 {
		if v.Secret, err = s.unbox(encrypted, nonce); err != nil {
			return v, errors.New("stored credential cannot be decrypted")
		}
	}
	return v, nil
}
