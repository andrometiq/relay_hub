// Package numbers lets a site manage its own WhatsApp numbers with its site
// token. Every call finds the number in the database by site, so a key of
// another site is never trusted. No call takes or returns a credential: tokens,
// app secrets and PINs stay in the Hub.
package numbers

import (
	"context"
	"errors"
	"time"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5"
)

// Service runs the site number tools.
type Service struct {
	Store    *containers.Store
	WhatsApp *whatsapp.Service
	// Reload republishes runtime numbers after a change.
	Reload func(context.Context) error
	Now    func() time.Time
}

// Failure is a coded tool error that Core maps to its own text.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

var (
	errNotFound        = &Failure{"not_found", "This WhatsApp number is not connected to this site."}
	errNoNumber        = &Failure{"no_number", "This site has no WhatsApp number."}
	errChannelRequired = &Failure{"channel_required", "This site has several WhatsApp numbers; choose one."}
	errUnavailable     = &Failure{"hub_unavailable", "Relay Hub could not finish this. Try again in a few minutes."}
)

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

// key resolves the number of one call: the named key when it is a number of
// site, else the site's only number.
func (s *Service) key(ctx context.Context, site, key string) (string, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT channel_key FROM relay_channel_credentials WHERE channel='WhatsApp' AND site_id=$1 ORDER BY channel_key LIMIT 500`, site)
	if err != nil {
		return "", errUnavailable
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", errUnavailable
	}
	if key != "" {
		for _, own := range keys {
			if own == key {
				return key, nil
			}
		}
		return "", errNotFound
	}
	switch len(keys) {
	case 0:
		return "", errNoNumber
	case 1:
		return keys[0], nil
	}
	return "", errChannelRequired
}

// channel reads one number of site with its public settings only.
func (s *Service) channel(ctx context.Context, site, key string) (containers.Channel, error) {
	key, err := s.key(ctx, site, key)
	if err != nil {
		return containers.Channel{}, err
	}
	c, err := s.Store.GetChannel(ctx, containers.ChannelRef{Channel: "WhatsApp", Key: key})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.Site != site) {
		return containers.Channel{}, errNotFound
	}
	if err != nil {
		return containers.Channel{}, errUnavailable
	}
	return c, nil
}

// Health is what a site may see about one of its numbers.
type Health struct {
	Channel           string                  `json:"channel"`
	Label             string                  `json:"label"`
	DisplayNumber     string                  `json:"display_number"`
	Enabled           bool                    `json:"enabled"`
	ConnectionStatus  string                  `json:"connection_status"`
	PartnerStatus     string                  `json:"partner_status"`
	VerifiedAt        string                  `json:"verified_at"`
	CheckedAt         string                  `json:"checked_at"`
	CheckError        string                  `json:"check_error"`
	WebhookSubscribed *bool                   `json:"webhook_subscribed"`
	PINKept           bool                    `json:"pin_kept"`
	Settings          map[string]bool         `json:"settings"`
	Delivery          whatsapp.DeliveryHealth `json:"delivery"`
}

// Health reads the stored status and the delivery evidence of a number. It never contacts Meta.
func (s *Service) Health(ctx context.Context, site, key string) (Health, error) {
	c, err := s.channel(ctx, site, key)
	if err != nil {
		return Health{}, err
	}
	delivery, err := whatsapp.ReadDeliveryHealth(ctx, s.Store.DB, c.Key)
	if err != nil {
		return Health{}, errUnavailable
	}
	text := func(name string) string { value, _ := c.Config[name].(string); return value }
	flag := func(name string) bool { value, _ := c.Config[name].(bool); return value }
	health := Health{Channel: c.Key, Label: c.Label, DisplayNumber: text("display_number"), Enabled: c.Enabled,
		ConnectionStatus: text("connection_status"), PartnerStatus: text("partner_status"), VerifiedAt: text("verified_at"),
		CheckedAt: text("checked_at"), CheckError: text("check_error"),
		Settings: map[string]bool{"templates": flag("templates"), "sync_metadata": flag("sync_metadata")}, Delivery: delivery}
	if value, ok := c.Config["webhook_subscribed"].(bool); ok {
		health.WebhookSubscribed = &value
	}
	for _, name := range c.ConfiguredSecrets {
		health.PINKept = health.PINKept || name == "pin"
	}
	return health, nil
}
