package numbers

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SiteOf returns the site of the MCP caller's token.
type SiteOf func(context.Context) (string, bool)

type numberInput struct {
	Channel string `json:"channel,omitempty"`
}

type checkInput struct {
	Channel string `json:"channel,omitempty"`
	Repair  bool   `json:"repair,omitempty"`
}

// AddTools registers the whatsapp.number tools. Each acts only on numbers of the caller's site.
func (s *Service) AddTools(server *mcp.Server, siteOf SiteOf) {
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.health", Description: "Read the connection status, settings and delivery health of one WhatsApp number of this site. No credentials are returned."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in numberInput) (*mcp.CallToolResult, map[string]any, error) {
			return run(ctx, siteOf, func(site string) (any, error) { return s.Health(ctx, site, in.Channel) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.check", Description: "Check one WhatsApp number of this site with Meta and record the result. repair subscribes the app to the account's webhooks again. reconnect_required means the number must be connected again with Embedded Signup."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in checkInput) (*mcp.CallToolResult, map[string]any, error) {
			return run(ctx, siteOf, func(site string) (any, error) { return s.Check(ctx, site, in.Channel, in.Repair) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.number.settings", Description: "Turn one WhatsApp number of this site on or off, and switch message templates and the hourly Meta sync. Omitted fields keep their values."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SettingsInput) (*mcp.CallToolResult, map[string]any, error) {
			return run(ctx, siteOf, func(site string) (any, error) { return s.SaveSettings(ctx, site, in) })
		})
}

// run calls one tool for the caller's site and returns its result as an object or a coded failure.
func run(ctx context.Context, siteOf SiteOf, call func(site string) (any, error)) (*mcp.CallToolResult, map[string]any, error) {
	site, ok := siteOf(ctx)
	if !ok {
		return nil, nil, errors.New("site credential required")
	}
	value, err := call(site)
	if err != nil {
		var failure *Failure
		if !errors.As(err, &failure) {
			failure = errUnavailable
		}
		result := &mcp.CallToolResult{}
		result.SetError(failure)
		return result, map[string]any{"error": failure}, nil
	}
	var out map[string]any
	raw, _ := json.Marshal(value)
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}
