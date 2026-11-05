// Package numbers lets a site manage its own WhatsApp numbers with its site
// token. Every call finds the number in the database by site, so a key of
// another site is never trusted. No call takes or returns a credential: tokens,
// app secrets and PINs stay in the Hub.
package numbers

import (
	"context"
	"time"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/sitechannels"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
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
type Failure = sitechannels.Failure

var (
	errUnavailable = sitechannels.ErrUnavailable
	missingNumber  = sitechannels.Missing{
		NotFound: &Failure{Code: "not_found", Message: "This WhatsApp number is not connected to this site."},
		None:     &Failure{Code: "no_number", Message: "This site has no WhatsApp number."},
		Choose:   &Failure{Code: "channel_required", Message: "This site has several WhatsApp numbers; choose one."},
	}
)

const timeFormat = time.RFC3339

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

// channel reads one number of site with its public settings only.
func (s *Service) channel(ctx context.Context, site, key string) (containers.Channel, error) {
	return sitechannels.Channel(ctx, s.Store, "WhatsApp", site, key, missingNumber)
}

// Health is what a site may see about one of its numbers.
type Health struct {
	Channel           string                  `json:"channel"`
	Label             string                  `json:"label"`
	DisplayNumber     string                  `json:"display_number"`
	Enabled           bool                    `json:"enabled"`
	AdminOff          bool                    `json:"admin_off"`
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
	health := Health{Channel: c.Key, Label: c.Label, DisplayNumber: text("display_number"), Enabled: c.Enabled, AdminOff: c.AdminOff,
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
