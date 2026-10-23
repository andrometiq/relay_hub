package accounts

import (
	"bytes"
	"context"
	"errors"
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
	if _, err = tx.Exec(ctx, `LOCK TABLE relay_channel_credentials IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return report, errors.New("could not lock credential storage")
	}
	type row struct {
		id                                       int64
		account, channel                         string
		secret, secretNonce, config, configNonce []byte
	}
	var cursor int64
	for {
		rows, err := tx.Query(ctx, `SELECT id,account_id,channel,secret_ciphertext,secret_nonce,config_ciphertext,config_nonce FROM relay_channel_credentials WHERE id>$1 ORDER BY id LIMIT 100 FOR UPDATE`, cursor)
		if err != nil {
			return report, errors.New("could not read credential batch")
		}
		batch := []row{}
		for rows.Next() {
			var value row
			if err = rows.Scan(&value.id, &value.account, &value.channel, &value.secret, &value.secretNonce, &value.config, &value.configNonce); err != nil {
				rows.Close()
				return report, errors.New("could not decode credential batch")
			}
			batch = append(batch, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return report, errors.New("credential batch interrupted")
		}
		if len(batch) == 0 {
			break
		}
		for _, value := range batch {
			report.Rows++
			changed := false
			for _, field := range []struct {
				name          string
				cipher, nonce *[]byte
			}{{"secret", &value.secret, &value.secretNonce}, {"private_config", &value.config, &value.configNonce}} {
				ciphertext, nonce, state, err := rotateField(s, next, *field.cipher, *field.nonce)
				if err != nil {
					return report, err
				}
				switch state {
				case "rotated":
					report.RotatedFields++
					changed = true
				case "current":
					report.AlreadyUsingNewKey++
				case "unreadable":
					report.Unreadable = append(report.Unreadable, RotationIssue{value.account, value.channel, field.name})
				}
				*field.cipher = ciphertext
				*field.nonce = nonce
			}
			if apply && changed {
				if _, err = tx.Exec(ctx, `UPDATE relay_channel_credentials SET secret_ciphertext=$2,secret_nonce=$3,config_ciphertext=$4,config_nonce=$5,credential_version=credential_version+1,updated_at=now() WHERE id=$1`, value.id, value.secret, value.secretNonce, value.config, value.configNonce); err != nil {
					return report, errors.New("could not write credential rotation")
				}
			}
			cursor = value.id
		}
	}
	if apply {
		if err = tx.Commit(ctx); err != nil {
			return report, errors.New("credential rotation commit was not confirmed; verify with a dry run")
		}
	}
	return report, nil
}
