package main

import (
	"context"
	"github.com/andrometiq/relay_hub/internal/accounts"
	"testing"
)

func TestWhatsAppSavedConfigurationMatchesValidation(t *testing.T) {
	stored := accounts.RuntimeCredential{Secret: "saved", Config: map[string]any{"phone_id": "111", "app_id": "333", "waba_id": "222", "app_secret": "private", "verify_token": "verify", "templates": true}}
	for _, config := range []map[string]any{nil, {}, {"phone_id": "", "app_id": "333", "waba_id": "222"}} {
		if validateWhatsAppConfig(config) == nil {
			t.Fatal("accepted incomplete public identity")
		}
		candidate := whatsappCandidate(accounts.Account{ID: "a"}, accounts.Channel{Config: config}, "", stored)
		if candidate.PhoneID != "" || candidate.Templates || candidate.AccessToken != "saved" || candidate.AppSecret != "private" {
			t.Fatal("candidate restored omitted public config")
		}
	}
	for _, value := range []any{0, 1, 1000, float64(15)} {
		config := map[string]any{"phone_id": "111", "app_id": "333", "waba_id": "222", "messages_per_second": value}
		if err := validateWhatsAppConfig(config); err != nil {
			t.Fatal(err)
		}
		candidate := whatsappCandidate(accounts.Account{ID: "a"}, accounts.Channel{Config: config}, "", stored)
		rows, err := runtimeWhatsApp(context.Background(), fixtureRuntime{rows: []accounts.RuntimeCredential{{AccountID: "a", Channel: "WhatsApp", Config: config}}})
		if err != nil || len(rows) != 1 || candidate.MessagesPerSecond != rows[0].MessagesPerSecond {
			t.Fatal("pacing mismatch", err)
		}
	}
	for _, value := range []any{-1, 1001, 1.5, true, "bad", "15"} {
		if _, err := whatsappRate(value); err == nil {
			t.Fatal("accepted invalid rate", value)
		}
		if _, err := runtimeWhatsApp(t.Context(), fixtureRuntime{rows: []accounts.RuntimeCredential{{Channel: "WhatsApp", Config: map[string]any{"messages_per_second": value}}}}); err == nil {
			t.Fatal("invalid stored rate loaded")
		}
	}
}
