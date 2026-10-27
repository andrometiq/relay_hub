package containers

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Channel is one WhatsApp number, Telegram bot or mailbox inside a container.
// Site is empty while the channel is not assigned.
type Channel struct {
	Key               string         `json:"key"`
	Channel           string         `json:"channel"`
	Container         string         `json:"container"`
	Label             string         `json:"label"`
	Address           string         `json:"address,omitempty"`
	Site              string         `json:"site,omitempty"`
	Enabled           bool           `json:"enabled"`
	Config            map[string]any `json:"config"`
	ConfiguredSecrets []string       `json:"configured_secrets"`
}

// ChannelRef names one channel. Keys are unique per provider.
type ChannelRef struct {
	Channel string `json:"channel"`
	Key     string `json:"key"`
}

type ChannelFilter struct {
	Container, Site string
}

var telegramBotID = regexp.MustCompile(`^([0-9]{1,20}):`)

const channelColumns = `channel_key,channel,container_id,label,coalesce(address,''),coalesce(site_id,''),enabled,config,config_ciphertext,config_nonce,length(secret_ciphertext)>0`

func (s *Store) Channels(ctx context.Context, filter ChannelFilter) ([]Channel, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+channelColumns+` FROM relay_channel_credentials
 WHERE ($1='' OR container_id=$1) AND ($2='' OR site_id=$2) ORDER BY channel,channel_key`, filter.Container, filter.Site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Channel{}
	for rows.Next() {
		channel, err := s.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, channel)
	}
	return result, rows.Err()
}

func (s *Store) GetChannel(ctx context.Context, ref ChannelRef) (Channel, error) {
	return s.scanChannel(s.DB.QueryRow(ctx, `SELECT `+channelColumns+` FROM relay_channel_credentials WHERE channel=$1 AND channel_key=$2`, ref.Channel, ref.Key))
}

func (s *Store) scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	var raw, cipher, nonce []byte
	var hasSecret bool
	if err := row.Scan(&c.Key, &c.Channel, &c.Container, &c.Label, &c.Address, &c.Site, &c.Enabled, &raw, &cipher, &nonce, &hasSecret); err != nil {
		return c, err
	}
	if json.Unmarshal(raw, &c.Config) != nil {
		return c, errors.New("invalid channel configuration")
	}
	if c.Config == nil {
		c.Config = map[string]any{}
	}
	private, err := s.configSecrets(cipher, nonce)
	if err != nil {
		return c, err
	}
	c.ConfiguredSecrets = []string{}
	if hasSecret {
		c.ConfiguredSecrets = append(c.ConfiguredSecrets, "secret")
	}
	c.ConfiguredSecrets = append(c.ConfiguredSecrets, configured(private, channelPrivateKeys[c.Channel])...)
	return c, nil
}

// SaveChannel creates or updates one channel. The site assignment is not
// changed here; use AssignChannels. A blank secret keeps the stored secret.
func (s *Store) SaveChannel(ctx context.Context, c Channel, secret string, create bool) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.saveChannel(ctx, tx, c, secret, create); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validateChannel(c Channel) error {
	if !validID(c.Key) || !channels[c.Channel] || !validID(c.Container) || len(c.Label) > 200 {
		return errors.New("invalid channel")
	}
	for key := range c.Config {
		if !channelPublicKeys[c.Channel][key] && !contains(channelPrivateKeys[c.Channel], key) {
			return errors.New("unsupported channel configuration field")
		}
	}
	return nil
}

func (s *Store) saveChannel(ctx context.Context, tx pgx.Tx, c Channel, secret string, create bool) error {
	if err := validateChannel(c); err != nil {
		return err
	}
	var container, site, address, label string
	var enabled bool
	var oldCipher, oldNonce []byte
	err := tx.QueryRow(ctx, `SELECT container_id,coalesce(site_id,''),coalesce(address,''),label,enabled,config_ciphertext,config_nonce
 FROM relay_channel_credentials WHERE channel=$1 AND channel_key=$2 FOR UPDATE`, c.Channel, c.Key).Scan(&container, &site, &address, &label, &enabled, &oldCipher, &oldNonce)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if exists && create {
		return errors.New("channel already exists")
	}
	if !exists {
		if err = uniqueKey(ctx, tx, c); err != nil {
			return err
		}
	}
	if exists && container != c.Container {
		return ErrConflict
	}
	previous, err := s.configSecrets(oldCipher, oldNonce)
	if err != nil {
		return err
	}
	private, err := mergeSecrets(previous, c.Config, channelPrivateKeys[c.Channel])
	if err != nil {
		return err
	}
	public := map[string]any{}
	for key, value := range c.Config {
		if channelPublicKeys[c.Channel][key] {
			public[key] = value
		}
	}
	nextAddress := channelAddress(c.Channel, public, secret, address)
	if c.Label == "" && exists {
		c.Label = label
	} else if c.Label == "" {
		c.Label = c.Key
	}
	if err = s.writeChannel(ctx, tx, c, public, private, secret, nextAddress); err != nil {
		return err
	}
	if exists && site != "" && (label != c.Label || enabled != c.Enabled || address != nextAddress) {
		return notifyChannels(ctx, tx, site)
	}
	return nil
}

func (s *Store) writeChannel(ctx context.Context, tx pgx.Tx, c Channel, public map[string]any, private map[string]string, secret, address string) error {
	configCipher, configNonce, err := s.boxMap(private)
	if err != nil {
		return err
	}
	secretCipher, secretNonce, err := s.boxOptional(secret)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(public)
	if err != nil {
		return errors.New("invalid channel configuration")
	}
	_, err = tx.Exec(ctx, `INSERT INTO relay_channel_credentials(channel,channel_key,container_id,label,address,enabled,config,secret_ciphertext,secret_nonce,config_ciphertext,config_nonce)
 VALUES($1,$2,$3,$4,nullif($5,''),$6,$7,$8,$9,$10,$11) ON CONFLICT(channel,channel_key) DO UPDATE SET label=excluded.label,address=excluded.address,
 enabled=excluded.enabled,config=excluded.config,
 secret_ciphertext=CASE WHEN length(excluded.secret_ciphertext)>0 THEN excluded.secret_ciphertext ELSE relay_channel_credentials.secret_ciphertext END,
 secret_nonce=CASE WHEN length(excluded.secret_nonce)>0 THEN excluded.secret_nonce ELSE relay_channel_credentials.secret_nonce END,
 config_ciphertext=excluded.config_ciphertext,config_nonce=excluded.config_nonce,updated_at=now()`,
		c.Channel, c.Key, c.Container, c.Label, address, c.Enabled, raw, secretCipher, secretNonce, configCipher, configNonce)
	if isUniqueViolation(err, "relay_channel_credentials_address") {
		return errors.New("this number, bot or mailbox is already configured in another channel")
	}
	return err
}

// channelAddress is the provider identity: WhatsApp phone_id, Telegram bot id or mailbox address.
func channelAddress(channel string, public map[string]any, secret, current string) string {
	switch channel {
	case "WhatsApp":
		value, _ := public["phone_id"].(string)
		return strings.TrimSpace(value)
	case "Email":
		value, _ := public["from"].(string)
		return strings.ToLower(strings.TrimSpace(value))
	}
	if match := telegramBotID.FindStringSubmatch(secret); match != nil {
		return match[1]
	}
	return current
}

// notifyChannels journals one relay_channels event for a site, so Core re-reads its channel list.
func notifyChannels(ctx context.Context, tx pgx.Tx, site string) error {
	_, err := tx.Exec(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id)
 VALUES('', 'relay_channels:' || $1 || ':' || txid_current(), '{"object":"relay_channels","changed":true}'::jsonb, 'realtime', $1)
 ON CONFLICT(account,digest) DO NOTHING`, site)
	return err
}

// uniqueKey keeps new keys unique across providers, because outbox, rate and
// lock tables are keyed by the channel key alone. Only the legacy shape, where
// the key equals its container id, may share one key across providers.
func uniqueKey(ctx context.Context, tx pgx.Tx, c Channel) error {
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_channel_credentials WHERE channel_key=$1 AND channel<>$2)`, c.Key, c.Channel).Scan(&used); err != nil {
		return err
	}
	if used && c.Key != c.Container {
		return errors.New("channel key is already used by another channel")
	}
	return nil
}
