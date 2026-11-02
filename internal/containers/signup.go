package containers

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNumberInUse means a WhatsApp number is already a channel of another site or container.
var ErrNumberInUse = errors.New("this WhatsApp number is already connected to another site")

// ErrNoSignupContainer means no enabled container hosts self-service signup.
var ErrNoSignupContainer = errors.New("self-service WhatsApp signup is not set up")

// SignupApp is the Meta app of the self-service signup container, for in-process use only.
type SignupApp struct {
	Container, AppID, AppSecret, ConfigID, GraphVersion string
}

// SignupApp returns the enabled signup container and its decrypted Meta app.
func (s *Store) SignupApp(ctx context.Context) (SignupApp, error) {
	var app SignupApp
	err := s.DB.QueryRow(ctx, `SELECT container_id FROM relay_containers WHERE signup AND enabled`).Scan(&app.Container)
	if errors.Is(err, pgx.ErrNoRows) {
		return app, ErrNoSignupContainer
	}
	if err != nil {
		return app, err
	}
	config, _, err := s.ProviderCredential(ctx, app.Container)
	if err != nil {
		return app, err
	}
	text := func(key string) string { value, _ := config[key].(string); return value }
	app.AppID, app.AppSecret, app.ConfigID, app.GraphVersion = text("app_id"), text("app_secret"), text("es_config_id"), text("graph_version")
	if app.AppID == "" || app.AppSecret == "" || app.ConfigID == "" || app.GraphVersion == "" {
		return app, ErrNoSignupContainer
	}
	return app, nil
}

// SaveSignupChannel creates or updates a self-service number and links it to
// site in one transaction. The phone id is the idempotency key: the same site
// updates its channel, another site or container gets ErrNumberInUse.
func (s *Store) SaveSignupChannel(ctx context.Context, site string, c Channel, secret string) error {
	if c.Channel != "WhatsApp" {
		return errors.New("signup channels are WhatsApp numbers")
	}
	phone, _ := c.Config["phone_id"].(string)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT 1 FROM relay_channel_credentials WHERE channel='WhatsApp' AND (address=$1 OR channel_key=$2) ORDER BY id FOR UPDATE`, phone, c.Key); err != nil {
		return err
	}
	var foreign bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_channel_credentials WHERE channel='WhatsApp' AND (address=$1 OR channel_key=$2)
 AND (channel_key<>$2 OR container_id<>$3 OR (site_id IS NOT NULL AND site_id<>$4)))`, phone, c.Key, c.Container, site).Scan(&foreign); err != nil {
		return err
	}
	if foreign {
		return ErrNumberInUse
	}
	if err = s.saveChannel(ctx, tx, c, secret, false); err != nil {
		return err
	}
	if err = linkChannel(ctx, tx, site, ChannelRef{Channel: c.Channel, Key: c.Key}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LinkChannel adds one channel to a site and keeps its other channels. A
// channel of another site is refused. The site gets a relay_channels event.
func (s *Store) LinkChannel(ctx context.Context, site string, ref ChannelRef) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = linkChannel(ctx, tx, site, ref); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func linkChannel(ctx context.Context, tx pgx.Tx, site string, ref ChannelRef) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_sites WHERE site_id=$1 FOR UPDATE)`, site).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT coalesce(site_id,'') FROM relay_channel_credentials WHERE channel=$1 AND channel_key=$2 FOR UPDATE`, ref.Channel, ref.Key).Scan(&owner); err != nil {
		return err
	}
	if owner != "" && owner != site {
		return ErrConflict
	}
	if owner == "" {
		if _, err := tx.Exec(ctx, `UPDATE relay_channel_credentials SET site_id=$1,assigned_at=now(),updated_at=now() WHERE channel=$2 AND channel_key=$3`, site, ref.Channel, ref.Key); err != nil {
			return err
		}
		if _, err := replayHeld(ctx, tx, site, []string{ref.Channel}, []string{ref.Key}); err != nil {
			return err
		}
	}
	return notifyChannels(ctx, tx, site)
}

// DisableWABA turns off every number of a WABA in the signup container after
// Meta removed the app's access, and notifies their sites. It returns the sites.
func (s *Store) DisableWABA(ctx context.Context, waba string) ([]string, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE relay_channel_credentials c SET enabled=false,
 config=c.config||'{"partner_status":"removed","connection_status":"disabled"}'::jsonb,updated_at=now()
 FROM relay_containers k WHERE k.container_id=c.container_id AND k.signup AND c.channel='WhatsApp' AND c.config->>'waba_id'=$1
 RETURNING coalesce(c.site_id,'')`, waba)
	if err != nil {
		return nil, err
	}
	affected, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	sites := []string{}
	seen := map[string]bool{}
	for _, site := range affected {
		if site == "" || seen[site] {
			continue
		}
		seen[site] = true
		sites = append(sites, site)
		if err = notifyChannels(ctx, tx, site); err != nil {
			return nil, err
		}
	}
	return sites, tx.Commit(ctx)
}

// WhatsAppApp is the webhook identity of one enabled container's Meta app,
// for in-process use only. It lets a container without numbers verify and
// receive webhooks.
type WhatsAppApp struct {
	Container, WebhookID, AppSecret, VerifyToken string
}

// WhatsAppApps lists the Meta apps of enabled containers.
func (s *Store) WhatsAppApps(ctx context.Context) ([]WhatsAppApp, error) {
	rows, err := s.DB.Query(ctx, `SELECT k.container_id,k.public_id,p.config_ciphertext,p.config_nonce
 FROM relay_containers k JOIN relay_container_providers p ON p.container_id=k.container_id AND p.provider='WhatsApp'
 WHERE k.enabled ORDER BY k.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []WhatsAppApp{}
	for rows.Next() {
		var app WhatsAppApp
		var cipher, nonce []byte
		if err = rows.Scan(&app.Container, &app.WebhookID, &cipher, &nonce); err != nil {
			return nil, err
		}
		private, err := s.configSecrets(cipher, nonce)
		if err != nil {
			return nil, err
		}
		app.AppSecret, app.VerifyToken = private["app_secret"], private["verify_token"]
		if app.AppSecret != "" {
			result = append(result, app)
		}
	}
	return result, rows.Err()
}

// Seal encrypts a value with the store key, for short-lived secrets kept outside channel rows.
func (s *Store) Seal(secret string) ([]byte, []byte, error) { return s.box(secret) }

// Open decrypts a value sealed with Seal.
func (s *Store) Open(ciphertext, nonce []byte) (string, error) { return s.unbox(ciphertext, nonce) }

// RecordRegisterAttempt counts one Meta register call for a number. It
// refuses the call when limit calls happened within window, because Meta
// blocks the number for 72 hours after too many tries.
func (s *Store) RecordRegisterAttempt(ctx context.Context, key string, now time.Time, window time.Duration, limit int) (bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT coalesce(config->'register_attempts','[]'::jsonb) FROM relay_channel_credentials WHERE channel='WhatsApp' AND channel_key=$1 FOR UPDATE`, key).Scan(&raw); err != nil {
		return false, err
	}
	var stored []time.Time
	if json.Unmarshal(raw, &stored) != nil {
		stored = nil
	}
	recent := []time.Time{}
	for _, at := range stored {
		if now.Sub(at) < window {
			recent = append(recent, at)
		}
	}
	if len(recent) >= limit {
		return false, nil
	}
	recent = append(recent, now.UTC())
	encoded, _ := json.Marshal(recent)
	if _, err = tx.Exec(ctx, `UPDATE relay_channel_credentials SET config=config||jsonb_build_object('register_attempts',$2::jsonb),updated_at=now() WHERE channel='WhatsApp' AND channel_key=$1`, key, encoded); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
