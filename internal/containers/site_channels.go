package containers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrChannelInUse means a bot or mailbox is already a live channel, of this site or another.
var ErrChannelInUse = errors.New("this bot or mailbox is already connected")

// removedStatus is the public state a channel keeps after it leaves a site.
var removedStatus = map[string]string{
	"WhatsApp": `{"partner_status":"removed","connection_status":"disabled"}`,
	"Telegram": `{"webhook_status":"removed"}`,
	"Email":    `{"connection_status":"disabled"}`,
}

// keyPrefix starts the random key of a bot or mailbox that a site connects.
var keyPrefix = map[string]string{"Telegram": "tg-", "Email": "mail-"}

// SaveSiteChannel stores a Telegram bot or mailbox that a site connects
// itself, in the signup container, and links it to the site in one
// transaction. It returns the channel key. The bot id or From address is the
// identity: a removed channel with that identity is reused, so its key and
// history stay; a live one, of any site, is ErrChannelInUse.
func (s *Store) SaveSiteChannel(ctx context.Context, site string, c Channel, secret string) (string, error) {
	if keyPrefix[c.Channel] == "" {
		return "", errors.New("site channels are Telegram bots or mailboxes")
	}
	address := channelAddress(c.Channel, c.Config, secret, "")
	if address == "" {
		return "", errors.New("the bot or mailbox has no identity")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT container_id FROM relay_containers WHERE signup AND enabled`).Scan(&c.Container); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSignupContainer
	} else if err != nil {
		return "", err
	}
	if c.Key, err = siteChannelKey(ctx, tx, c.Channel, address, c.Container); err != nil {
		return "", err
	}
	c.Enabled = true
	c.Config["site_managed"] = true
	if err = s.saveChannel(ctx, tx, c, secret, false); err != nil {
		return "", err
	}
	if err = linkChannel(ctx, tx, site, ChannelRef{Channel: c.Channel, Key: c.Key}); err != nil {
		return "", err
	}
	return c.Key, tx.Commit(ctx)
}

// siteChannelKey is the key of a removed channel with the address, moved into
// container, or a new random key when the address is new.
func siteChannelKey(ctx context.Context, tx pgx.Tx, channel, address, container string) (string, error) {
	var key, owner string
	var live bool
	err := tx.QueryRow(ctx, `SELECT channel_key,coalesce(site_id,''),length(secret_ciphertext)>0 FROM relay_channel_credentials
 WHERE channel=$1 AND address=$2 FOR UPDATE`, channel, address).Scan(&key, &owner, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		random := make([]byte, 8)
		if _, err = rand.Read(random); err != nil {
			return "", err
		}
		return keyPrefix[channel] + hex.EncodeToString(random), nil
	}
	if err != nil {
		return "", err
	}
	if owner != "" || live {
		return "", ErrChannelInUse
	}
	_, err = tx.Exec(ctx, `UPDATE relay_channel_credentials SET container_id=$3,updated_at=now() WHERE channel=$1 AND channel_key=$2`, channel, key, container)
	return key, err
}

// UpdateSiteChannel saves the settings of one channel of site. c carries the
// whole public configuration; a blank secret keeps the stored one. A channel
// of another site is pgx.ErrNoRows.
func (s *Store) UpdateSiteChannel(ctx context.Context, site string, c Channel, secret string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT container_id FROM relay_channel_credentials WHERE channel=$1 AND channel_key=$2 AND site_id=$3 FOR UPDATE`,
		c.Channel, c.Key, site).Scan(&c.Container); err != nil {
		return err
	}
	if err = s.saveChannel(ctx, tx, c, secret, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateSiteValues writes public settings or check results of one channel of
// site and tells the site. A nil enabled keeps the flag. A channel of another
// site is pgx.ErrNoRows.
func (s *Store) UpdateSiteValues(ctx context.Context, site string, ref ChannelRef, enabled *bool, values map[string]any) error {
	for name := range values {
		if !channelPublicKeys[ref.Channel][name] {
			return errors.New("unsupported channel field")
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
	tag, err := tx.Exec(ctx, `UPDATE relay_channel_credentials SET enabled=coalesce($4,enabled),config=config||$5::jsonb,updated_at=now()
 WHERE channel=$2 AND channel_key=$3 AND site_id=$1`, site, ref.Channel, ref.Key, enabled, raw)
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

// RemoveSiteChannel takes one channel away from site. Its token, password and
// other private values are wiped, it is turned off and unlinked, and the site
// gets a relay_channels event. The row and its address stay for history, the
// outbox and a later connect. It refuses while the site has open sends on it.
func (s *Store) RemoveSiteChannel(ctx context.Context, site string, ref ChannelRef) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT channel_key FROM relay_channel_credentials WHERE channel=$1 AND channel_key=$2 AND site_id=$3 FOR UPDATE`,
		ref.Channel, ref.Key, site).Scan(&ref.Key); err != nil {
		return err
	}
	if err = channelLeaving(ctx, tx, site, ref); err != nil {
		return err
	}
	cipher, nonce, err := s.boxMap(map[string]string{})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_channel_credentials SET enabled=false,site_id=NULL,assigned_at=NULL,
 secret_ciphertext=''::bytea,secret_nonce=''::bytea,config_ciphertext=$3,config_nonce=$4,
 config=(config-'register_attempts')||$5::jsonb,updated_at=now()
 WHERE channel=$1 AND channel_key=$2`, ref.Channel, ref.Key, cipher, nonce, removedStatus[ref.Channel]); err != nil {
		return err
	}
	if err = notifyChannels(ctx, tx, site); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// channelLeaving refuses to take a channel from site while the site's sends on it are open.
func channelLeaving(ctx context.Context, tx pgx.Tx, site string, leaving ChannelRef) error {
	rows, err := tx.Query(ctx, `SELECT channel,channel_key FROM relay_channel_credentials WHERE site_id=$1 AND NOT (channel=$2 AND channel_key=$3)`, site, leaving.Channel, leaving.Key)
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

// AllowSiteAttempt records one try of a site tool and refuses it when the
// site made limit tries of action within window. Tries are rows of
// relay_operational_audit, so they also show who did what.
func (s *Store) AllowSiteAttempt(ctx context.Context, site, channel, action string, window time.Duration, limit int) (bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('relay_site_attempt:' || $1 || ':' || $2, 0))`, site, action); err != nil {
		return false, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM relay_operational_audit WHERE actor_type='api_token' AND account_id=$1 AND action=$2
 AND outcome='accepted' AND occurred_at>now()-make_interval(secs=>$3)`, site, action, window.Seconds()).Scan(&count); err != nil {
		return false, err
	}
	if count >= limit {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,account_id,channel,action,outcome) VALUES('api_token',$1,$2,$3,'accepted')`, site, channel, action); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// AuditSite records the outcome of a site tool on one of its channels.
func (s *Store) AuditSite(ctx context.Context, site string, ref ChannelRef, action, outcome, code string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,account_id,channel,action,outcome,error_code,metadata)
 VALUES('api_token',$1,$2,$3,$4,$5,jsonb_build_object('channel_key',$6::text))`, site, ref.Channel, action, outcome, code, ref.Key)
	return err
}
