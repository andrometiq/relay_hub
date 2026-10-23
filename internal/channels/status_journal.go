package channels

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
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
}

// PendingStatusReceipts is used while the caller holds an account dispatch lock.
// Acknowledgement is per row, so concurrent commits cannot fall behind a cursor.
func PendingStatusReceipts(ctx context.Context, db *pgx.Conn, account string) ([]StatusReceipt, error) {
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
		if size+len(raw)+1 > 512<<10 {
			break
		}
		items = append(items, row)
		size += len(raw) + 1
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
