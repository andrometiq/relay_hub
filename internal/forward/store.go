// Package forward sends a copy of each verified provider webhook to other URLs,
// for example an older system that still needs the same events.
package forward

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxTargets bounds the forward targets of one container, bot or mailbox.
const MaxTargets = 5

// Headers are the request headers that a forward copies. Only these, so the
// target sees what the provider sent and can check the same signature.
var Headers = []string{"Content-Type", "User-Agent", "X-Hub-Signature-256", "X-Hub-Signature", "X-Telegram-Bot-Api-Secret-Token", "X-Relay-Email-Secret"}

var (
	ErrInvalidURL   = errors.New("use an https URL; http is allowed only for loopback, private or Tailscale (100.64.0.0/10) addresses")
	ErrInvalidScope = errors.New("unknown provider, container or channel")
	ErrTooMany      = errors.New("at most 5 forward targets")
	scopeKey        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	tailscale       = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
)

// Scope names whose webhooks are forwarded: a WhatsApp container, or a
// Telegram bot or mailbox by its channel key.
type Scope struct {
	Provider, Key string
}

func (s Scope) valid() bool {
	return (s.Provider == "WhatsApp" || s.Provider == "Telegram" || s.Provider == "Email") && scopeKey.MatchString(s.Key)
}

type Target struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// Record is one forward of one received request to one target.
type Record struct {
	ID          int64      `json:"id"`
	TargetID    int64      `json:"target_id"`
	URL         string     `json:"url"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	StatusCode  int        `json:"status_code"`
	Error       string     `json:"error"`
	LatencyMS   int        `json:"latency_ms"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

type Store struct {
	DB   *pgxpool.Pool
	Wake chan struct{}
}

// ValidateURL accepts https, and http only to an address that stays inside
// this machine, the private network or Tailscale.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return ErrInvalidURL
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" {
		return ErrInvalidURL
	}
	if u.Hostname() == "localhost" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || tailscale.Contains(ip)) {
		return nil
	}
	return ErrInvalidURL
}

func (s Store) exists(ctx context.Context, scope Scope) error {
	if !scope.valid() {
		return ErrInvalidScope
	}
	var found bool
	err := s.DB.QueryRow(ctx, `SELECT CASE WHEN $1='WhatsApp' THEN EXISTS(SELECT 1 FROM relay_containers WHERE container_id=$2)
 ELSE EXISTS(SELECT 1 FROM relay_channel_credentials WHERE channel=$1 AND channel_key=$2) END`, scope.Provider, scope.Key).Scan(&found)
	if err == nil && !found {
		err = ErrInvalidScope
	}
	return err
}

func (s Store) Targets(ctx context.Context, scope Scope) ([]Target, error) {
	if err := s.exists(ctx, scope); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id,url,created_at FROM relay_forward_targets WHERE provider=$1 AND scope_key=$2 ORDER BY id`, scope.Provider, scope.Key)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Target])
}

// AddTarget stores a target. Adding a URL that is already there is a no-op.
func (s Store) AddTarget(ctx context.Context, scope Scope, address string) (Target, error) {
	if err := ValidateURL(address); err != nil {
		return Target{}, err
	}
	if err := s.exists(ctx, scope); err != nil {
		return Target{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Target{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('relay_forward_targets/'||$1||'/'||$2))", scope.Provider, scope.Key); err != nil {
		return Target{}, err
	}
	var target Target
	err = tx.QueryRow(ctx, `SELECT id,url,created_at FROM relay_forward_targets WHERE provider=$1 AND scope_key=$2 AND url=$3`, scope.Provider, scope.Key, address).Scan(&target.ID, &target.URL, &target.CreatedAt)
	if err == nil {
		return target, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Target{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM relay_forward_targets WHERE provider=$1 AND scope_key=$2`, scope.Provider, scope.Key).Scan(&count); err != nil {
		return Target{}, err
	}
	if count >= MaxTargets {
		return Target{}, ErrTooMany
	}
	err = tx.QueryRow(ctx, `INSERT INTO relay_forward_targets(provider,scope_key,url) VALUES($1,$2,$3) RETURNING id,url,created_at`, scope.Provider, scope.Key, address).Scan(&target.ID, &target.URL, &target.CreatedAt)
	if err != nil {
		return Target{}, err
	}
	return target, tx.Commit(ctx)
}

// RemoveTarget deletes a target and its records.
func (s Store) RemoveTarget(ctx context.Context, scope Scope, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM relay_forward_targets WHERE id=$1 AND provider=$2 AND scope_key=$3`, id, scope.Provider, scope.Key)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

// Capture queues one verified request for every target of the scope. Without
// targets it writes nothing.
func (s Store) Capture(ctx context.Context, scope Scope, header http.Header, body []byte) error {
	copied := map[string]string{}
	for _, name := range Headers {
		if value := header.Get(name); value != "" {
			copied[name] = value
		}
	}
	tag, err := s.DB.Exec(ctx, `INSERT INTO relay_forwards(target_id,headers,body)
 SELECT id,$3,$4 FROM relay_forward_targets WHERE provider=$1 AND scope_key=$2 ORDER BY id`, scope.Provider, scope.Key, copied, body)
	if err == nil && tag.RowsAffected() > 0 {
		select {
		case s.Wake <- struct{}{}:
		default:
		}
	}
	return err
}

// Queue captures like Capture, and only logs a failure, so the answer to the
// provider never depends on forwarding.
func (s Store) Queue(ctx context.Context, scope Scope, header http.Header, body []byte) {
	if err := s.Capture(ctx, scope, header, body); err != nil {
		log.Printf("%s webhook forward not queued", scope.Provider)
	}
}

// Records lists the newest forwards of a scope, before an id when given.
func (s Store) Records(ctx context.Context, scope Scope, before int64, limit int) ([]Record, map[string]int64, error) {
	if err := s.exists(ctx, scope); err != nil {
		return nil, nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT f.id,f.target_id,t.url,f.state,f.attempts,f.status_code,f.error,f.latency_ms,f.created_at,f.delivered_at
 FROM relay_forwards f JOIN relay_forward_targets t ON t.id=f.target_id
 WHERE t.provider=$1 AND t.scope_key=$2 AND ($3::bigint=0 OR f.id<$3) ORDER BY f.id DESC LIMIT $4`, scope.Provider, scope.Key, before, limit)
	if err != nil {
		return nil, nil, err
	}
	records, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Record])
	if err != nil {
		return nil, nil, err
	}
	counts := map[string]int64{"pending": 0, "delivered": 0, "failed": 0}
	rows, err = s.DB.Query(ctx, `SELECT f.state,count(*) FROM relay_forwards f JOIN relay_forward_targets t ON t.id=f.target_id
 WHERE t.provider=$1 AND t.scope_key=$2 GROUP BY f.state`, scope.Provider, scope.Key)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err = rows.Scan(&state, &count); err != nil {
			return nil, nil, err
		}
		counts[state] = count
	}
	return records, counts, rows.Err()
}
