package hubmcp

import (
	"context"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/onboarding"
)

func TestSignupToolsAreScopedToTheTokenSite(t *testing.T) {
	ctx := context.Background()
	db, _ := simulationDatabase(t, ctx)
	if _, err := db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin,enabled) VALUES('site-a','https://a.test',true),('site-b','https://b.test',true)`); err != nil {
		t.Fatal(err)
	}
	store, _ := containers.New(db, "mcp-signup-test-key")
	app := &containers.ProviderInput{Config: map[string]any{"app_id": "333", "graph_version": "v25.0", "es_config_id": "es-1", "app_secret": "app-secret", "verify_token": "verify"}}
	if err := store.SaveContainer(ctx, containers.Container{ID: "signup", Enabled: true, Signup: true}, app, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO relay_channel_credentials(channel,channel_key,container_id,label,address,site_id,enabled,config) VALUES
 ('WhatsApp','wa-120','signup','Acme','120','site-a',true,'{"phone_id":"120","waba_id":"230","coexistence":true,"onboarding":"coexistence"}')`); err != nil {
		t.Fatal(err)
	}
	signup, err := onboarding.New(onboarding.Config{DB: db, Store: store, Auth: &auth.Store{DB: db}, PublicOrigin: "https://hub.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	server := func(site string) func(string, map[string]any) (bool, map[string]any, string) {
		h, err := NewAuthenticatedChannelsWithOptions(db.Ping, nil, func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{UserID: 1, Site: site}, nil
		}, nil, channels.Store{DB: db}, Options{Signup: signup})
		if err != nil {
			t.Fatal(err)
		}
		return func(name string, arguments map[string]any) (bool, map[string]any, string) {
			return callTool(h, name, arguments)
		}
	}
	siteA, siteB := server("site-a"), server("site-b")
	ok, result, body := siteA("whatsapp.signup.start", map[string]any{"flow": "new", "actor": "admin@a.test"})
	if !ok || !strings.HasPrefix(result["launch_url"].(string), "https://hub.example.test/onboard/whatsapp/sus_") {
		t.Fatalf("start: %s", body)
	}
	id := result["session"].(map[string]any)["session_id"].(string)
	if ok, result, body = siteA("whatsapp.signup.get", map[string]any{"session_id": id}); !ok || result["state"] != "created" || result["flow"] != "new" {
		t.Fatalf("get: %s", body)
	}
	for _, tool := range []string{"whatsapp.signup.get", "whatsapp.signup.resume", "whatsapp.signup.cancel"} {
		ok, result, body = siteB(tool, map[string]any{"session_id": id})
		if ok || result["error"].(map[string]any)["code"] != "not_found" {
			t.Fatalf("%s of a foreign session: %s", tool, body)
		}
	}
	if ok, _, body = siteA("whatsapp.signup.start", map[string]any{"flow": "phone"}); ok || !strings.Contains(body, "invalid_request") {
		t.Fatalf("invalid flow: %s", body)
	}
	if ok, result, body = siteA("whatsapp.signup.cancel", map[string]any{"session_id": id}); !ok || result["state"] != "cancelled" {
		t.Fatalf("cancel: %s", body)
	}
	ok, result, body = siteA("channels.list", map[string]any{})
	first := result["channels"].([]any)[0].(map[string]any)
	if !ok || first["coexistence"] != true || first["onboarding"] != "coexistence" {
		t.Fatalf("channel onboarding fields: %s", body)
	}
}
