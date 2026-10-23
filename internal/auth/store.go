// Package auth owns Hub identities, browser sessions and account-scoped API credentials.
package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"regexp"
	"strings"
	"time"
)

var ErrDenied = errors.New("authentication or permission denied")
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.@-]{2,99}$`)
var Scopes = []string{"messages:send", "messages:read", "deliveries:read", "deliveries:cancel", "templates:read", "templates:write", "groups:read", "groups:write", "calls:read", "calls:write", "account:read"}

type Store struct{ DB *pgxpool.Pool }
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
	Version  int64  `json:"-"`
}
type Principal struct {
	UserID  int64
	Account string
	Scopes  []string
}

func (p Principal) Allows(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}
func Secret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func password(v string) (string, string, error) {
	if len(v) < 14 || len(v) > 200 {
		return "", "", errors.New("password must contain 14 to 200 bytes")
	}
	salt := Secret()[:32]
	sb, _ := hex.DecodeString(salt)
	h, e := pbkdf2.Key(sha256.New, v, sb, 600000, 32)
	return salt, hex.EncodeToString(h), e
}
func verify(p, s, h string) bool {
	sb, e := hex.DecodeString(s)
	if e != nil {
		return false
	}
	want, e := hex.DecodeString(h)
	if e != nil {
		return false
	}
	got, e := pbkdf2.Key(sha256.New, p, sb, 600000, 32)
	return e == nil && subtle.ConstantTimeCompare(want, got) == 1
}
func audit(ctx context.Context, tx pgx.Tx, actor any, action string, target int64) error {
	if _, e := tx.Exec(ctx, "INSERT INTO relay_auth_audit(actor_id,action,target_id) VALUES($1,$2,$3)", actor, action, target); e != nil {
		return e
	}
	// Keep the normalized operational trail in the same transaction. The action
	// names are server-authored; credentials and passwords never enter metadata.
	_, e := tx.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,actor_id,action,outcome,metadata) VALUES('user',$1,$2,'completed',jsonb_build_object('target_id',$3::bigint))`, actor, action, target)
	return e
}

// RecordOperationalAudit appends a non-transactional event for actions that do
// not mutate an auth record (for example rejected login attempts or logout).
// Callers must pass server-authored action/outcome values; secrets are never
// accepted as metadata by this package.
func (s *Store) RecordOperationalAudit(ctx context.Context, actorType, action, outcome, errorCode string, actorID *int64) error {
	if actorType == "" || action == "" || outcome == "" {
		return errors.New("audit fields are required")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,actor_id,action,outcome,error_code) VALUES($1,$2,$3,$4,$5)`, actorType, actorID, action, outcome, errorCode)
	return err
}
func (s *Store) Bootstrap(ctx context.Context, name, pass string) error {
	if !usernamePattern.MatchString(name) {
		return errors.New("invalid username")
	}
	salt, h, e := password(pass)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351019)"); e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM relay_users").Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return errors.New("bootstrap is disabled after the first user")
	}
	var id int64
	if e = tx.QueryRow(ctx, "INSERT INTO relay_users(username,password_salt,password_hash,role) VALUES($1,$2,$3,'admin') RETURNING id", name, salt, h).Scan(&id); e != nil {
		return e
	}
	if e = audit(ctx, tx, id, "bootstrap", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) CreateUser(ctx context.Context, actor User, name, pass, role string) (int64, error) {
	if !usernamePattern.MatchString(name) || (role != "admin" && role != "member") {
		return 0, errors.New("invalid username or role")
	}
	salt, h, e := password(pass)
	if e != nil {
		return 0, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	if e = admin(ctx, tx, actor.ID); e != nil {
		return 0, e
	}
	var id int64
	if e = tx.QueryRow(ctx, "INSERT INTO relay_users(username,password_salt,password_hash,role) VALUES($1,$2,$3,$4) RETURNING id", name, salt, h, role).Scan(&id); e != nil {
		return 0, errors.New("username unavailable")
	}
	if e = audit(ctx, tx, actor.ID, "user.create", id); e != nil {
		return 0, e
	}
	return id, tx.Commit(ctx)
}
func admin(ctx context.Context, tx pgx.Tx, id int64) error {
	var ok bool
	e := tx.QueryRow(ctx, "SELECT enabled AND role='admin' FROM relay_users WHERE id=$1 FOR UPDATE", id).Scan(&ok)
	if e != nil || !ok {
		return ErrDenied
	}
	return nil
}
func (s *Store) Login(ctx context.Context, name, pass, agent string) (string, error) {
	var u User
	var salt, h string
	e := s.DB.QueryRow(ctx, "SELECT id,username,role,enabled,auth_version,password_salt,password_hash FROM relay_users WHERE username=$1", name).Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.Version, &salt, &h)
	if e != nil {
		salt = strings.Repeat("0", 32)
		h = strings.Repeat("0", 64)
	}
	valid := verify(pass, salt, h)
	if e != nil || !valid || !u.Enabled {
		// Do not reveal whether the username exists, but retain a security trail.
		_ = s.RecordOperationalAudit(ctx, "anonymous", "session.login", "rejected", "invalid_credentials", nil)
		return "", ErrDenied
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	var version int64
	var enabled bool
	if e = tx.QueryRow(ctx, "SELECT auth_version,enabled FROM relay_users WHERE id=$1 FOR UPDATE", u.ID).Scan(&version, &enabled); e != nil || !enabled || version != u.Version {
		return "", ErrDenied
	}
	if _, e = tx.Exec(ctx, "DELETE FROM relay_auth_sessions WHERE user_id=$1 AND (expires_at<=now() OR auth_version<>$2)", u.ID, u.Version); e != nil {
		return "", e
	}
	var sessions int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM relay_auth_sessions WHERE user_id=$1", u.ID).Scan(&sessions); e != nil {
		return "", e
	}
	if sessions >= 50 {
		return "", errors.New("revoke an existing session before signing in on another device")
	}

	if len(agent) > 300 {
		agent = agent[:300]
	}
	token := Secret()
	if _, e = tx.Exec(ctx, "INSERT INTO relay_auth_sessions(user_id,token_hash,auth_version,expires_at,user_agent) VALUES($1,$2,$3,'infinity'::timestamptz,$4)", u.ID, Hash(token), u.Version, agent); e != nil {
		return "", e
	}
	if e = audit(ctx, tx, u.ID, "session.login", u.ID); e != nil {
		return "", e
	}
	return token, tx.Commit(ctx)
}
func (s *Store) Session(ctx context.Context, token string) (User, error) {
	var u User
	if len(token) != 64 {
		return u, ErrDenied
	}
	e := s.DB.QueryRow(ctx, `UPDATE relay_auth_sessions s SET last_seen=now(), expires_at='infinity'::timestamptz FROM relay_users u WHERE s.token_hash=$1 AND s.user_id=u.id AND u.enabled AND s.auth_version=u.auth_version AND s.expires_at>now() RETURNING u.id,u.username,u.role,u.enabled,u.auth_version`, Hash(token)).Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.Version)
	if errors.Is(e, pgx.ErrNoRows) {
		return u, ErrDenied
	}
	if e != nil {
		return u, e
	}
	return u, nil
}
func (s *Store) Authenticate(ctx context.Context, token string) (Principal, error) {
	var p Principal
	if !strings.HasPrefix(token, "relay_") || len(token) != 70 {
		return p, ErrDenied
	}
	e := s.DB.QueryRow(ctx, `SELECT t.user_id,t.account_id,t.scopes FROM relay_api_tokens t JOIN relay_accounts a ON a.account_id=t.account_id AND a.enabled JOIN relay_users u ON u.id=t.user_id JOIN relay_user_accounts g ON g.user_id=u.id AND g.account_id=t.account_id WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND t.expires_at>now() AND u.enabled`, Hash(token)).Scan(&p.UserID, &p.Account, &p.Scopes)
	if e != nil {
		return p, ErrDenied
	}
	return p, nil
}
func (s *Store) ChangePassword(ctx context.Context, actor User, target int64, old, new string) error {
	return s.ChangePasswordWithTokenRevocation(ctx, actor, target, old, new, true)
}

// Self-service rotation can preserve integration tokens. Administrator resets
// always revoke credentials, regardless of the submitted preference.
func (s *Store) ChangePasswordWithTokenRevocation(ctx context.Context, actor User, target int64, old, new string, revokeTokens bool) error {
	salt, h, e := password(new)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Serialize security mutations to avoid privilege-change races and lock inversion.
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351020)"); e != nil {
		return e
	}
	if actor.ID != target {
		revokeTokens = true
		if e = admin(ctx, tx, actor.ID); e != nil {
			return e
		}
	}
	var currentSalt, currentHash string
	var enabled bool
	if e = tx.QueryRow(ctx, "SELECT password_salt,password_hash,enabled FROM relay_users WHERE id=$1 FOR UPDATE", target).Scan(&currentSalt, &currentHash, &enabled); e != nil {
		return ErrDenied
	}
	if actor.ID == target && (!enabled || !verify(old, currentSalt, currentHash)) {
		return ErrDenied
	}
	if _, e = tx.Exec(ctx, "UPDATE relay_users SET password_salt=$1,password_hash=$2,auth_version=auth_version+1 WHERE id=$3", salt, h, target); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM relay_auth_sessions WHERE user_id=$1", target); e != nil {
		return e
	}
	if revokeTokens {
		if _, e = tx.Exec(ctx, "UPDATE relay_api_tokens SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", target); e != nil {
			return e
		}
	}
	if e = audit(ctx, tx, actor.ID, "password.change", target); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) SetEnabled(ctx context.Context, actor User, target int64, enabled bool) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351020)"); e != nil {
		return e
	}
	if e = admin(ctx, tx, actor.ID); e != nil {
		return e
	}
	var role string
	if e = tx.QueryRow(ctx, "SELECT role FROM relay_users WHERE id=$1 FOR UPDATE", target).Scan(&role); e != nil {
		return ErrDenied
	}
	if !enabled && role == "admin" {
		var n int
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM relay_users WHERE enabled AND role='admin' AND id<>$1", target).Scan(&n); e != nil {
			return e
		}
		if n == 0 {
			return errors.New("cannot disable the last administrator")
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE relay_users SET enabled=$1,auth_version=auth_version+1 WHERE id=$2", enabled, target); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE relay_api_tokens SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", target); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor.ID, "user.enabled", target); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Grant(ctx context.Context, actor User, target int64, account string, allow bool) error {
	if account == "" || len(account) > 100 {
		return errors.New("invalid account")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351020)"); e != nil {
		return e
	}
	if e = admin(ctx, tx, actor.ID); e != nil {
		return e
	}
	if allow {
		_, e = tx.Exec(ctx, "INSERT INTO relay_user_accounts(user_id,account_id) VALUES($1,$2) ON CONFLICT DO NOTHING", target, account)
	} else {
		_, e = tx.Exec(ctx, "DELETE FROM relay_user_accounts WHERE user_id=$1 AND account_id=$2", target, account)
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE relay_api_tokens SET revoked_at=now() WHERE user_id=$1 AND account_id=$2 AND revoked_at IS NULL", target, account)
		}
	}
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor.ID, "account.grant", target); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Issue(ctx context.Context, u User, name, account string, scopes []string, days int) (string, error) {
	if len(name) < 1 || len(name) > 100 || days < 0 || days > 90 || len(scopes) == 0 || len(scopes) > len(Scopes) {
		return "", errors.New("token requires a name, scopes and expiry of 0 (no expiry) or 1–90 days")
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		ok := false
		for _, known := range Scopes {
			if scope == known {
				ok = true
			}
		}
		if !ok || seen[scope] {
			return "", errors.New("invalid scopes")
		}
		seen[scope] = true
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726351020)"); e != nil {
		return "", e
	}
	var granted bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM relay_user_accounts g JOIN relay_users u ON u.id=g.user_id WHERE g.user_id=$1 AND g.account_id=$2 AND u.enabled AND u.auth_version=$3)", u.ID, account, u.Version).Scan(&granted)
	if e != nil || !granted {
		return "", ErrDenied
	}
	var n int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM relay_api_tokens WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>now()", u.ID).Scan(&n); e != nil {
		return "", e
	}
	if n >= 20 {
		return "", errors.New("revoke an existing token first")
	}
	token := "relay_" + Secret()
	var id int64
	var expiry any = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	if days == 0 {
		expiry = pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
	}
	e = tx.QueryRow(ctx, "INSERT INTO relay_api_tokens(user_id,name,token_hash,account_id,scopes,expires_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING id", u.ID, name, Hash(token), account, scopes, expiry).Scan(&id)
	if e != nil {
		return "", e
	}
	if e = audit(ctx, tx, u.ID, "token.issue", id); e != nil {
		return "", e
	}
	return token, tx.Commit(ctx)
}
func (s *Store) Revoke(ctx context.Context, u User, id int64) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "UPDATE relay_api_tokens SET revoked_at=now() WHERE id=$1 AND user_id=$2", id, u.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrDenied
	}
	if e = audit(ctx, tx, u.ID, "token.revoke", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
