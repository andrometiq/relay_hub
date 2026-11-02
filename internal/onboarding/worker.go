package onboarding

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/andrometiq/relay_hub/internal/lifecycle"
	"github.com/jackc/pgx/v5"
)

const (
	workerInterval = 30 * time.Second
	leaseTime      = 2 * time.Minute
	workerBudget   = 90 * time.Second
)

// Run resumes due signup steps every 30 seconds and when a step asks for it,
// and expires sessions that waited too long for Meta.
func (s *Service) Run(ctx context.Context) {
	lifecycle.Go(ctx, func() {
		ticker := time.NewTicker(workerInterval)
		defer ticker.Stop()
		for {
			s.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	})
}

// Tick expires stale sessions and runs every due session once.
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	rows, err := s.cfg.DB.Query(ctx, `UPDATE relay_signup_sessions SET state='expired',updated_at=$1
 WHERE state IN ('created','waiting_for_meta') AND expires_at<=$1 RETURNING site_id,session_id`, now)
	if err == nil {
		expired, _ := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Site, ID string }])
		for _, row := range expired {
			s.journalAlone(ctx, row.Site, row.ID, "expired")
		}
	}
	rows, err = s.cfg.DB.Query(ctx, `SELECT session_id FROM relay_signup_sessions WHERE state='working' AND next_attempt_at<=$1
 AND (lease_until IS NULL OR lease_until<=$1) ORDER BY next_attempt_at LIMIT 20`, now)
	if err != nil {
		return
	}
	due, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	for _, id := range due {
		if ctx.Err() != nil {
			return
		}
		s.advance(ctx, id, workerBudget)
	}
}

func (s *Service) journalAlone(ctx context.Context, site, id, state string) {
	tx, err := s.cfg.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(context.Background())
	if journal(ctx, tx, site, id, state, "") == nil {
		_ = tx.Commit(ctx)
	}
}

// advance claims a due session with a lease and runs its steps. A session
// another worker holds is left alone.
func (s *Service) advance(ctx context.Context, id string, budget time.Duration) {
	r, err := s.claim(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		log.Printf("signup %s: claim failed", id)
		return
	}
	if err = s.run(ctx, &r, time.Now().Add(budget)); err != nil && !errors.Is(err, errLeaseLost) {
		log.Printf("signup %s: step run failed", id)
	}
}

func (s *Service) claim(ctx context.Context, id string) (record, error) {
	now := s.now()
	lease := now.Add(leaseTime)
	tag, err := s.cfg.DB.Exec(ctx, `UPDATE relay_signup_sessions SET lease_until=$2 WHERE session_id=$1 AND state='working'
 AND (lease_until IS NULL OR lease_until<=$3)`, id, lease, now)
	if err != nil {
		return record{}, err
	}
	if tag.RowsAffected() != 1 {
		return record{}, pgx.ErrNoRows
	}
	r, err := loadRecord(ctx, s.cfg.DB, id, false)
	if err != nil {
		return r, err
	}
	if r.lease == nil || !r.lease.Equal(lease) {
		return r, pgx.ErrNoRows
	}
	return r, nil
}
