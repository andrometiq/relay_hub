package containers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type RotationIssue struct {
	Account string `json:"account"`
	Channel string `json:"channel"`
	Field   string `json:"field"`
}
type RotationReport struct {
	DryRun             bool            `json:"dry_run"`
	Rows               int             `json:"rows"`
	RotatedFields      int             `json:"rotated_fields"`
	AlreadyUsingNewKey int             `json:"already_using_new_key"`
	Unreadable         []RotationIssue `json:"unreadable"`
}

// rotateField preserves unreadable ciphertext byte-for-byte, including its nonce.
// A retry can recognize fields already encrypted with the intended new key.
func rotateField(old, next *Store, ciphertext, nonce []byte) ([]byte, []byte, string, error) {
	if len(ciphertext) == 0 {
		return ciphertext, nonce, "empty", nil
	}
	plain, err := old.unbox(ciphertext, nonce)
	if err != nil {
		if _, err = next.unbox(ciphertext, nonce); err == nil {
			return ciphertext, nonce, "current", nil
		}
		return ciphertext, nonce, "unreadable", nil
	}
	boxed, newNonce, err := next.box(plain)
	if err != nil {
		return nil, nil, "", errors.New("could not encrypt rotated credential")
	}
	return boxed, newNonce, "rotated", nil
}

type rotationTable struct {
	name, key, label string
	fields           []string
}

// Every encrypted column pair, by table. Keys are text so one loop serves all.
var rotationTables = []rotationTable{
	{"relay_channel_credentials", "id::text", "channel_key||'/'||channel", []string{"secret", "config"}},
	{"relay_container_providers", "container_id||'/'||provider", "container_id||'/'||provider", []string{"secret", "config"}},
	{"relay_sites", "site_id", "site_id", []string{"secret"}},
}

type rotationRow struct {
	key, label string
	values     [][]byte
}

// RotateKey is an offline transaction. The caller must stop credential consumers
// before applying it, then restart them with the new key after commit.
func (s *Store) RotateKey(ctx context.Context, newKey string, apply bool) (RotationReport, error) {
	report := RotationReport{DryRun: !apply, Unreadable: []RotationIssue{}}
	next, err := New(s.DB, newKey)
	if err != nil {
		return report, err
	}
	if bytes.Equal(s.Key, next.Key) {
		return report, errors.New("old and new keys must differ")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return report, errors.New("could not begin credential rotation")
	}
	defer tx.Rollback(ctx)
	for _, table := range rotationTables {
		if err = s.rotateTable(ctx, tx, next, table, apply, &report); err != nil {
			return report, err
		}
	}
	if apply {
		if err = tx.Commit(ctx); err != nil {
			return report, errors.New("credential rotation commit was not confirmed; verify with a dry run")
		}
	}
	return report, nil
}

func (s *Store) rotateTable(ctx context.Context, tx pgx.Tx, next *Store, table rotationTable, apply bool, report *RotationReport) error {
	if _, err := tx.Exec(ctx, `LOCK TABLE `+table.name+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return errors.New("could not lock credential storage")
	}
	columns := []string{table.key, table.label}
	for _, field := range table.fields {
		columns = append(columns, field+"_ciphertext", field+"_nonce")
	}
	rows, err := tx.Query(ctx, `SELECT `+strings.Join(columns, ",")+` FROM `+table.name+` ORDER BY 1 FOR UPDATE`)
	if err != nil {
		return errors.New("could not read credential batch")
	}
	batch := []rotationRow{}
	for rows.Next() {
		row := rotationRow{values: make([][]byte, len(table.fields)*2)}
		targets := []any{&row.key, &row.label}
		for i := range row.values {
			targets = append(targets, &row.values[i])
		}
		if err = rows.Scan(targets...); err != nil {
			rows.Close()
			return errors.New("could not decode credential batch")
		}
		batch = append(batch, row)
	}
	rows.Close()
	if rows.Err() != nil {
		return errors.New("credential batch interrupted")
	}
	for _, row := range batch {
		if err = s.rotateRow(ctx, tx, next, table, row, apply, report); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) rotateRow(ctx context.Context, tx pgx.Tx, next *Store, table rotationTable, row rotationRow, apply bool, report *RotationReport) error {
	report.Rows++
	changed := false
	assignments := []string{}
	arguments := []any{row.key}
	for i, field := range table.fields {
		ciphertext, nonce, state, err := rotateField(s, next, row.values[2*i], row.values[2*i+1])
		if err != nil {
			return err
		}
		switch state {
		case "rotated":
			report.RotatedFields++
			changed = true
		case "current":
			report.AlreadyUsingNewKey++
		case "unreadable":
			name := field
			if field == "config" {
				name = "private_config"
			}
			report.Unreadable = append(report.Unreadable, RotationIssue{Account: row.label, Channel: table.name, Field: name})
		}
		arguments = append(arguments, ciphertext, nonce)
		assignments = append(assignments, fmt.Sprintf("%s_ciphertext=$%d,%s_nonce=$%d", field, len(arguments)-1, field, len(arguments)))
	}
	if !apply || !changed {
		return nil
	}
	if table.name == "relay_channel_credentials" {
		assignments = append(assignments, "credential_version=credential_version+1")
	}
	if _, err := tx.Exec(ctx, `UPDATE `+table.name+` SET `+strings.Join(assignments, ",")+`,updated_at=now() WHERE `+table.key+`=$1`, arguments...); err != nil {
		return errors.New("could not write credential rotation")
	}
	return nil
}
