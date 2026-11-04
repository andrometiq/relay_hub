package whatsapp

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DeliveryHealth is the observed webhook and Core callback evidence of one number.
type DeliveryHealth struct {
	Webhook     string `json:"webhook_received_at"`
	Callback    string `json:"core_confirmed_at"`
	Pending     int64  `json:"callback_pending"`
	Failed      int64  `json:"callback_failed"`
	Quarantined int64  `json:"callback_quarantined"`
}

// ReadDeliveryHealth reads the delivery evidence of a number. It never contacts Meta.
func ReadDeliveryHealth(ctx context.Context, db *pgxpool.Pool, account string) (DeliveryHealth, error) {
	var result DeliveryHealth
	err := db.QueryRow(ctx, `SELECT
   COALESCE((SELECT max(created_at)::text FROM relay_whatsapp_events WHERE account=$1),''),
   COALESCE(max(d.updated_at) FILTER (WHERE d.acknowledged)::text,''),
   count(*) FILTER (WHERE NOT d.acknowledged),
   count(*) FILTER (WHERE NOT d.acknowledged AND d.failures>0),
   count(*) FILTER (WHERE d.quarantined)
   FROM relay_whatsapp_callback_delivery d JOIN relay_whatsapp_events e ON e.id=d.event_id WHERE e.account=$1`, account).
		Scan(&result.Webhook, &result.Callback, &result.Pending, &result.Failed, &result.Quarantined)
	return result, err
}
