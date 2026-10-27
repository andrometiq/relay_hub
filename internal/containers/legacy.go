package containers

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Account is the simple setup that the current account editor uses: one site
// whose container, site and channel keys all equal the account id. It is a
// view over containers, channels and sites, kept until the container and site
// pages replace the editor.
type Account struct {
	ID       string           `json:"id"`
	Site     string           `json:"site"`
	Enabled  bool             `json:"enabled"`
	Channels []AccountChannel `json:"channels"`
}

type AccountChannel struct {
	Channel           string         `json:"channel"`
	Enabled           bool           `json:"enabled"`
	Config            map[string]any `json:"config"`
	ConfiguredSecrets []string       `json:"configured_secrets,omitempty"`
}

func (s *Store) List(ctx context.Context) ([]Account, error) {
	accounts, _, err := s.Page(ctx, "", "", 100)
	return accounts, err
}

// Page applies a bounded keyset window over sites.
func (s *Store) Page(ctx context.Context, query, after string, limit int) ([]Account, string, error) {
	if limit < 1 || limit > 100 || len(query) > 80 || (after != "" && !validID(after)) {
		return nil, "", errors.New("invalid account page")
	}
	rows, err := s.DB.Query(ctx, `SELECT site_id FROM relay_sites WHERE ($1='' OR site_id ILIKE $2 OR origin ILIKE $2) AND ($3='' OR site_id>$3)
 ORDER BY site_id LIMIT $4`, strings.TrimSpace(query), likePattern(query), after, limit+1)
	if err != nil {
		return nil, "", err
	}
	accounts, err := s.accounts(ctx, rows)
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

// PageBefore returns the bounded window just before a cursor, in ascending order.
func (s *Store) PageBefore(ctx context.Context, query, before string, limit int) ([]Account, bool, error) {
	if limit < 1 || limit > 100 || len(query) > 80 || !validID(before) {
		return nil, false, errors.New("invalid account page")
	}
	rows, err := s.DB.Query(ctx, `SELECT site_id FROM (SELECT site_id FROM relay_sites
 WHERE ($1='' OR site_id ILIKE $2 OR origin ILIKE $2) AND site_id<$3 ORDER BY site_id DESC LIMIT $4) page ORDER BY site_id`,
		strings.TrimSpace(query), likePattern(query), before, limit+1)
	if err != nil {
		return nil, false, err
	}
	accounts, err := s.accounts(ctx, rows)
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
	err := s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM relay_sites),
 (SELECT count(*) FROM relay_channel_credentials c JOIN relay_sites a ON a.site_id=c.site_id WHERE c.enabled AND a.enabled)`).Scan(&total, &enabled)
	return total, enabled, err
}

func (s *Store) accounts(ctx context.Context, rows pgx.Rows) ([]Account, error) {
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	result := make([]Account, 0, len(ids))
	for _, id := range ids {
		account, err := s.account(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, account)
	}
	return result, nil
}

func (s *Store) account(ctx context.Context, id string) (Account, error) {
	site, err := s.GetSite(ctx, id)
	if err != nil {
		return Account{}, err
	}
	account := Account{ID: id, Site: site.Origin, Enabled: site.Enabled, Channels: []AccountChannel{}}
	for _, channel := range site.Channels {
		if channel.Key != id {
			continue
		}
		view := AccountChannel{Channel: channel.Channel, Enabled: channel.Enabled, Config: channel.Config, ConfiguredSecrets: channel.ConfiguredSecrets}
		if channel.Channel == "WhatsApp" {
			container, err := s.GetContainer(ctx, channel.Container)
			if err != nil {
				return Account{}, err
			}
			for key, value := range container.WhatsApp.Config {
				view.Config[key] = value
			}
			view.ConfiguredSecrets = appendProviderSecrets(view.ConfiguredSecrets, container.WhatsApp.ConfiguredSecrets)
		}
		view.ConfiguredSecrets = append(view.ConfiguredSecrets, site.ConfiguredSecrets...)
		account.Channels = append(account.Channels, view)
	}
	return account, nil
}

func appendProviderSecrets(channel, provider []string) []string {
	for _, key := range provider {
		if key == "access_token" {
			if !contains(channel, "secret") {
				channel = append([]string{"secret"}, channel...)
			}
			continue
		}
		channel = append(channel, key)
	}
	return channel
}

func (s *Store) Save(ctx context.Context, a Account, secrets map[string]string) error {
	return s.saveAccount(ctx, a, secrets, false)
}

func (s *Store) Create(ctx context.Context, a Account, secrets map[string]string) error {
	return s.saveAccount(ctx, a, secrets, true)
}

// accountParts splits one legacy channel config into its channel, Meta app and site parts.
func accountParts(c AccountChannel) (map[string]any, map[string]any, map[string]any, error) {
	channel, provider, site := map[string]any{}, map[string]any{}, map[string]any{}
	for key, value := range c.Config {
		switch {
		case c.Channel == "WhatsApp" && (providerPublicKeys[key] || contains(providerPrivateKeys, key)):
			provider[key] = value
		case channelPublicKeys[c.Channel][key] || contains(channelPrivateKeys[c.Channel], key):
			channel[key] = value
		case contains(siteSecretKeys, key):
			site[key] = value
		default:
			return nil, nil, nil, errors.New("unsupported channel configuration field")
		}
	}
	return channel, provider, site, nil
}

func (s *Store) saveAccount(ctx context.Context, a Account, secrets map[string]string, create bool) error {
	if err := validate(a.ID, a.Site); err != nil {
		return err
	}
	siteEdits, seen := map[string]any{}, map[string]bool{}
	for _, c := range a.Channels {
		if seen[c.Channel] || !channels[c.Channel] {
			return errors.New("duplicate or unsupported channel")
		}
		seen[c.Channel] = true
		_, _, site, err := accountParts(c)
		if err != nil {
			return err
		}
		for key, value := range site {
			if previous, ok := siteEdits[key]; ok && previous != "" && value != "" && previous != value {
				return errors.New("Telegram and email share Core delivery; use the same three Core credentials for both channels")
			}
			if value != "" || siteEdits[key] == nil {
				siteEdits[key] = value
			}
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.saveSite(ctx, tx, Site{ID: a.ID, Origin: a.Site, Enabled: a.Enabled}, siteEdits, create); err != nil {
		if create && err.Error() == "site already exists" {
			return errors.New("account already exists")
		}
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_containers(container_id,name) VALUES($1,$1) ON CONFLICT DO NOTHING`, a.ID); err != nil {
		return err
	}
	for _, c := range a.Channels {
		if err = s.saveAccountChannel(ctx, tx, a.ID, c, secrets[c.Channel]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) saveAccountChannel(ctx context.Context, tx pgx.Tx, id string, c AccountChannel, secret string) error {
	config, provider, _, err := accountParts(c)
	if err != nil {
		return err
	}
	if c.Channel == "WhatsApp" && len(provider) > 0 {
		if err = s.saveProvider(ctx, tx, id, provider, ""); err != nil {
			return err
		}
	}
	if err = s.saveChannel(ctx, tx, Channel{Key: id, Channel: c.Channel, Container: id, Enabled: c.Enabled, Config: config}, secret, false); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_channel_credentials SET site_id=$1,assigned_at=coalesce(assigned_at,now())
 WHERE channel=$2 AND channel_key=$1 AND (site_id IS NULL OR site_id=$1)`, id, c.Channel)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
