package containers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Site is one Frappe site: its origin, Core callback credentials and assigned channels.
// CoreAPIKey is shown so the admin can copy it; the API secret and callback
// secret stay write-only.
type Site struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Origin            string     `json:"origin"`
	Enabled           bool       `json:"enabled"`
	ConfiguredSecrets []string   `json:"configured_secrets"`
	CoreAPIKey        string     `json:"core_api_key,omitempty"`
	LastCallbackAt    *time.Time `json:"last_callback_at"`
	ActiveTokens      int        `json:"active_tokens"`
	Channels          []Channel  `json:"channels"`
}

// CoreCredentials authorize and sign Hub callbacks to one site.
type CoreCredentials struct {
	APIKey, APISecret, CallbackSecret string
}

func (c CoreCredentials) Complete() bool {
	return c.APIKey != "" && c.APISecret != "" && c.CallbackSecret != ""
}

func (s *Store) ListSites(ctx context.Context, query, after string, limit int) ([]Site, string, error) {
	if limit < 1 || limit > 100 || len(query) > 80 || (after != "" && !validID(after)) {
		return nil, "", errors.New("invalid site page")
	}
	rows, err := s.DB.Query(ctx, `SELECT site_id FROM relay_sites
 WHERE ($1='' OR site_id ILIKE $2 OR origin ILIKE $2 OR name ILIKE $2) AND ($3='' OR site_id>$3)
 ORDER BY site_id LIMIT $4`, strings.TrimSpace(query), likePattern(query), after, limit+1)
	if err != nil {
		return nil, "", err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}
	result := make([]Site, 0, len(ids))
	for _, id := range ids {
		site, err := s.GetSite(ctx, id)
		if err != nil {
			return nil, "", err
		}
		result = append(result, site)
	}
	return result, next, nil
}

func (s *Store) GetSite(ctx context.Context, id string) (Site, error) {
	site := Site{ID: id}
	var cipher, nonce []byte
	if err := s.DB.QueryRow(ctx, `SELECT name,origin,enabled,secret_ciphertext,secret_nonce,last_callback_at,
 (SELECT count(*) FROM relay_api_tokens t WHERE t.site_id=$1 AND t.revoked_at IS NULL AND t.expires_at>now())
 FROM relay_sites WHERE site_id=$1`, id).Scan(&site.Name, &site.Origin, &site.Enabled, &cipher, &nonce, &site.LastCallbackAt, &site.ActiveTokens); err != nil {
		return site, err
	}
	private, err := s.configSecrets(cipher, nonce)
	if err != nil {
		return site, err
	}
	site.ConfiguredSecrets = configured(private, siteSecretKeys)
	site.CoreAPIKey = private["core_api_key"]
	site.Channels, err = s.Channels(ctx, ChannelFilter{Site: id})
	return site, err
}

// SiteCredentials returns the decrypted Core credentials for in-process use only.
func (s *Store) SiteCredentials(ctx context.Context, id string) (CoreCredentials, error) {
	var cipher, nonce []byte
	if err := s.DB.QueryRow(ctx, `SELECT secret_ciphertext,secret_nonce FROM relay_sites WHERE site_id=$1`, id).Scan(&cipher, &nonce); err != nil {
		return CoreCredentials{}, err
	}
	private, err := s.configSecrets(cipher, nonce)
	if err != nil {
		return CoreCredentials{}, err
	}
	return CoreCredentials{APIKey: private["core_api_key"], APISecret: private["core_api_secret"], CallbackSecret: private["callback_secret"]}, nil
}

// SaveSite creates or updates a site. Secrets holds write-only core_api_key,
// core_api_secret and callback_secret; blank values keep stored values.
func (s *Store) SaveSite(ctx context.Context, site Site, secrets map[string]any, create bool) error {
	if err := validate(site.ID, site.Origin); err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.saveSite(ctx, tx, site, secrets, create); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) saveSite(ctx context.Context, tx pgx.Tx, site Site, secrets map[string]any, create bool) error {
	for key := range secrets {
		if !contains(siteSecretKeys, key) {
			return errors.New("unsupported site credential field")
		}
	}
	var cipher, nonce []byte
	err := tx.QueryRow(ctx, `SELECT secret_ciphertext,secret_nonce FROM relay_sites WHERE site_id=$1 FOR UPDATE`, site.ID).Scan(&cipher, &nonce)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && create {
		return errors.New("site already exists")
	}
	previous, err := s.configSecrets(cipher, nonce)
	if err != nil {
		return err
	}
	private, err := mergeSecrets(previous, secrets, siteSecretKeys)
	if err != nil {
		return err
	}
	if err = validateCore(private); err != nil {
		return err
	}
	cipher, nonce, err = s.boxMap(private)
	if err != nil {
		return err
	}
	if len(site.Name) > 200 {
		return errors.New("site name is too long")
	}
	_, err = tx.Exec(ctx, `INSERT INTO relay_sites(site_id,name,origin,enabled,secret_ciphertext,secret_nonce) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(site_id) DO UPDATE SET name=excluded.name,origin=excluded.origin,enabled=excluded.enabled,secret_ciphertext=excluded.secret_ciphertext,
 secret_nonce=excluded.secret_nonce,updated_at=now()`, site.ID, site.Name, site.Origin, site.Enabled, cipher, nonce)
	return err
}

func validateCore(private map[string]string) error {
	count := len(configured(private, siteSecretKeys))
	if count != 0 && count != len(siteSecretKeys) {
		return errors.New("complete all three Core delivery credentials")
	}
	if value := private["callback_secret"]; value != "" && len(value) < 32 {
		return errors.New("callback signing secret must have at least 32 characters")
	}
	if strings.ContainsAny(private["core_api_key"]+private["core_api_secret"]+private["callback_secret"], "\r\n\x00") {
		return errors.New("invalid Core delivery credentials")
	}
	return nil
}

// AssignChannels sets the full list of channels a site uses. A channel that
// another site uses is refused; channels left out are unassigned. The site
// receives a relay_channels event when its list changes.
func (s *Store) AssignChannels(ctx context.Context, site string, refs []ChannelRef) error {
	if !validID(site) || len(refs) > 500 {
		return errors.New("invalid assignment")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_sites WHERE site_id=$1 FOR UPDATE)`, site).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	providers, keys := make([]string, len(refs)), make([]string, len(refs))
	for i, ref := range refs {
		if !channels[ref.Channel] || !validID(ref.Key) {
			return errors.New("invalid channel")
		}
		providers[i], keys[i] = ref.Channel, ref.Key
	}
	changed, err := assign(ctx, tx, site, providers, keys)
	if err != nil {
		return err
	}
	if changed {
		if err = notifyChannels(ctx, tx, site); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func assign(ctx context.Context, tx pgx.Tx, site string, providers, keys []string) (bool, error) {
	// Lock every channel the change touches, so parallel assignments serialize.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM relay_channel_credentials c WHERE c.site_id=$1
 OR (c.channel,c.channel_key) IN (SELECT * FROM unnest($2::text[],$3::text[])) ORDER BY c.id FOR UPDATE`, site, providers, keys); err != nil {
		return false, err
	}
	var found, foreign int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE c.site_id IS NOT NULL AND c.site_id<>$1)
 FROM relay_channel_credentials c JOIN unnest($2::text[],$3::text[]) r(channel,channel_key) USING(channel,channel_key)`, site, providers, keys).Scan(&found, &foreign); err != nil {
		return false, err
	}
	if err := pendingSends(ctx, tx, site, providers, keys); err != nil {
		return false, err
	}
	if foreign > 0 {
		return false, ErrConflict
	}
	if found != len(distinctRefs(providers, keys)) {
		return false, errors.New("unknown channel")
	}
	added, err := tx.Exec(ctx, `UPDATE relay_channel_credentials c SET site_id=$1,assigned_at=now(),updated_at=now()
 FROM unnest($2::text[],$3::text[]) r(channel,channel_key) WHERE c.channel=r.channel AND c.channel_key=r.channel_key AND c.site_id IS NULL`, site, providers, keys)
	if err != nil {
		return false, err
	}
	removed, err := tx.Exec(ctx, `UPDATE relay_channel_credentials c SET site_id=NULL,assigned_at=NULL,updated_at=now()
 WHERE c.site_id=$1 AND NOT EXISTS(SELECT 1 FROM unnest($2::text[],$3::text[]) r(channel,channel_key) WHERE r.channel=c.channel AND r.channel_key=c.channel_key)`, site, providers, keys)
	if err != nil {
		return false, err
	}
	return added.RowsAffected()+removed.RowsAffected() > 0, nil
}

func distinctRefs(providers, keys []string) map[string]bool {
	seen := map[string]bool{}
	for i := range providers {
		seen[providers[i]+"\x00"+keys[i]] = true
	}
	return seen
}

// ErrPendingSends means a channel leaving a site still has sends that site queued.
var ErrPendingSends = errors.New("channel has queued or unresolved sends from this site; cancel or resolve them before unassigning")

// pendingSends refuses to unassign a channel while the site's sends on it are
// open, so the outbox never sends for a site that no longer owns the channel.
func pendingSends(ctx context.Context, tx pgx.Tx, site string, providers, keys []string) error {
	var pending bool
	err := tx.QueryRow(ctx, `WITH leaving AS (
 SELECT c.channel,c.channel_key FROM relay_channel_credentials c WHERE c.site_id=$1
 AND NOT EXISTS(SELECT 1 FROM unnest($2::text[],$3::text[]) r(channel,channel_key) WHERE r.channel=c.channel AND r.channel_key=c.channel_key))
 SELECT EXISTS(SELECT 1 FROM leaving l JOIN relay_outbox o ON l.channel='WhatsApp' AND o.account=l.channel_key AND o.site_id=$1
  WHERE o.state IN ('queued','submitting','uncertain'))
 OR EXISTS(SELECT 1 FROM leaving l JOIN relay_bulk_pending b ON l.channel='WhatsApp' AND b.account=l.channel_key
  JOIN relay_outbox o ON o.id=b.outbox_id AND o.site_id=$1)
 OR EXISTS(SELECT 1 FROM leaving l JOIN relay_channel_outbox o ON l.channel IN ('Telegram','Email') AND o.account=l.channel_key AND o.site_id=$1
  AND o.operation=CASE l.channel WHEN 'Telegram' THEN 'telegram.message' ELSE 'email.send' END
  WHERE o.state IN ('queued','dispatching','uncertain'))`, site, providers, keys).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return ErrPendingSends
	}
	return nil
}
