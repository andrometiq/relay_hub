package numbers

import (
	"context"

	"github.com/andrometiq/relay_hub/internal/containers"
)

var (
	errCoexistencePIN = &Failure{"not_supported", "The WhatsApp Business app keeps the two-step PIN of this number."}
	errPINUnknown     = &Failure{"outcome_unknown", "Relay does not know whether Meta took the new PIN. Reset the PIN again."}
)

// ResetPIN gives the number a new two-step verification PIN. The Hub makes
// the PIN, sets it at Meta and keeps it encrypted; it is never shown. A failed
// or unknown result leaves the stored PIN as it was, and another reset sets a
// fresh PIN at Meta and in the Hub together.
func (s *Service) ResetPIN(ctx context.Context, site, key string) (Health, error) {
	c, err := s.channel(ctx, site, key)
	if err != nil {
		return Health{}, err
	}
	account, err := s.account(ctx, c)
	if err != nil {
		return Health{}, err
	}
	if !c.Enabled {
		return Health{}, errDisabled
	}
	if coexistence, _ := c.Config["coexistence"].(bool); coexistence {
		return Health{}, errCoexistencePIN
	}
	pin, err := containers.NewPIN()
	if err != nil {
		return Health{}, errUnavailable
	}
	err = s.WhatsApp.SetTwoStepPIN(ctx, account, pin)
	switch metaOutcome(err) {
	case "reconnect":
		return Health{}, errReconnectRequired
	case "unavailable":
		return Health{}, errPINUnknown
	case "failed":
		return Health{}, &Failure{"meta_rejected", metaText(err, "Meta did not take the new PIN.")}
	}
	if err = s.Store.SetNumberPIN(ctx, site, c.Key, pin); err != nil {
		return Health{}, errPINUnknown
	}
	return s.Health(ctx, site, c.Key)
}
