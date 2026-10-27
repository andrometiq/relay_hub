package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type webhookTransport func(*http.Request) (*http.Response, error)

func (f webhookTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebhookRepairPreservesUpdatesAndRedactsProviderFields(t *testing.T) {
	calls := 0
	client, err := NewWithHTTPClient(Config{AccountID: "local", BotToken: "123456:abcdefghijklmnopqrstuv", Enabled: true}, &http.Client{Transport: webhookTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"ok":true,"result":{"url":"https://hub.invalid/hub/webhooks/telegram/local","pending_update_count":7,"last_error_date":123,"last_error_message":"secret-should-not-escape"}}`
		if strings.HasSuffix(r.URL.Path, "setWebhook") {
			var input map[string]any
			if json.NewDecoder(r.Body).Decode(&input) != nil || input["max_connections"] != float64(1) || input["drop_pending_updates"] != false || input["secret_token"] != "local-secret" || input["url"] != "https://hub.invalid/hub/webhooks/telegram/local" {
				t.Fatal("invalid registration")
			}
			body = `{"ok":true,"result":true}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.RegisterWebhook(context.Background(), "https://hub.invalid/hub/webhooks/telegram/local", "local-secret"); err != nil {
		t.Fatal(err)
	}
	health, err := client.WebhookHealth(context.Background(), "https://hub.invalid/hub/webhooks/telegram/local")
	raw, _ := json.Marshal(health)
	if err != nil || !health.Matches || health.Pending != 7 || health.LastErrorAt != 123 || strings.Contains(string(raw), "secret") || calls != 2 {
		t.Fatalf("bad health: %s, %v", raw, err)
	}
	if client.RegisterWebhook(context.Background(), "http://untrusted.invalid", "local-secret") == nil || calls != 2 {
		t.Fatal("unsafe webhook accepted")
	}
}
