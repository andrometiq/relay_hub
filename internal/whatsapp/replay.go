package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotAssigned means the number of a stored event has no site yet.
var ErrNotAssigned = errors.New("the number is unknown or not assigned to a site; add or assign it first")

// ReplayUnassigned gives stored events of an unassigned or unknown number to
// the site that owns the number now. An unknown-number event is routed again
// by its phone number id; the original row is marked replayed.
func (s *Service) ReplayUnassigned(ctx context.Context, db *pgxpool.Pool, id int64) error {
	store, ok := s.Events.(PostgresEvents)
	if !ok {
		return errors.New("event store unavailable")
	}
	var account, container string
	var payload, raw []byte
	err := db.QueryRow(ctx, `SELECT account,coalesce(container_id,''),payload,coalesce(raw_payload,payload) FROM relay_whatsapp_events
 WHERE id=$1 AND site_id IS NULL AND replayed_at IS NULL`, id).Scan(&account, &container, &payload, &raw)
	if err != nil {
		return err
	}
	if account != "" {
		return assignStored(ctx, db, s, id, account)
	}
	var envelope webhookEnvelope
	if json.Unmarshal(payload, &envelope) != nil {
		return errors.New("stored event is not a WhatsApp envelope")
	}
	routed, _, err := routeEnvelope(envelope, containerAccounts(s.AccountsSnapshot(), container))
	if err != nil || len(routed) == 0 {
		return ErrNotAssigned
	}
	byKey := map[string]Account{}
	for _, a := range s.AccountsSnapshot() {
		byKey[a.ID] = a
	}
	for key := range routed {
		if byKey[key].SiteID == "" {
			return ErrNotAssigned
		}
	}
	for key, part := range routed {
		body, _ := json.Marshal(part)
		a := byKey[key]
		if err = store.SaveRouted(ctx, Route{Account: key, Site: a.SiteID, Container: a.ContainerID}, body, raw); err != nil {
			return err
		}
	}
	_, err = db.Exec(ctx, `UPDATE relay_whatsapp_events SET replayed_at=now() WHERE id=$1`, id)
	return err
}

// assignStored gives an event of a known but unassigned number to its current site.
func assignStored(ctx context.Context, db *pgxpool.Pool, s *Service, id int64, key string) error {
	a, ok := s.AccountByKey(key)
	if !ok || a.SiteID == "" {
		return ErrNotAssigned
	}
	tag, err := db.Exec(ctx, `UPDATE relay_whatsapp_events SET site_id=$2,replayed_at=now() WHERE id=$1 AND site_id IS NULL AND replayed_at IS NULL`, id, a.SiteID)
	if err == nil && tag.RowsAffected() != 1 {
		err = fmt.Errorf("event %d was already replayed", id)
	}
	if err == nil {
		select {
		case s.callbackWake <- struct{}{}:
		default:
		}
	}
	return err
}
