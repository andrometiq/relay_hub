package accounts

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"regexp"
	"strings"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var channels = map[string]bool{"WhatsApp": true, "Telegram": true, "Email": true}

type Store struct {
	DB  *pgxpool.Pool
	Key []byte
}
type Account struct {
	ID       string    `json:"id"`
	Site     string    `json:"site"`
	Enabled  bool      `json:"enabled"`
	Channels []Channel `json:"channels"`
}
type Channel struct {
	Channel           string         `json:"channel"`
	Enabled           bool           `json:"enabled"`
	Config            map[string]any `json:"config"`
	ConfiguredSecrets []string       `json:"configured_secrets,omitempty"`
}
type RuntimeCredential struct {
	AccountID string
	Site      string
	Enabled   bool
	Channel   string
	Config    map[string]any
	Secret    string
}

func New(db *pgxpool.Pool, key string) (*Store, error) {
	h := sha256.Sum256([]byte(key))
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("RELAY_CREDENTIAL_KEY is required")
	}
	return &Store{DB: db, Key: h[:]}, nil
}
func (s *Store) box(secret string) ([]byte, []byte, error) {
	b, e := aes.NewCipher(s.Key)
	if e != nil {
		return nil, nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, nil, e
	}
	c, e := g.Seal(nil, n, []byte(secret), nil), error(nil)
	return c, n, e
}
func (s *Store) unbox(c, n []byte) (string, error) {
	b, e := aes.NewCipher(s.Key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(n) != g.NonceSize() {
		return "", errors.New("invalid credential nonce")
	}
	v, e := g.Open(nil, n, c, nil)
	return string(v), e
}
func validate(id, site string) error {
	if !idRE.MatchString(id) {
		return errors.New("invalid account id")
	}
	u, e := url.Parse(site)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || len(site) > 300 || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("site must be an HTTP or HTTPS origin")
	}
	return nil
}
func (s *Store) List(ctx context.Context) ([]Account, error) {
	rows, e := s.DB.Query(ctx, `SELECT a.account_id,a.site,a.enabled,c.channel,c.enabled,c.config,c.config_ciphertext,c.config_nonce,COALESCE(length(c.secret_ciphertext),0)>0 FROM relay_accounts a LEFT JOIN relay_channel_credentials c ON c.account_id=a.account_id ORDER BY a.id,c.channel`)
	if e != nil {
		return nil, e
	}
	return s.scanAccounts(rows)
}

func (s *Store) scanAccounts(rows pgx.Rows) ([]Account, error) {
	var e error
	defer rows.Close()
	out := []Account{}
	by := map[string]int{}
	for rows.Next() {
		var id, site string
		var enabled bool
		var ch *string
		var ce *bool
		var cfg []byte
		var encrypted, nonce []byte
		var mainSecret bool
		if e = rows.Scan(&id, &site, &enabled, &ch, &ce, &cfg, &encrypted, &nonce, &mainSecret); e != nil {
			return nil, e
		}
		index, exists := by[id]
		if !exists {
			out = append(out, Account{ID: id, Site: site, Enabled: enabled, Channels: []Channel{}})
			index = len(out) - 1
			by[id] = index
		}
		a := &out[index]
		if ch != nil {
			var m map[string]any
			if json.Unmarshal(cfg, &m) != nil {
				return nil, errors.New("invalid channel configuration")
			}
			private, err := s.configSecrets(encrypted, nonce)
			if err != nil {
				return nil, err
			}
			configured := []string{}
			if mainSecret {
				configured = append(configured, "secret")
			}
			for _, key := range sensitiveKeys {
				if value, ok := m[key].(string); ok && value != "" {
					private[key] = value
				}
				delete(m, key)
				if private[key] != "" {
					configured = append(configured, key)
				}
			}
			a.Channels = append(a.Channels, Channel{Channel: *ch, Enabled: *ce, Config: m, ConfiguredSecrets: configured})
		}
	}
	return out, rows.Err()
}

// Page applies a bounded keyset window before joining channel rows.
func (s *Store) Page(ctx context.Context, query, after string, limit int) ([]Account, string, error) {
	if limit < 1 || limit > 100 || len(query) > 80 || (after != "" && !idRE.MatchString(after)) {
		return nil, "", errors.New("invalid account page")
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(query)) + "%"
	rows, err := s.DB.Query(ctx, `WITH page AS (
 SELECT account_id,site,enabled FROM relay_accounts
 WHERE ($1='' OR account_id ILIKE $2 OR site ILIKE $2) AND ($3='' OR account_id>$3)
 ORDER BY account_id LIMIT $4
 ) SELECT a.account_id,a.site,a.enabled,c.channel,c.enabled,c.config,c.config_ciphertext,c.config_nonce,COALESCE(length(c.secret_ciphertext),0)>0
 FROM page a LEFT JOIN relay_channel_credentials c ON c.account_id=a.account_id ORDER BY a.account_id,c.channel`, strings.TrimSpace(query), pattern, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	accounts, err := s.scanAccounts(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(accounts) > limit {
		accounts = accounts[:limit]
		next = accounts[len(accounts)-1].ID
	}
	return accounts, next, nil
}

// PageBefore returns the bounded window immediately before a stable account
// cursor. Results remain in ascending order so virtualized lists can prepend
// them without changing row order.
func (s *Store) PageBefore(ctx context.Context, query, before string, limit int) ([]Account, bool, error) {
	if limit < 1 || limit > 100 || len(query) > 80 || !idRE.MatchString(before) {
		return nil, false, errors.New("invalid account page")
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(query)) + "%"
	rows, err := s.DB.Query(ctx, `WITH preceding AS (
 SELECT account_id,site,enabled FROM relay_accounts
 WHERE ($1='' OR account_id ILIKE $2 OR site ILIKE $2) AND account_id<$3
 ORDER BY account_id DESC LIMIT $4
 ), page AS (
 SELECT account_id,site,enabled FROM preceding ORDER BY account_id
 ) SELECT a.account_id,a.site,a.enabled,c.channel,c.enabled,c.config,c.config_ciphertext,c.config_nonce,COALESCE(length(c.secret_ciphertext),0)>0
 FROM page a LEFT JOIN relay_channel_credentials c ON c.account_id=a.account_id ORDER BY a.account_id,c.channel`,
		strings.TrimSpace(query), pattern, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	accounts, err := s.scanAccounts(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(accounts) > limit
	if more {
		accounts = accounts[len(accounts)-limit:]
	}
	return accounts, more, nil
}

func (s *Store) Summary(ctx context.Context) (int64, int64, error) {
	var total, enabled int64
	err := s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM relay_accounts),
 (SELECT count(*) FROM relay_channel_credentials c JOIN relay_accounts a ON a.account_id=c.account_id WHERE c.enabled AND a.enabled)`).Scan(&total, &enabled)
	return total, enabled, err
}

func (s *Store) Save(ctx context.Context, a Account, secrets map[string]string) error {
	return s.save(ctx, a, secrets, false)
}
func (s *Store) Create(ctx context.Context, a Account, secrets map[string]string) error {
	return s.save(ctx, a, secrets, true)
}
func (s *Store) save(ctx context.Context, a Account, secrets map[string]string, createOnly bool) error {
	if e := validate(a.ID, a.Site); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, c := range a.Channels {
		if seen[c.Channel] {
			return errors.New("duplicate channel")
		}
		seen[c.Channel] = true
		if !channels[c.Channel] {
			return errors.New("unsupported channel")
		}
		for key := range c.Config {
			if !sensitive(key) && !publicConfigKeys[c.Channel][key] {
				return errors.New("unsupported channel configuration field")
			}
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	result, e := tx.Exec(ctx, `INSERT INTO relay_accounts(account_id,site,enabled) VALUES($1,$2,$3) ON CONFLICT(account_id) DO UPDATE SET site=excluded.site,enabled=excluded.enabled,updated_at=now() WHERE NOT $4`, a.ID, a.Site, a.Enabled, createOnly)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return errors.New("account already exists")
	}

	for _, c := range a.Channels {
		var oldCipher, oldNonce, oldConfig []byte
		e = tx.QueryRow(ctx, `SELECT config_ciphertext,config_nonce,config FROM relay_channel_credentials WHERE account_id=$1 AND channel=$2 FOR UPDATE`, a.ID, c.Channel).Scan(&oldCipher, &oldNonce, &oldConfig)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		private, err := s.configSecrets(oldCipher, oldNonce)
		if err != nil {
			return err
		}
		if len(oldConfig) > 0 {
			var previous map[string]any
			if json.Unmarshal(oldConfig, &previous) != nil {
				return errors.New("invalid stored configuration")
			}
			for _, key := range sensitiveKeys {
				if value, ok := previous[key].(string); ok && value != "" {
					private[key] = value
				}
			}
		}
		config := map[string]any{}
		for key, value := range c.Config {
			if sensitive(key) {
				v, ok := value.(string)
				if !ok {
					return errors.New("invalid secret field")
				}
				if v != "" {
					private[key] = v
				}
			} else {
				config[key] = value
			}
		}
		privateRaw, err := json.Marshal(private)
		if err != nil {
			return errors.New("invalid credential configuration")
		}
		configCipher, configNonce, err := s.box(string(privateRaw))
		if err != nil {
			return err
		}
		raw, marshalErr := json.Marshal(config)
		if marshalErr != nil {
			return errors.New("invalid channel configuration")
		}
		secret := secrets[c.Channel]
		enc, nonce := []byte{}, []byte{}
		if secret != "" {
			enc, nonce, e = s.box(secret)
			if e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO relay_channel_credentials(account_id,channel,enabled,config,secret_ciphertext,secret_nonce,config_ciphertext,config_nonce) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id,channel) DO UPDATE SET enabled=excluded.enabled,config=excluded.config,secret_ciphertext=CASE WHEN length(excluded.secret_ciphertext)>0 THEN excluded.secret_ciphertext ELSE relay_channel_credentials.secret_ciphertext END,secret_nonce=CASE WHEN length(excluded.secret_nonce)>0 THEN excluded.secret_nonce ELSE relay_channel_credentials.secret_nonce END,config_ciphertext=excluded.config_ciphertext,config_nonce=excluded.config_nonce,updated_at=now()`, a.ID, c.Channel, c.Enabled, raw, enc, nonce, configCipher, configNonce)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func SecretString(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
func (s *Store) ValidateSecret(ctx context.Context, id, ch string) error {
	var c, n []byte
	e := s.DB.QueryRow(ctx, `SELECT secret_ciphertext,secret_nonce FROM relay_channel_credentials WHERE account_id=$1 AND channel=$2`, id, ch).Scan(&c, &n)
	if e != nil {
		return e
	}
	_, e = s.unbox(c, n)
	return e
}

// Runtime returns decrypted credentials only to the in-process provider
// loader. It is deliberately separate from List, whose result is safe for UI
// responses and never contains secret material.
func (s *Store) Runtime(ctx context.Context) ([]RuntimeCredential, error) {
	rows, err := s.DB.Query(ctx, `SELECT a.account_id,a.site,a.enabled,c.channel,c.config,c.secret_ciphertext,c.secret_nonce,c.config_ciphertext,c.config_nonce FROM relay_accounts a JOIN relay_channel_credentials c ON c.account_id=a.account_id WHERE a.enabled AND c.enabled ORDER BY a.id,c.channel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RuntimeCredential{}
	for rows.Next() {
		var v RuntimeCredential
		var raw []byte
		var ciphertext, nonce []byte
		var configCipher, configNonce []byte
		if err = rows.Scan(&v.AccountID, &v.Site, &v.Enabled, &v.Channel, &raw, &ciphertext, &nonce, &configCipher, &configNonce); err != nil {
			return nil, err
		}
		if len(raw) > 0 && json.Unmarshal(raw, &v.Config) != nil {
			return nil, errors.New("invalid channel configuration")
		}
		private, e := s.configSecrets(configCipher, configNonce)
		if e != nil {
			return nil, e
		}
		if v.Config == nil {
			v.Config = map[string]any{}
		}
		for k, value := range private {
			v.Config[k] = value
		}
		if len(ciphertext) > 0 {
			v.Secret, err = s.unbox(ciphertext, nonce)
			if err != nil {
				return nil, errors.New("stored credential cannot be decrypted")
			}
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// Credential returns one decrypted provider configuration for an in-process
// validation/reload path. It is never serialized by an HTTP handler.
func (s *Store) Credential(ctx context.Context, accountID, channel string) (RuntimeCredential, error) {
	var value RuntimeCredential
	var raw, ciphertext, nonce, configCipher, configNonce []byte
	err := s.DB.QueryRow(ctx, `SELECT a.account_id,a.site,a.enabled,c.channel,c.config,c.secret_ciphertext,c.secret_nonce,c.config_ciphertext,c.config_nonce
		FROM relay_accounts a JOIN relay_channel_credentials c ON c.account_id=a.account_id
		WHERE a.account_id=$1 AND c.channel=$2`, accountID, channel).Scan(&value.AccountID, &value.Site, &value.Enabled, &value.Channel, &raw, &ciphertext, &nonce, &configCipher, &configNonce)
	if err != nil {
		return value, err
	}
	if len(raw) > 0 && json.Unmarshal(raw, &value.Config) != nil {
		return value, errors.New("invalid channel configuration")
	}
	private, err := s.configSecrets(configCipher, configNonce)
	if err != nil {
		return value, err
	}
	if value.Config == nil {
		value.Config = map[string]any{}
	}
	for key, item := range private {
		value.Config[key] = item
	}
	if len(ciphertext) > 0 {
		value.Secret, err = s.unbox(ciphertext, nonce)
		if err != nil {
			return value, err
		}
	}
	return value, nil
}

var _ = fmt.Sprintf
