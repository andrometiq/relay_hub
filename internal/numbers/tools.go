package numbers

import (
	"context"

	"github.com/andrometiq/relay_hub/internal/sitechannels"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SiteOf returns the site of the MCP caller's token.
type SiteOf = sitechannels.SiteOf

type numberInput struct {
	Channel string `json:"channel,omitempty"`
}

type removeInput struct {
	Channel string `json:"channel,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

type checkInput struct {
	Channel string `json:"channel,omitempty"`
	Repair  bool   `json:"repair,omitempty"`
}

// AddTools registers the whatsapp.number tools. Each acts only on numbers of the caller's site.
func (s *Service) AddTools(server *mcp.Server, siteOf SiteOf) {
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.health", Description: "Read the connection status, settings and delivery health of one WhatsApp number of this site. No credentials are returned."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in numberInput) (*mcp.CallToolResult, map[string]any, error) {
			return sitechannels.Run(ctx, siteOf, func(site string) (any, error) { return s.Health(ctx, site, in.Channel) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.check", Description: "Check one WhatsApp number of this site with Meta and record the result. repair subscribes the app to the account's webhooks again. reconnect_required means the number must be connected again with Embedded Signup."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in checkInput) (*mcp.CallToolResult, map[string]any, error) {
			return sitechannels.Run(ctx, siteOf, func(site string) (any, error) { return s.Check(ctx, site, in.Channel, in.Repair) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.settings", Description: "Turn one WhatsApp number of this site on or off, and switch message templates and the hourly Meta sync. Omitted fields keep their values."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SettingsInput) (*mcp.CallToolResult, map[string]any, error) {
			return sitechannels.Run(ctx, siteOf, func(site string) (any, error) { return s.SaveSettings(ctx, site, in) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.pin_reset", Description: "Give one WhatsApp number of this site a new two-step verification PIN. The Hub keeps the PIN and never returns it."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in numberInput) (*mcp.CallToolResult, map[string]any, error) {
			return sitechannels.Run(ctx, siteOf, func(site string) (any, error) { return s.ResetPIN(ctx, site, in.Channel) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.remove", Description: "Remove one WhatsApp number from this site after confirm. The Hub wipes its credentials and deregisters it at Meta so it can move. Refused while sends from the number are open."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in removeInput) (*mcp.CallToolResult, map[string]any, error) {
			return sitechannels.Run(ctx, siteOf, func(site string) (any, error) { return s.Remove(ctx, site, in.Channel, in.Confirm) })
		})
}
