package main

import (
	"context"
	"errors"
	"testing"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

type fixtureRuntime struct {
	rows []containers.RuntimeCredential
	err  error
}

func (s fixtureRuntime) Runtime(context.Context) ([]containers.RuntimeCredential, error) {
	return s.rows, s.err
}
func TestAuthoritativeEmptyWhatsAppConfiguration(t *testing.T) {
	legacy := whatsapp.Account{ID: "legacy", Site: "https://fixture.invalid", PhoneID: "111", WABAID: "222", Version: "v25.0", AccessToken: "fixture"}
	wa, err := whatsapp.New([]whatsapp.Account{legacy})
	if err != nil {
		t.Fatal(err)
	}
	if err = activateWhatsApp(context.Background(), wa, fixtureRuntime{}); err != nil {
		t.Fatal(err)
	}
	if len(wa.AccountsSnapshot()) != 0 {
		t.Fatal("empty database configuration restored legacy credentials")
	}
	restarted, _ := whatsapp.New(nil)
	if err = activateWhatsApp(context.Background(), restarted, fixtureRuntime{}); err != nil || len(restarted.AccountsSnapshot()) != 0 {
		t.Fatal("disabled accounts reactivated", err)
	}
	if err = activateWhatsApp(context.Background(), restarted, fixtureRuntime{err: errors.New("database unavailable")}); err == nil {
		t.Fatal("database error silently fell back")
	}
}

func TestWhatsAppRuntimeDisablesStoredGroupAndCallCapabilities(t *testing.T) {
	rows, err := runtimeWhatsApp(context.Background(), fixtureRuntime{rows: []containers.RuntimeCredential{{
		Key: "fixture", Channel: "WhatsApp", Site: "https://fixture.invalid", Secret: "fixture", Enabled: true,
		Config: map[string]any{"phone_id": "111", "waba_id": "222", "graph_version": "v25.0", "templates": true, "groups": true, "calls": true},
	}}})
	if err != nil || len(rows) != 1 || rows[0].Groups || rows[0].Calls || !rows[0].Templates {
		t.Fatal("v1 runtime capabilities", rows, err)
	}
}
