package sitechannels

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type channelInput struct {
	Channel string `json:"channel,omitempty"`
}

type checkInput struct {
	Channel string `json:"channel,omitempty"`
	Repair  bool   `json:"repair,omitempty"`
}

type removeInput struct {
	Channel string `json:"channel,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

type connectBotInput struct {
	BotToken string `json:"bot_token"`
	Label    string `json:"label,omitempty"`
}

// AddTools registers the telegram.bot tools. Each acts only on channels of the caller's site.
func (s *Service) AddTools(server *mcp.Server, siteOf SiteOf) {
	mcp.AddTool(server, &mcp.Tool{Name: "telegram.bot.connect", Description: "Connect a Telegram bot to this site with its @BotFather token. The Hub checks the token, keeps it encrypted and registers the webhook. The token is never returned."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in connectBotInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.ConnectBot(ctx, site, in.BotToken, in.Label) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "telegram.bot.health", Description: "Read the stored status of one Telegram bot of this site. No credentials are returned and Telegram is not called."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in channelInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.BotHealth(ctx, site, in.Channel) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "telegram.bot.check", Description: "Check one Telegram bot of this site with Telegram and record the result. repair registers the webhook again."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in checkInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.CheckBot(ctx, site, in.Channel, in.Repair) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "telegram.bot.settings", Description: "Rename one Telegram bot of this site, turn it on or off, or save a new token of the same bot. Omitted fields keep their values."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in BotSettings) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.SaveBotSettings(ctx, site, in) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "telegram.bot.remove", Description: "Remove one Telegram bot from this site after confirm. The Hub wipes its token and deletes its webhook. Refused while sends from the bot are open."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in removeInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.RemoveBot(ctx, site, in.Channel, in.Confirm) })
		})
}
