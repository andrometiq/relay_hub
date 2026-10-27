package hubmcp

import (
	"context"
	"errors"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChannelRequired means the site has several channels of the provider and the call named none.
var ErrChannelRequired = errors.New("channel required: this site has several channels of this type; pass the channel key")

var errForeignChannel = errors.New("channel is not available to this site")

// siteNumbers lists the WhatsApp numbers the token's site uses. A legacy
// shared-token request is bound to one number when it is authenticated.
func siteNumbers(ctx context.Context, wa *whatsapp.Service) ([]whatsapp.Account, error) {
	if bound, ok := ctx.Value(accountKey{}).(whatsapp.Account); ok {
		return []whatsapp.Account{bound}, nil
	}
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	if !ok {
		return nil, errors.New("account credential required")
	}
	numbers := []whatsapp.Account{}
	for _, a := range wa.AccountsSnapshot() {
		if a.SiteID == p.Account {
			numbers = append(numbers, a)
		}
	}
	return numbers, nil
}

// whatsappNumber resolves the number of one call: the named channel, else
// the site's only number.
func whatsappNumber(ctx context.Context, wa *whatsapp.Service, channel string) (whatsapp.Account, error) {
	numbers, err := siteNumbers(ctx, wa)
	if err != nil {
		return whatsapp.Account{}, err
	}
	if channel != "" {
		for _, a := range numbers {
			if a.ID == channel {
				return a, nil
			}
		}
		return whatsapp.Account{}, errForeignChannel
	}
	switch len(numbers) {
	case 1:
		return numbers[0], nil
	case 0:
		return whatsapp.Account{}, errors.New("this site has no active WhatsApp number")
	}
	return whatsapp.Account{}, ErrChannelRequired
}

// whatsappScope is the delivery scope of one call: the named number, or every number of the site.
func whatsappScope(ctx context.Context, wa *whatsapp.Service, channel string) (whatsapp.Account, error) {
	if channel != "" {
		return whatsappNumber(ctx, wa, channel)
	}
	if bound, ok := ctx.Value(accountKey{}).(whatsapp.Account); ok {
		return bound, nil
	}
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	if !ok {
		return whatsapp.Account{}, errors.New("account credential required")
	}
	return whatsapp.Account{SiteID: p.Account}, nil
}

// channelKey resolves the Telegram bot or mailbox of one call: the named
// channel, else the site's only channel of that provider.
func channelKey(ctx context.Context, registry *channels.Registry, provider, channel string) (string, error) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	if !ok {
		return "", errors.New("account credential required")
	}
	if channel != "" {
		if registry.Binding(provider, channel).Site != p.Account {
			return "", errForeignChannel
		}
		return channel, nil
	}
	keys := registry.SiteKeys(provider, p.Account)
	switch len(keys) {
	case 1:
		return keys[0], nil
	case 0:
		return "", errors.New("this site has no active " + provider + " channel")
	}
	return "", ErrChannelRequired
}

// deliveryKey checks an optional Telegram or Email channel for a delivery read
// or change; an empty result means every channel of the site.
func deliveryKey(ctx context.Context, registry *channels.Registry, channel string) (string, string, error) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	if !ok {
		return "", "", errors.New("account credential required")
	}
	if channel != "" && registry.Binding("Telegram", channel).Site != p.Account && registry.Binding("Email", channel).Site != p.Account {
		return "", "", errForeignChannel
	}
	return channel, p.Account, nil
}

// Tool inputs with an optional channel key. The embedded input is what the
// provider and the outbox see, so the key never changes a stored request.
type whatsappInput struct {
	whatsapp.Input
	Channel string `json:"channel,omitempty"`
}

type telegramTyping struct {
	telegram.TypingInput
	Channel string `json:"channel,omitempty"`
}

type telegramMessage struct {
	telegram.MessageInput
	Channel string `json:"channel,omitempty"`
}

type telegramGroup struct {
	telegram.GroupInput
	Channel string `json:"channel,omitempty"`
}

type emailMessage struct {
	channels.EmailInput
	Channel string `json:"channel,omitempty"`
}

// siteChannels lists the channels assigned to a site, without credentials.
func siteChannels(ctx context.Context, db *pgxpool.Pool, site string) ([]map[string]any, error) {
	rows, err := db.Query(ctx, `SELECT c.channel_key,c.channel,c.label,coalesce(c.address,''),c.container_id,c.enabled AND k.enabled,c.config
 FROM relay_channel_credentials c JOIN relay_containers k USING(container_id) WHERE c.site_id=$1 ORDER BY c.channel,c.channel_key LIMIT 500`, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var key, provider, label, address, container string
		var enabled bool
		var config map[string]any
		if err = rows.Scan(&key, &provider, &label, &address, &container, &enabled, &config); err != nil {
			return nil, err
		}
		text := func(name string) string { value, _ := config[name].(string); return value }
		flag := func(name string) bool { value, _ := config[name].(bool); return value }
		result = append(result, map[string]any{"key": key, "provider": provider, "label": label, "address": address,
			"display_number": text("display_number"), "waba_id": text("waba_id"), "container": container, "enabled": enabled,
			"capabilities": map[string]bool{"templates": flag("templates"), "groups": flag("groups"), "calls": flag("calls")}})
	}
	return result, rows.Err()
}
