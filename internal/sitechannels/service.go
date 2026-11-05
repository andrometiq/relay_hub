package sitechannels

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

// Clients builds provider clients for credentials a site sends, before they are stored.
type Clients interface {
	Telegram(token string) (*telegram.Client, error)
	Email(cfg relayemail.Config) (*relayemail.Client, error)
}

// ProviderClients reaches the real Telegram and mail servers. Mail goes only to public hosts.
type ProviderClients struct{ Hosts relayemail.PublicHosts }

func (p ProviderClients) Telegram(token string) (*telegram.Client, error) {
	return telegram.New(telegram.Config{AccountID: "connect", BotToken: token, Enabled: true})
}

func (p ProviderClients) Email(cfg relayemail.Config) (*relayemail.Client, error) {
	return relayemail.NewWithDialer(cfg, p.Hosts.Dial)
}

// Service runs the site tools for Telegram bots and mailboxes. A site sends
// a token or password only to connect or change a channel; no tool returns one.
type Service struct {
	Store    *containers.Store
	Registry *channels.Registry
	Clients  Clients
	// Hosts checks the mail servers a site gives before any connection.
	Hosts relayemail.PublicHosts
	// Reload republishes runtime channels after a change.
	Reload func(context.Context) error
	// PublicURL and TelegramMaster register Telegram webhooks.
	PublicURL, TelegramMaster string
	Now                       func() time.Time
}

// Each site may try connect or a new credential this many times an hour, and check twice as often.
const (
	connectLimit = 10
	checkLimit   = 30
)

var (
	errRateLimited  = &Failure{"rate_limited", "Too many tries. Wait an hour, then try again."}
	errNotAvailable = &Failure{"not_available", "This Relay Hub does not let sites connect channels yet. Ask your Relay provider."}
	errInUse        = &Failure{"in_use", "This bot or mailbox is already connected to Relay."}
	errConfirm      = &Failure{"confirm_required", "Confirm the removal first."}
	errPendingSends = &Failure{"pending_sends", "Messages are still being sent from this channel. Cancel or resolve them, then remove it."}
	errNoChange     = &Failure{"invalid_request", "Choose a setting to change."}
	errDisabled     = &Failure{"channel_disabled", "Turn the channel on first."}
)

const timeFormat = time.RFC3339

func (s *Service) now() string {
	if s.Now == nil {
		return time.Now().UTC().Format(timeFormat)
	}
	return s.Now().UTC().Format(timeFormat)
}

// allow counts one try of action by site.
func (s *Service) allow(ctx context.Context, site, provider, action string, limit int) error {
	allowed, err := s.Store.AllowSiteAttempt(ctx, site, provider, action, time.Hour, limit)
	if err != nil {
		return ErrUnavailable
	}
	if !allowed {
		return errRateLimited
	}
	return nil
}

// audit records the outcome of a change; a failed write only loses the audit row.
func (s *Service) audit(ctx context.Context, site string, ref containers.ChannelRef, action string, err error) {
	outcome, code := "completed", ""
	var failure *Failure
	if errors.As(err, &failure) {
		outcome, code = "rejected", failure.Code
	} else if err != nil {
		outcome, code = "failed", ErrUnavailable.Code
	}
	if e := s.Store.AuditSite(ctx, site, ref, action, outcome, code); e != nil {
		log.Printf("audit %s of %s: %v", action, site, e)
	}
}

// reload republishes the runtime. A change is already committed, so a failure is logged and the next reload applies it.
func (s *Service) reload(ctx context.Context, site, key string) {
	if err := s.Reload(ctx); err != nil {
		log.Printf("channel %s of %s: runtime reload failed: %v", key, site, err)
	}
}

// stored maps a store error of a connect or a change to its failure.
func stored(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, containers.ErrChannelInUse) || errors.Is(err, containers.ErrAddressInUse) || errors.Is(err, containers.ErrConflict):
		return errInUse
	case errors.Is(err, containers.ErrNoSignupContainer):
		return errNotAvailable
	case errors.Is(err, containers.ErrPendingSends):
		return errPendingSends
	case errors.Is(err, containers.ErrAdminOff):
		return ErrAdminOff
	}
	return ErrUnavailable
}

func text(config map[string]any, name string) string { value, _ := config[name].(string); return value }

func saved(c containers.Channel, name string) bool {
	for _, secret := range c.ConfiguredSecrets {
		if secret == name {
			return true
		}
	}
	return false
}
