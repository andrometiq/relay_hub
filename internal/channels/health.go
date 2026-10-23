package channels

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const healthRowLimit = 1000
const healthQueryTimeout = 2 * time.Second

// Limit before joining callback rows; completed outbox history is excluded by
// the matching partial index. One extra row detects a truncated sample. The
// lateral LIMIT keeps the planner from scanning the entire callback table.
const inboundHealthSQL = `WITH candidates AS MATERIALIZED (
 SELECT id,received_at FROM relay_channel_events
 WHERE account=$1 AND channel=$2 AND direction='inbound'
 ORDER BY received_at DESC NULLS LAST,id DESC LIMIT $3
), sample AS (SELECT * FROM candidates ORDER BY received_at DESC NULLS LAST,id DESC LIMIT $4)
 SELECT COALESCE(max(e.received_at)::text,''),COALESCE(max(d.updated_at) FILTER(WHERE d.acknowledged)::text,''),
 count(*) FILTER(WHERE NOT d.acknowledged),count(*) FILTER(WHERE NOT d.acknowledged AND d.failures>0),count(*) FILTER(WHERE d.quarantined),
 (SELECT count(*)>$4 FROM candidates)
 FROM sample e LEFT JOIN LATERAL (
 SELECT updated_at,acknowledged,failures,quarantined FROM relay_channel_callback_delivery
 WHERE event_id=e.id LIMIT 1
 ) d ON true`

const outboxHealthSQL = `WITH candidates AS MATERIALIZED (
 SELECT id,state,attempts,provider_error,next_attempt FROM relay_channel_outbox
 WHERE account=$1 AND split_part(operation,'.',1)=$2 AND state IN ('queued','dispatching','uncertain','rejected')
 ORDER BY id DESC LIMIT $3
), sample AS (SELECT * FROM candidates ORDER BY id DESC LIMIT $4)
 SELECT count(*) FILTER(WHERE state IN ('queued','dispatching')),count(*) FILTER(WHERE state='uncertain'),count(*) FILTER(WHERE state='rejected'),
 count(*) FILTER(WHERE state='queued' AND attempts>0),count(*) FILTER(WHERE state='queued' AND provider_error->>'ErrorCode'='429'),
 COALESCE(min(next_attempt) FILTER(WHERE state='queued' AND next_attempt>now())::text,''),
 (SELECT count(*)>$4 FROM candidates)
 FROM sample`

type Health struct {
	SampleLimit      int             `json:"sample_limit"`
	InboundTruncated bool            `json:"inbound_truncated"`
	OutboxTruncated  bool            `json:"outbox_truncated"`
	LastUpdate       string          `json:"webhook_received_at"`
	Confirmed        string          `json:"core_confirmed_at"`
	Pending          int64           `json:"callback_pending"`
	Failed           int64           `json:"callback_failed"`
	Quarantined      int64           `json:"callback_quarantined"`
	DeliveryPending  int64           `json:"delivery_pending"`
	Uncertain        int64           `json:"delivery_uncertain"`
	Rejected         int64           `json:"delivery_rejected"`
	Retrying         int64           `json:"delivery_retrying"`
	RateLimited      int64           `json:"delivery_rate_limited"`
	NextAttempt      string          `json:"next_attempt"`
	Mailboxes        []MailboxHealth `json:"mailboxes"`
}

func AccountHealth(ctx context.Context, db *pgxpool.Pool, account, channel string) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, healthQueryTimeout)
	defer cancel()
	h := Health{Mailboxes: []MailboxHealth{}, SampleLimit: healthRowLimit}
	err := db.QueryRow(ctx, inboundHealthSQL, account, channel, healthRowLimit+1, healthRowLimit).Scan(
		&h.LastUpdate, &h.Confirmed, &h.Pending, &h.Failed, &h.Quarantined, &h.InboundTruncated)
	if err != nil {
		return h, err
	}
	err = db.QueryRow(ctx, outboxHealthSQL, account, strings.ToLower(channel), healthRowLimit+1, healthRowLimit).Scan(
		&h.DeliveryPending, &h.Uncertain, &h.Rejected, &h.Retrying, &h.RateLimited, &h.NextAttempt, &h.OutboxTruncated)
	if err == nil && channel == "Email" {
		h.Mailboxes, err = MailboxStatus(ctx, db, account)
	}
	return h, err
}
