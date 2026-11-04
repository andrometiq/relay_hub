package onboarding

import (
	"context"
	"errors"
	"log"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Disconnect is the site's own end of the link: the site is turned off and its
// tokens are revoked, so the Hub stops callbacks and refuses its calls. Its
// channels stay linked and keep their history; re-enrolment turns the site on
// again with a new token. Numbers to free are removed before this call.
func (s *Service) Disconnect(ctx context.Context, site string, confirm bool) error {
	if !confirm {
		return fail(422, "confirm_required", "Confirm that the site should be disconnected.")
	}
	tx, err := s.cfg.DB.Begin(ctx)
	if err != nil {
		return unavailable()
	}
	defer tx.Rollback(context.Background())
	var id string
	err = tx.QueryRow(ctx, `UPDATE relay_sites SET enabled=false,disconnected_at=$2 WHERE site_id=$1 AND enabled RETURNING site_id`, site, s.now()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "not_found", "This site is not connected to the Hub.")
	}
	if err != nil {
		return unavailable()
	}
	if err = auth.RevokeSiteTokens(ctx, tx, site); err != nil {
		return unavailable()
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_operational_audit(actor_type,account_id,action,outcome) VALUES('api_token',$1,'site.disconnect','completed')`, site); err != nil {
		return unavailable()
	}
	if err = tx.Commit(ctx); err != nil {
		return unavailable()
	}
	if err = s.cfg.Reload(context.WithoutCancel(ctx)); err != nil {
		log.Printf("disconnect %s: runtime reload failed: %v", site, err)
	}
	return nil
}
