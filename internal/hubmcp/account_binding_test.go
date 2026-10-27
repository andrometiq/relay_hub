package hubmcp

import (
	"context"
	"encoding/json"
	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountBindingIsScopedAndContainsNoCredentials(t *testing.T) {
	ctx := context.Background()
	db, _ := simulationDatabase(t, ctx)
	for _, a := range []string{"one", "two"} {
		if _, err := db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin,enabled) VALUES($1,$2,true)`, a, "https://"+a+".test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, account string
		scopes        []string
		denied        bool
	}{
		{"own", "one", []string{"account:read"}, false},
		{"other", "two", []string{"account:read"}, false},
		{"scope denied", "one", []string{"messages:send"}, true},
		{"missing", "missing", []string{"account:read"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewAuthenticatedChannels(db.Ping, nil, func(context.Context, string) (auth.Principal, error) {
				return auth.Principal{Site: tc.account, Scopes: tc.scopes}, nil
			}, nil, channels.Store{DB: db})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"account.get","arguments":{}}}`))
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", "2025-06-18")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			var response struct {
				Result struct {
					IsError bool           `json:"isError"`
					Data    map[string]any `json:"structuredContent"`
				}
			}
			if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err, w.Code)
			}
			if response.Result.IsError != tc.denied {
				t.Fatal("unexpected authorization outcome", w.Body.String())
			}
			if !tc.denied && (len(response.Result.Data) != 4 || response.Result.Data["account"] != tc.account || response.Result.Data["site"] != "https://"+tc.account+".test") {
				t.Fatal("binding leaked or invalid", response.Result.Data)
			}
		})
	}
}
