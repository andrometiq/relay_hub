package containers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Container groups provider apps and their channels. It is not tied to a site.
type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// WebhookPath is the random public WhatsApp callback path of the container.
	WebhookPath string `json:"webhook_path"`
	// OldAddress means the container also answers on its readable key path.
	OldAddress bool      `json:"old_address"`
	WhatsApp   Provider  `json:"whatsapp"`
	Channels   []Channel `json:"channels"`
}

// Provider is the shared WhatsApp app of a container: app_id, graph_version,
// write-only app_secret and verify_token, and an optional system-user token.
type Provider struct {
	Config            map[string]any `json:"config"`
	ConfiguredSecrets []string       `json:"configured_secrets"`
	LastEventAt       *time.Time     `json:"last_event_at"`
}

// ProviderInput edits a container's WhatsApp app. Blank secrets keep stored values.
type ProviderInput struct {
	Config      map[string]any `json:"config"`
	AccessToken string         `json:"access_token,omitempty"`
}

func (s *Store) ListContainers(ctx context.Context, query, after string, limit int) ([]Container, string, error) {
	if limit < 1 || limit > 100 || len(query) > 80 || (after != "" && !validID(after)) {
		return nil, "", errors.New("invalid container page")
	}
	rows, err := s.DB.Query(ctx, `SELECT container_id FROM relay_containers
 WHERE ($1='' OR container_id ILIKE $2 OR name ILIKE $2) AND ($3='' OR container_id>$3)
 ORDER BY container_id LIMIT $4`, strings.TrimSpace(query), likePattern(query), after, limit+1)
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
	result := make([]Container, 0, len(ids))
	for _, id := range ids {
		container, err := s.GetContainer(ctx, id)
		if err != nil {
			return nil, "", err
		}
		result = append(result, container)
	}
	return result, next, nil
}

func (s *Store) GetContainer(ctx context.Context, id string) (Container, error) {
	c := Container{ID: id, WhatsApp: Provider{Config: map[string]any{}, ConfiguredSecrets: []string{}}}
	var public string
	if err := s.DB.QueryRow(ctx, `SELECT name,enabled,public_id,legacy_path FROM relay_containers WHERE container_id=$1`, id).Scan(&c.Name, &c.Enabled, &public, &c.OldAddress); err != nil {
		return c, err
	}
	c.WebhookPath = "/webhooks/whatsapp/" + public
	var raw, cipher, nonce, token []byte
	err := s.DB.QueryRow(ctx, `SELECT config,config_ciphertext,config_nonce,secret_ciphertext FROM relay_container_providers WHERE container_id=$1 AND provider='WhatsApp'`, id).Scan(&raw, &cipher, &nonce, &token)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	if err == nil {
		if json.Unmarshal(raw, &c.WhatsApp.Config) != nil {
			return c, errors.New("invalid container configuration")
		}
		private, err := s.configSecrets(cipher, nonce)
		if err != nil {
			return c, err
		}
		c.WhatsApp.ConfiguredSecrets = configured(private, providerPrivateKeys)
		if len(token) > 0 {
			c.WhatsApp.ConfiguredSecrets = append(c.WhatsApp.ConfiguredSecrets, "access_token")
		}
	}
	if err = s.DB.QueryRow(ctx, `SELECT max(created_at) FROM (SELECT created_at FROM relay_whatsapp_events WHERE container_id=$1 ORDER BY id DESC LIMIT 1) latest`, id).Scan(&c.WhatsApp.LastEventAt); err != nil {
		return c, err
	}
	c.Channels, err = s.Channels(ctx, ChannelFilter{Container: id})
	return c, err
}

// SaveContainer creates or updates a container and, when given, its WhatsApp app.
func (s *Store) SaveContainer(ctx context.Context, c Container, whatsapp *ProviderInput, create bool) error {
	if create && c.ID == "" {
		c.ID = NewContainerID()
	}
	if !validID(c.ID) || len(c.Name) > 200 {
		return errors.New("invalid container")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = upsertContainer(ctx, tx, c, create); err != nil {
		return err
	}
	if whatsapp != nil {
		if err = s.saveProvider(ctx, tx, c.ID, whatsapp.Config, whatsapp.AccessToken); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func upsertContainer(ctx context.Context, tx pgx.Tx, c Container, create bool) error {
	name := c.Name
	if name == "" {
		name = c.ID
	}
	result, err := tx.Exec(ctx, `INSERT INTO relay_containers(container_id,name,enabled) VALUES($1,$2,$3)
 ON CONFLICT(container_id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,updated_at=now() WHERE NOT $4`, c.ID, name, c.Enabled, create)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("container already exists")
	}
	return nil
}

// saveProvider merges WhatsApp app edits. Blank secret fields keep stored values.
func (s *Store) saveProvider(ctx context.Context, tx pgx.Tx, container string, edits map[string]any, token string) error {
	public := map[string]any{}
	for key, value := range edits {
		if !providerPublicKeys[key] && !contains(providerPrivateKeys, key) {
			return errors.New("unsupported WhatsApp app field")
		}
		if providerPublicKeys[key] {
			public[key] = value
		}
	}
	var oldConfig, oldCipher, oldNonce []byte
	err := tx.QueryRow(ctx, `SELECT config,config_ciphertext,config_nonce FROM relay_container_providers WHERE container_id=$1 AND provider='WhatsApp' FOR UPDATE`, container).Scan(&oldConfig, &oldCipher, &oldNonce)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	stored := map[string]any{}
	if len(oldConfig) > 0 && json.Unmarshal(oldConfig, &stored) != nil {
		return errors.New("invalid stored configuration")
	}
	for key, value := range public {
		stored[key] = value
	}
	previous, err := s.configSecrets(oldCipher, oldNonce)
	if err != nil {
		return err
	}
	private, err := mergeSecrets(previous, edits, providerPrivateKeys)
	if err != nil {
		return err
	}
	configCipher, configNonce, err := s.boxMap(private)
	if err != nil {
		return err
	}
	tokenCipher, tokenNonce, err := s.boxOptional(token)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return errors.New("invalid WhatsApp app configuration")
	}
	_, err = tx.Exec(ctx, `INSERT INTO relay_container_providers(container_id,provider,config,config_ciphertext,config_nonce,secret_ciphertext,secret_nonce)
 VALUES($1,'WhatsApp',$2,$3,$4,$5,$6) ON CONFLICT(container_id,provider) DO UPDATE SET config=excluded.config,
 config_ciphertext=excluded.config_ciphertext,config_nonce=excluded.config_nonce,
 secret_ciphertext=CASE WHEN length(excluded.secret_ciphertext)>0 THEN excluded.secret_ciphertext ELSE relay_container_providers.secret_ciphertext END,
 secret_nonce=CASE WHEN length(excluded.secret_nonce)>0 THEN excluded.secret_nonce ELSE relay_container_providers.secret_nonce END,updated_at=now()`,
		container, raw, configCipher, configNonce, tokenCipher, tokenNonce)
	return err
}

// ProviderCredential returns the decrypted WhatsApp app of a container and its
// system-user token, for validating a new number before it is stored.
func (s *Store) ProviderCredential(ctx context.Context, container string) (map[string]any, string, error) {
	var raw, cipher, nonce, token, tokenNonce []byte
	err := s.DB.QueryRow(ctx, `SELECT config,config_ciphertext,config_nonce,secret_ciphertext,secret_nonce FROM relay_container_providers WHERE container_id=$1 AND provider='WhatsApp'`, container).Scan(&raw, &cipher, &nonce, &token, &tokenNonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	config := map[string]any{}
	if json.Unmarshal(raw, &config) != nil {
		return nil, "", errors.New("invalid container configuration")
	}
	private, err := s.configSecrets(cipher, nonce)
	if err != nil {
		return nil, "", err
	}
	for key, value := range private {
		config[key] = value
	}
	secret := ""
	if len(token) > 0 {
		if secret, err = s.unbox(token, tokenNonce); err != nil {
			return nil, "", errors.New("stored credential cannot be decrypted")
		}
	}
	return config, secret, nil
}

// NewContainerID returns a random internal container key, for a container created by name only.
func NewContainerID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return "ct_" + hex.EncodeToString(raw)
}

// TurnOffOldAddress stops the container's WhatsApp webhook, or one bot's or
// mailbox's webhook, from answering on its readable key path.
func (s *Store) TurnOffOldAddress(ctx context.Context, container string, channel *ChannelRef) error {
	var tag pgconn.CommandTag
	var err error
	if channel == nil {
		tag, err = s.DB.Exec(ctx, `UPDATE relay_containers SET legacy_path=false,updated_at=now() WHERE container_id=$1`, container)
	} else {
		tag, err = s.DB.Exec(ctx, `UPDATE relay_channel_credentials SET legacy_path=false,updated_at=now() WHERE container_id=$1 AND channel=$2 AND channel_key=$3`, container, channel.Channel, channel.Key)
	}
	if err == nil && tag.RowsAffected() != 1 {
		err = pgx.ErrNoRows
	}
	return err
}
