package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ServiceUser ensures the member user that holds one enrolled site's API
// token, named site:<site>, and grants it that site. A service user has no
// usable password and Login refuses it.
func ServiceUser(ctx context.Context, tx pgx.Tx, site string) (int64, error) {
	if site == "" || len(site) > 100 {
		return 0, errors.New("invalid site")
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO relay_users(username,password_salt,password_hash,role,kind) VALUES($1,'','!','member','service')
 ON CONFLICT(username) DO UPDATE SET enabled=true WHERE relay_users.kind='service' RETURNING id`, "site:"+site).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errors.New("service user name is taken by a person")
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO relay_user_sites(user_id,site_id) VALUES($1,$2) ON CONFLICT DO NOTHING", id, site); err != nil {
		return 0, err
	}
	return id, nil
}

// RotateServiceToken revokes the service user's live tokens for site and
// issues one token without expiry. The token is returned once.
func RotateServiceToken(ctx context.Context, tx pgx.Tx, user int64, site string) (string, error) {
	if _, err := tx.Exec(ctx, "UPDATE relay_api_tokens SET revoked_at=now() WHERE user_id=$1 AND site_id=$2 AND revoked_at IS NULL", user, site); err != nil {
		return "", err
	}
	token := "relay_" + Secret()
	if _, err := tx.Exec(ctx, "INSERT INTO relay_api_tokens(user_id,name,token_hash,site_id,expires_at) VALUES($1,'Site enrolment',$2,$3,'infinity'::timestamptz)", user, Hash(token), site); err != nil {
		return "", err
	}
	return token, nil
}

// RevokeServiceTokens revokes the live Core tokens of site: those of its
// service user. Tokens an operator issued for site keep working.
func RevokeServiceTokens(ctx context.Context, tx pgx.Tx, site string) error {
	_, err := tx.Exec(ctx, `UPDATE relay_api_tokens t SET revoked_at=now() FROM relay_users u
 WHERE t.user_id=u.id AND u.kind='service' AND u.username='site:' || $1 AND t.site_id=$1 AND t.revoked_at IS NULL`, site)
	return err
}
