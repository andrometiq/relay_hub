package onboarding

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	enrolPrefix = "relay_enrol_"
	setupPrefix = "relay_setup_"
)

// EnrolmentToken is an enrolment token or setup code as the Hub admin sees it.
type EnrolmentToken struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	OriginPattern string     `json:"origin_pattern"`
	MaxSites      *int       `json:"max_sites"`
	UsedSites     int        `json:"used_sites"`
	ExpiresAt     *time.Time `json:"expires_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
	CreatedAt     time.Time  `json:"created_at"`
	Status        string     `json:"status"`
}

// TokenInput creates an enrolment token. ExpiryDays 0 means no expiry for a
// bench token; a setup code lasts 1 to 90 days and defaults to one site.
type TokenInput struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	OriginPattern string `json:"origin_pattern"`
	MaxSites      *int   `json:"max_sites"`
	ExpiryDays    int    `json:"expiry_days"`
}

// CreateToken stores a new token and returns its secret once.
func (s *Service) CreateToken(ctx context.Context, actor auth.User, in TokenInput) (EnrolmentToken, string, error) {
	in.Name, in.OriginPattern = strings.TrimSpace(in.Name), strings.TrimSpace(in.OriginPattern)
	if err := validToken(&in); err != nil {
		return EnrolmentToken{}, "", err
	}
	secret := enrolPrefix + auth.Secret()
	if in.Kind == "setup_code" {
		secret = setupPrefix + auth.Secret()[:32]
	}
	var expiry any = s.now().Add(time.Duration(in.ExpiryDays) * 24 * time.Hour)
	if in.ExpiryDays == 0 {
		expiry = pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
	}
	tx, err := s.cfg.DB.Begin(ctx)
	if err != nil {
		return EnrolmentToken{}, "", err
	}
	defer tx.Rollback(context.Background())
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO relay_enrolment_tokens(name,token_hash,kind,origin_pattern,max_sites,expires_at,created_by)
 VALUES($1,$2,$3,$4,$5,$6,nullif($7::bigint,0)) RETURNING id`, in.Name, auth.Hash(secret), in.Kind, in.OriginPattern, in.MaxSites, expiry, actor.ID).Scan(&id); err != nil {
		return EnrolmentToken{}, "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,actor_id,action,outcome,metadata) VALUES('user',nullif($1::bigint,0),'enrolment_token.create','completed',jsonb_build_object('token_id',$2::bigint,'kind',$3::text))`, actor.ID, id, in.Kind); err != nil {
		return EnrolmentToken{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return EnrolmentToken{}, "", err
	}
	token, err := s.getToken(ctx, id)
	return token, secret, err
}

func validToken(in *TokenInput) error {
	if in.Name == "" || len(in.Name) > 100 || (in.Kind != "bench" && in.Kind != "setup_code") || len(in.OriginPattern) > 300 {
		return errors.New("token needs a name and kind bench or setup_code")
	}
	if in.MaxSites != nil && (*in.MaxSites < 1 || *in.MaxSites > 10000) {
		return errors.New("max sites must be from 1 to 10000")
	}
	if in.Kind == "setup_code" {
		if in.MaxSites == nil {
			one := 1
			in.MaxSites = &one
		}
		if in.ExpiryDays == 0 {
			in.ExpiryDays = 7
		}
		if in.ExpiryDays < 1 || in.ExpiryDays > 90 {
			return errors.New("a setup code lasts 1 to 90 days")
		}
	} else if in.OriginPattern == "" || in.ExpiryDays < 0 || in.ExpiryDays > 3650 {
		return errors.New("a bench token needs an origin pattern and 0 (no expiry) to 3650 days")
	}
	if in.OriginPattern != "" && !validPattern(in.OriginPattern) {
		return errors.New("origin pattern must look like https://*.example.com or https://shop.example.com")
	}
	return nil
}

func validPattern(pattern string) bool {
	u, err := url.Parse(strings.ToLower(pattern))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return false
	}
	host := strings.TrimPrefix(u.Hostname(), "*.")
	return host == "localhost" || (!strings.Contains(host, "*") && strings.Contains(host, "."))
}

// ListTokens lists every token, newest first.
func (s *Service) ListTokens(ctx context.Context) ([]EnrolmentToken, error) {
	rows, err := s.cfg.DB.Query(ctx, tokenQuery+` ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EnrolmentToken{}
	for rows.Next() {
		token, err := s.scanToken(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, token)
	}
	return result, rows.Err()
}

// RevokeToken stops a token; sites it enrolled keep working.
func (s *Service) RevokeToken(ctx context.Context, actor auth.User, id int64) error {
	tx, err := s.cfg.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `UPDATE relay_enrolment_tokens SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, id, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,actor_id,action,outcome,metadata) VALUES('user',nullif($1::bigint,0),'enrolment_token.revoke','completed',jsonb_build_object('token_id',$2::bigint))`, actor.ID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SiteEnrolment names the token that enrolled a site, for the site detail view.
func (s *Service) SiteEnrolment(ctx context.Context, site string) (map[string]any, error) {
	var name *string
	var at *time.Time
	err := s.cfg.DB.QueryRow(ctx, `SELECT t.name,s.enrolled_at FROM relay_sites s LEFT JOIN relay_enrolment_tokens t ON t.id=s.enrolled_by WHERE s.site_id=$1`, site).Scan(&name, &at)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": name, "enrolled_at": at}, nil
}

const tokenQuery = `SELECT id,name,kind,origin_pattern,max_sites,used_sites,CASE WHEN expires_at='infinity' THEN NULL ELSE expires_at END,revoked_at,created_at FROM relay_enrolment_tokens`

func (s *Service) getToken(ctx context.Context, id int64) (EnrolmentToken, error) {
	return s.scanToken(s.cfg.DB.QueryRow(ctx, tokenQuery+` WHERE id=$1`, id))
}

func (s *Service) scanToken(row pgx.Row) (EnrolmentToken, error) {
	var t EnrolmentToken
	if err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.OriginPattern, &t.MaxSites, &t.UsedSites, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt); err != nil {
		return t, err
	}
	switch {
	case t.RevokedAt != nil:
		t.Status = "revoked"
	case t.ExpiresAt != nil && !t.ExpiresAt.After(s.now()):
		t.Status = "expired"
	case t.MaxSites != nil && t.UsedSites >= *t.MaxSites:
		t.Status = "used"
	default:
		t.Status = "active"
	}
	return t, nil
}

// SetupLink is what a site owner pastes in Get started: the code and this Hub's address in one link,
// so a site without relay_hub_origin can find the Hub.
func (s *Service) SetupLink(secret string) string {
	return s.cfg.PublicOrigin + "/enrol?code=" + url.QueryEscape(secret)
}
