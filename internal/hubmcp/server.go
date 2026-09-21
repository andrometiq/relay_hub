package hubmcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"strings"
	"time"
)

type Health struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

func New(token string, check func(context.Context) error) (http.Handler, error) {
	if len(token) < 32 {
		return nil, errors.New("RELAY_MCP_TOKEN must contain at least 32 characters")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "relay-hub", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "health.get", Description: "Check Relay Hub database readiness"}, func(ctx context.Context, r *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, Health, error) {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if check(c) != nil {
			return nil, Health{}, errors.New("Hub is not ready")
		}
		return nil, Health{Status: "ready", Service: "relay_hub"}, nil
	})
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Development service credential: no browser access or business tools yet.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origin not permitted", http.StatusForbidden)
			return
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		transport.ServeHTTP(w, r)
	}), nil
}
