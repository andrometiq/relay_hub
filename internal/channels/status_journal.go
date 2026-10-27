package channels

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/jackc/pgx/v5"
	"slices"
	"time"
)

type StatusReceipt struct {
	ID         int64           `json:"id"`
	DeliveryID int64           `json:"delivery_id"`
	RequestKey string          `json:"request_key"`
	Operation  string          `json:"operation"`
	State      string          `json:"state"`
	Receipt    json.RawMessage `json:"receipt"`
	OccurredAt time.Time       `json:"occurred_at"`
	ChannelKey string          `json:"channel_key,omitempty"`
	To         string          `json:"to,omitempty"`
}

// routingReserve is room per row for channel_key and to, added after the batch is read.
const routingReserve = 330

// PendingStatusReceipts is used while the caller holds an account dispatch lock.
// Acknowledgement is per row, so concurrent commits cannot fall behind a cursor.
func PendingStatusReceipts(ctx context.Context, db *pgx.Conn, account string) ([]StatusReceipt, error) {
	if err := projectEmailStatuses(ctx, db, account); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT id,delivery_id,request_key,operation,state,receipt,occurred_at FROM relay_channel_status_journal WHERE account=$1 AND acknowledged_at IS NULL ORDER BY id LIMIT 500`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []StatusReceipt{}
	size := 2
	for rows.Next() {
		var row StatusReceipt
		if err = rows.Scan(&row.ID, &row.DeliveryID, &row.RequestKey, &row.Operation, &row.State, &row.Receipt, &row.OccurredAt); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		if size+len(raw)+1+routingReserve > 512<<10 {
			break
		}
		items = append(items, row)
		size += len(raw) + 1 + routingReserve
	}
	return items, rows.Err()
}

func AcknowledgeStatusReceipts(ctx context.Context, db *pgx.Conn, account string, ids []int64) error {
	if account == "" || len(ids) < 1 || len(ids) > 500 {
		return errors.New("invalid status acknowledgement")
	}
	for _, id := range ids {
		if id < 1 {
			return errors.New("invalid status identity")
		}
	}
	_, err := db.Exec(ctx, `UPDATE relay_channel_status_journal SET acknowledged_at=now() WHERE account=$1 AND id=ANY($2) AND acknowledged_at IS NULL`, account, ids)
	return err
}

func saveEmailStatuses(ctx context.Context, tx pgx.Tx, account string, statuses []relayemail.DeliveryStatus) error {
	statuses = slices.Clone(statuses)
	slices.SortFunc(statuses, func(a, b relayemail.DeliveryStatus) int {
		return cmp.Or(cmp.Compare(a.MessageID, b.MessageID), cmp.Compare(a.Recipient, b.Recipient))
	})
	for _, status := range statuses {
		rank := relayemail.DeliveryRank(status.State)
		if rank == 0 || !relayemail.ValidMessageID(status.MessageID) || len(status.Recipient) == 0 || len(status.Recipient) > relayemail.MaxRecipientBytes {
			return errors.New("invalid delivery evidence")
		}
		_, err := tx.Exec(ctx, `INSERT INTO relay_email_delivery_evidence(account,message_id,recipient,state,code,rank)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account,message_id,recipient) DO UPDATE
 SET state=EXCLUDED.state,code=EXCLUDED.code,rank=EXCLUDED.rank,occurred_at=now()
 WHERE relay_email_delivery_evidence.rank < EXCLUDED.rank`, account, status.MessageID, status.Recipient, status.State, status.Code, rank)
		if err != nil {
			return err
		}
	}
	return nil
}

// Evidence can arrive before the SMTP result commits. Keep it pending until a
// unique account-scoped message and envelope recipient match becomes available.
func projectEmailStatuses(ctx context.Context, db *pgx.Conn, account string) error {
	_, err := db.Exec(ctx, `WITH candidates AS (
 SELECT e.account,e.message_id,e.recipient,e.rank,e.state,e.code,e.occurred_at,
 o.id,o.request_key,o.operation
 FROM relay_email_delivery_evidence e
 JOIN relay_channel_outbox o ON o.account=e.account AND o.operation='email.send' AND o.result->>'message_id'=e.message_id
 WHERE e.account=$1 AND e.rank>e.published_rank AND ('email:'||e.recipient)=ANY(o.destinations)
 AND (SELECT count(*) FROM relay_channel_outbox x WHERE x.account=e.account AND x.operation='email.send' AND x.result->>'message_id'=e.message_id)=1
 ORDER BY e.message_id,e.recipient LIMIT 500 FOR UPDATE OF e SKIP LOCKED
 ), published AS (
 UPDATE relay_email_delivery_evidence e SET published_rank=c.rank FROM candidates c
 WHERE e.account=c.account AND e.message_id=c.message_id AND e.recipient=c.recipient
 RETURNING c.*
 ) INSERT INTO relay_channel_status_journal(delivery_id,account,request_key,operation,state,receipt,occurred_at)
 SELECT id,account,request_key,operation,state,jsonb_build_object('message_id',message_id,'recipient',recipient,'delivery_status',state,'status_code',code),occurred_at FROM published`, account)
	return err
}
