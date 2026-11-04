package onboarding

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SiteOf returns the site of the MCP caller's token.
type SiteOf func(context.Context) (string, bool)

type sessionInput struct {
	SessionID string `json:"session_id"`
	Refresh   bool   `json:"refresh,omitempty"`
}

// AddTools registers whatsapp.signup.start, get, resume and cancel, and
// site.disconnect. Each tool acts only on the caller's site.
func (s *Service) AddTools(server *mcp.Server, siteOf SiteOf) {
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.signup.start", Description: "Start a self-service WhatsApp signup for this site and return the Hub window URL. A running signup is returned instead of a new one."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Flow    string         `json:"flow"`
			Prefill map[string]any `json:"prefill,omitempty"`
			Actor   string         `json:"actor,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			site, ok := siteOf(ctx)
			if !ok {
				return nil, nil, errors.New("site credential required")
			}
			session, launch, err := s.Start(ctx, site, in.Flow, in.Prefill, in.Actor)
			if err != nil {
				return toolFailure(err)
			}
			return nil, map[string]any{"session": session, "launch_url": launch}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.signup.get", Description: "Read a WhatsApp signup of this site. refresh re-reads the payment method and number status from Meta."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sessionInput) (*mcp.CallToolResult, map[string]any, error) {
			return s.sessionTool(ctx, siteOf, func(site string) (Session, error) { return s.Get(ctx, site, in.SessionID, in.Refresh) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.signup.resume", Description: "Run the next signup step now. Safe to repeat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sessionInput) (*mcp.CallToolResult, map[string]any, error) {
			return s.sessionTool(ctx, siteOf, func(site string) (Session, error) { return s.Resume(ctx, site, in.SessionID) })
		})
	mcp.AddTool(server, &mcp.Tool{Name: "site.disconnect", Description: "Disconnect this site from the Hub after confirm: the site is turned off and its tokens are revoked. Its channels stay linked; enrol again to reconnect."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Confirm bool `json:"confirm,omitempty"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			site, ok := siteOf(ctx)
			if !ok {
				return nil, nil, errors.New("site credential required")
			}
			if err := s.Disconnect(ctx, site, in.Confirm); err != nil {
				return toolFailure(err)
			}
			return nil, map[string]any{"disconnected": true}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whatsapp.signup.cancel", Description: "Cancel a signup before Meta returned a code."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sessionInput) (*mcp.CallToolResult, map[string]any, error) {
			return s.sessionTool(ctx, siteOf, func(site string) (Session, error) { return s.Cancel(ctx, site, in.SessionID) })
		})
}

func (s *Service) sessionTool(ctx context.Context, siteOf SiteOf, call func(string) (Session, error)) (*mcp.CallToolResult, map[string]any, error) {
	site, ok := siteOf(ctx)
	if !ok {
		return nil, nil, errors.New("site credential required")
	}
	session, err := call(site)
	if err != nil {
		return toolFailure(err)
	}
	var out map[string]any
	raw, _ := json.Marshal(session)
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// toolFailure returns a coded tool error that Core maps to its own text.
func toolFailure(err error) (*mcp.CallToolResult, map[string]any, error) {
	failure := asFailure(err)
	result := &mcp.CallToolResult{}
	result.SetError(failure)
	return result, map[string]any{"error": failure}, nil
}
