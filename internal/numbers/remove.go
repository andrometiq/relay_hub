package numbers

import (
	"context"
	"errors"
	"log"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

var (
	errConfirm      = &Failure{Code: "confirm_required", Message: "Confirm that the number should be removed."}
	errPendingSends = &Failure{Code: "pending_sends", Message: "Messages are still being sent from this number. Cancel or resolve them, then remove the number."}
)

// Removal says what happened at Meta when a number left the site.
type Removal struct {
	Channel      string `json:"channel"`
	Removed      bool   `json:"removed"`
	Deregistered bool   `json:"deregistered"`
	Unsubscribed bool   `json:"unsubscribed"`
}

// Remove takes a number away from the site. The Hub first wipes its
// credentials and unlinks it, so the site stops using it even when Meta does
// not answer. Then it deregisters the number at Meta, so it can move, and it
// unsubscribes the app when no other Hub number uses the WABA. A Business app
// number stays registered; it is disconnected in the WhatsApp Business app.
func (s *Service) Remove(ctx context.Context, site, key string, confirm bool) (Removal, error) {
	if !confirm {
		return Removal{}, errConfirm
	}
	c, err := s.channel(ctx, site, key)
	if err != nil {
		return Removal{}, err
	}
	account, accountErr := s.account(ctx, c)
	err = s.Store.RemoveSiteChannel(ctx, site, containers.ChannelRef{Channel: "WhatsApp", Key: c.Key})
	if errors.Is(err, containers.ErrPendingSends) {
		return Removal{}, errPendingSends
	}
	if err != nil {
		return Removal{}, errUnavailable
	}
	// The removal is committed; a failed reload must not skip the deregistration, which a retry can no longer do.
	if err = s.Reload(ctx); err != nil {
		log.Printf("remove %s from %s: runtime reload failed: %v", c.Key, site, err)
	}
	removal := Removal{Channel: c.Key, Removed: true}
	if accountErr != nil {
		return removal, nil
	}
	if coexistence, _ := c.Config["coexistence"].(bool); !coexistence {
		removal.Deregistered = s.WhatsApp.Deregister(ctx, account) == nil
	}
	removal.Unsubscribed = s.unsubscribe(ctx, account)
	return removal, nil
}

// unsubscribe stops the app's webhooks of the WABA once no Hub number uses it.
func (s *Service) unsubscribe(ctx context.Context, account whatsapp.Account) bool {
	used, err := s.Store.WABAInUse(ctx, account.WABAID, account.ID)
	if err != nil || used {
		return false
	}
	return s.WhatsApp.Unsubscribe(ctx, account) == nil
}
