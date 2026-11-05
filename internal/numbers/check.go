package numbers

import (
	"context"
	"errors"

	"github.com/andrometiq/relay_hub/internal/containers"
	relayruntime "github.com/andrometiq/relay_hub/internal/runtime"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5"
)

var (
	errDisabled          = &Failure{Code: "number_disabled", Message: "Turn the number on first."}
	errReconnectRequired = &Failure{Code: "reconnect_required", Message: "Meta no longer accepts Relay's access to this number. Connect it again with Facebook."}
)

// Check is the result of a connection check.
type Check struct {
	Health
	ReconnectRequired bool           `json:"reconnect_required"`
	Profile           map[string]any `json:"profile,omitempty"`
}

// account reads the number with its token for one Meta call. A number without
// a token, or one Meta removed, must be connected again.
func (s *Service) account(ctx context.Context, c containers.Channel) (whatsapp.Account, error) {
	if c.Config["partner_status"] == "removed" {
		return whatsapp.Account{}, errReconnectRequired
	}
	credential, err := s.Store.Credential(ctx, "WhatsApp", c.Key)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && credential.Secret == "") {
		return whatsapp.Account{}, errReconnectRequired
	}
	if err != nil {
		return whatsapp.Account{}, errUnavailable
	}
	account, err := relayruntime.WhatsAppAccount(credential)
	if err != nil {
		return whatsapp.Account{}, errUnavailable
	}
	return account, nil
}

// Check reads the number from Meta with its stored token and records the
// result. With repair it also subscribes the app to the WABA's webhooks again.
// A token Meta refuses is reconnect_required: only Embedded Signup gives a new one.
func (s *Service) Check(ctx context.Context, site, key string, repair bool) (Check, error) {
	c, err := s.channel(ctx, site, key)
	if err != nil {
		return Check{}, err
	}
	values := map[string]any{"checked_at": s.now().Format(timeFormat), "check_error": ""}
	account, err := s.account(ctx, c)
	if errors.Is(err, errReconnectRequired) {
		values["connection_status"] = "reconnect_required"
		return s.recordCheck(ctx, site, c.Key, values, nil)
	}
	if err != nil {
		return Check{}, err
	}
	if !c.Enabled {
		return Check{}, errDisabled
	}
	phone, err := s.WhatsApp.NumberStatus(ctx, account)
	switch metaOutcome(err) {
	case "reconnect":
		values["connection_status"] = "reconnect_required"
		return s.recordCheck(ctx, site, c.Key, values, nil)
	case "unavailable":
		return Check{}, &Failure{Code: "meta_unavailable", Message: "Meta is not answering. Try again in a few minutes."}
	case "failed":
		values["connection_status"], values["check_error"] = "failed", metaText(err, "Meta refused the check.")
		return s.recordCheck(ctx, site, c.Key, values, nil)
	}
	values["connection_status"], values["verified_at"] = "connected", values["checked_at"]
	if subscribed, err := s.WhatsApp.WebhookSubscription(ctx, account, repair); err == nil {
		values["webhook_subscribed"] = subscribed
	} else {
		values["check_error"] = metaText(err, "Relay could not read the webhook subscription.")
	}
	return s.recordCheck(ctx, site, c.Key, values, phoneProfile(phone))
}

func (s *Service) recordCheck(ctx context.Context, site, key string, values map[string]any, profile map[string]any) (Check, error) {
	if err := s.Store.UpdateSiteValues(ctx, site, containers.ChannelRef{Channel: "WhatsApp", Key: key}, nil, values); err != nil {
		return Check{}, errUnavailable
	}
	health, err := s.Health(ctx, site, key)
	if err != nil {
		return Check{}, err
	}
	return Check{Health: health, ReconnectRequired: health.ConnectionStatus == "reconnect_required", Profile: profile}, nil
}

// metaOutcome classifies a Graph failure: "" is success, "reconnect" a token
// or access Meta refuses, "unavailable" a failure a retry can fix.
func metaOutcome(err error) string {
	if err == nil {
		return ""
	}
	var provider *whatsapp.ProviderError
	if !errors.As(err, &provider) {
		return "unavailable"
	}
	switch {
	case provider.Code == 190 || provider.Code == 10 || provider.Code == 200 || (provider.Code == 100 && provider.Subcode == 33):
		return "reconnect"
	case provider.Retryable() || provider.Status >= 500:
		return "unavailable"
	}
	return "failed"
}

// metaText is Meta's safe user text, else fallback.
func metaText(err error, fallback string) string {
	var provider *whatsapp.ProviderError
	if errors.As(err, &provider) && provider.Message != "" {
		return fallback + " Meta says: " + provider.Message
	}
	return fallback
}

// phoneProfile keeps the public fields of a Graph phone number.
func phoneProfile(phone map[string]any) map[string]any {
	profile := map[string]any{}
	for _, name := range []string{"display_phone_number", "verified_name", "name_status", "status", "quality_rating"} {
		if value, _ := phone[name].(string); value != "" {
			profile[name] = value
		}
	}
	return profile
}
