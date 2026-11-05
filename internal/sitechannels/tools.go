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

// AddTools registers the telegram.bot and email.mailbox tools. Each acts only on channels of the caller's site.
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
	mcp.AddTool(server, &mcp.Tool{Name: "email.mailbox.connect", Description: "Connect a mailbox to this site. The Hub signs in to SMTP and IMAP first and stores nothing when that fails. Passwords are kept encrypted and never returned. Only public mail servers on standard ports are allowed."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in MailboxInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.ConnectMailbox(ctx, site, in) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "email.mailbox.health", Description: "Read the stored status, server settings and sync state of one mailbox of this site. Passwords show only as saved or not."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in channelInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.MailboxHealth(ctx, site, in.Channel) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "email.mailbox.check", Description: "Sign in to SMTP and IMAP of one mailbox of this site and record the result. No mail is sent or read."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in channelInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.CheckMailbox(ctx, site, in.Channel) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "email.mailbox.settings", Description: "Rename one mailbox of this site, turn it on or off, or change how the Hub signs in. New sign-in settings are tested before they are stored. Omitted fields keep their values."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in MailboxSettings) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.SaveMailboxSettings(ctx, site, in) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "email.mailbox.remove", Description: "Remove one mailbox from this site after confirm. The Hub wipes both passwords and stops reading it. Refused while sends from the mailbox are open."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in removeInput) (*mcp.CallToolResult, map[string]any, error) {
			return Run(ctx, siteOf, func(site string) (any, error) { return s.RemoveMailbox(ctx, site, in.Channel, in.Confirm) })
		})
}
