package containers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type legacyRow struct {
	id                       int64
	channel, container, site string
	enabled                  bool
	public                   map[string]any
	private                  map[string]string
}

// UpgradeContainers moves legacy per-channel values to their new owners: the
// Meta app values to the container and the Core credentials to the site.
// SQL cannot do this because the values are encrypted. It is repeatable and
// refuses to start when two channels of one owner hold different values.
func (s *Store) UpgradeContainers(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(771842016)`); err != nil {
		return err
	}
	rows, err := s.legacyRows(ctx, tx)
	if err != nil {
		return err
	}
	providers, sites := map[string]map[string]any{}, map[string]map[string]any{}
	for _, row := range rows {
		if err = collect(providers, row.container, row, providerPublicKeys, providerPrivateKeys, legacyProvider(row)); err != nil {
			return err
		}
		if err = collect(sites, row.site, row, nil, siteSecretKeys, true); err != nil {
			return err
		}
	}
	for container, values := range providers {
		if err = s.mergeProvider(ctx, tx, container, values); err != nil {
			return err
		}
	}
	for site, values := range sites {
		if err = s.mergeSite(ctx, tx, site, values); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err = s.stripLegacy(ctx, tx, row); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) legacyRows(ctx context.Context, tx pgx.Tx) ([]legacyRow, error) {
	rows, err := tx.Query(ctx, `SELECT id,channel,container_id,coalesce(site_id,''),enabled,config,config_ciphertext,config_nonce FROM relay_channel_credentials ORDER BY enabled DESC,id FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []legacyRow{}
	for rows.Next() {
		var row legacyRow
		var raw, cipher, nonce []byte
		if err = rows.Scan(&row.id, &row.channel, &row.container, &row.site, &row.enabled, &raw, &cipher, &nonce); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &row.public) != nil {
			return nil, errors.New("invalid stored channel configuration")
		}
		if row.private, err = s.configSecrets(cipher, nonce); err != nil {
			return nil, err
		}
		if hasLegacy(row) {
			result = append(result, row)
		}
	}
	return result, rows.Err()
}

// legacyProvider is a WhatsApp row that still holds Meta app values in its
// public config. A new number never has app_id or graph_version there; its
// own app_secret and verify_token are a deliberate per-number override.
func legacyProvider(row legacyRow) bool {
	if row.channel != "WhatsApp" {
		return false
	}
	for key := range providerPublicKeys {
		if _, ok := row.public[key]; ok {
			return true
		}
	}
	return false
}

func hasLegacy(row legacyRow) bool {
	if legacyProvider(row) {
		return true
	}
	for _, key := range siteSecretKeys {
		if _, ok := row.private[key]; ok {
			return true
		}
	}
	return false
}

// collect gathers values for one owner. Rows come enabled first: a disabled
// row never overrides an enabled one, and two enabled rows must agree.
func collect(owners map[string]map[string]any, owner string, row legacyRow, public map[string]bool, private []string, applies bool) error {
	values := map[string]any{}
	for key := range public {
		if value, ok := row.public[key]; ok && applies && value != "" {
			values[key] = value
		}
	}
	for _, key := range private {
		if value := row.private[key]; value != "" && applies {
			values[key] = value
		}
	}
	if len(values) == 0 {
		return nil
	}
	if owner == "" {
		return errors.New("legacy Core credentials belong to a channel without a site")
	}
	if owners[owner] == nil {
		owners[owner] = map[string]any{}
	}
	for key, value := range values {
		previous, ok := owners[owner][key]
		if ok && previous != value && row.enabled {
			return fmt.Errorf("legacy channels of %s hold different %s values; fix them before upgrade", owner, key)
		}
		if !ok {
			owners[owner][key] = value
		}
	}
	return nil
}

func (s *Store) mergeProvider(ctx context.Context, tx pgx.Tx, container string, values map[string]any) error {
	var raw, cipher, nonce []byte
	err := tx.QueryRow(ctx, `SELECT config,config_ciphertext,config_nonce FROM relay_container_providers WHERE container_id=$1 AND provider='WhatsApp' FOR UPDATE`, container).Scan(&raw, &cipher, &nonce)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	stored := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &stored) != nil {
		return errors.New("invalid stored container configuration")
	}
	private, err := s.configSecrets(cipher, nonce)
	if err != nil {
		return err
	}
	for key, value := range values {
		current, ok := stored[key]
		if text := private[key]; text != "" {
			current, ok = text, true
		}
		if ok && current != "" && current != value {
			return fmt.Errorf("container %s already holds a different %s", container, key)
		}
	}
	return s.saveProvider(ctx, tx, container, values, "")
}

func (s *Store) mergeSite(ctx context.Context, tx pgx.Tx, site string, values map[string]any) error {
	current, err := s.siteSecrets(ctx, tx, site)
	if err != nil {
		return err
	}
	for key, value := range values {
		if current[key] != "" && current[key] != value {
			return fmt.Errorf("site %s already holds a different %s", site, key)
		}
		current[key] = value.(string)
	}
	if err = validateCore(current); err != nil {
		return fmt.Errorf("site %s: %w", site, err)
	}
	cipher, nonce, err := s.boxMap(current)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE relay_sites SET secret_ciphertext=$2,secret_nonce=$3,updated_at=now() WHERE site_id=$1`, site, cipher, nonce)
	return err
}

func (s *Store) siteSecrets(ctx context.Context, tx pgx.Tx, site string) (map[string]string, error) {
	var cipher, nonce []byte
	if err := tx.QueryRow(ctx, `SELECT secret_ciphertext,secret_nonce FROM relay_sites WHERE site_id=$1 FOR UPDATE`, site).Scan(&cipher, &nonce); err != nil {
		return nil, err
	}
	return s.configSecrets(cipher, nonce)
}

func (s *Store) stripLegacy(ctx context.Context, tx pgx.Tx, row legacyRow) error {
	if legacyProvider(row) {
		for key := range providerPublicKeys {
			delete(row.public, key)
		}
		for _, key := range providerPrivateKeys {
			delete(row.private, key)
		}
	}
	for _, key := range siteSecretKeys {
		delete(row.private, key)
	}
	raw, err := json.Marshal(row.public)
	if err != nil {
		return err
	}
	cipher, nonce, err := s.boxMap(row.private)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE relay_channel_credentials SET config=$2,config_ciphertext=$3,config_nonce=$4,updated_at=now() WHERE id=$1`, row.id, raw, cipher, nonce)
	return err
}
