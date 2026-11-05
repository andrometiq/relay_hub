package hubmcp

import (
	"context"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/sitechannels"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type channelHealthInput struct {
	Provider string `json:"provider,omitempty"`
}

var (
	healthProviders    = []string{"WhatsApp", "Telegram", "Email"}
	errUnknownProvider = &sitechannels.Failure{Code: "invalid_request", Message: "Choose WhatsApp, Telegram or Email."}
)

// addChannelHealth registers channels.health, so a site reads the health of all its channels in one call instead of one call per channel.
func addChannelHealth(server *mcp.Server, siteOf sitechannels.SiteOf, opts Options) {
	mcp.AddTool(server, &mcp.Tool{Name: "channels.health", Description: "Read the stored health of every WhatsApp number, Telegram bot and mailbox of this site, or of one provider. The answer has one list per provider. No credentials are returned and no provider is called."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in channelHealthInput) (*mcp.CallToolResult, map[string]any, error) {
			return sitechannels.Run(ctx, siteOf, func(site string) (any, error) { return channelHealth(ctx, opts, site, in.Provider) })
		})
}

// channelHealth lists the health of the site's channels by provider. A requested provider is always in the answer, with an empty list when the site has none.
func channelHealth(ctx context.Context, opts Options, site, provider string) (map[string][]any, error) {
	result := map[string][]any{}
	for _, name := range healthProviders {
		if provider == "" || provider == name {
			result[name] = []any{}
		}
	}
	if len(result) == 0 {
		return nil, errUnknownProvider
	}
	var store *containers.Store
	if opts.Channels != nil {
		store = opts.Channels.Store
	} else {
		store = opts.Numbers.Store
	}
	list, err := store.Channels(ctx, containers.ChannelFilter{Site: site})
	if err != nil {
		return nil, sitechannels.ErrUnavailable
	}
	for _, c := range list {
		if _, wanted := result[c.Channel]; !wanted {
			continue
		}
		health, err := healthOf(ctx, opts, c)
		if err != nil {
			return nil, err
		}
		if health != nil {
			result[c.Channel] = append(result[c.Channel], health)
		}
	}
	return result, nil
}

// healthOf is the health of one channel the site owns, or nil when this Hub has no tools for its provider.
func healthOf(ctx context.Context, opts Options, c containers.Channel) (any, error) {
	switch {
	case c.Channel == "WhatsApp" && opts.Numbers != nil:
		return opts.Numbers.HealthOf(ctx, c)
	case c.Channel == "Telegram" && opts.Channels != nil:
		return sitechannels.BotHealthOf(c), nil
	case c.Channel == "Email" && opts.Channels != nil:
		return opts.Channels.MailboxHealthOf(ctx, c)
	}
	return nil, nil
}
