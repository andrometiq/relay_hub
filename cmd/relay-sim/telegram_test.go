package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/telegram"
)

func telegramFixture(t *testing.T, path string) (*fakeTelegram, *telegram.Client, *http.Client) {
	t.Helper()
	fake, err := newFakeTelegram(path, newRecorder())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	origin, _ := url.Parse(server.URL)
	tr := newTransport(origin, origin, fake.records)
	t.Cleanup(tr.base.CloseIdleConnections)
	httpClient := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	client, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: "sim-v1", BotToken: simulationBotToken, Enabled: true}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return fake, client, httpClient
}
func TestTelegramProviderRoundTripAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telegram.json")
	fake, client, httpClient := telegramFixture(t, path)
	ctx := context.Background()
	sent, err := client.ExecuteMessage(ctx, telegram.MessageInput{Operation: "text", ChatID: -100123, Text: "Local simulation", ReplyTo: 7, ThreadID: 12})
	if err != nil {
		t.Fatal(err)
	}
	if sent["message_id"] != int64(1) {
		t.Fatal(sent)
	}
	_, err = client.ExecuteMessage(ctx, telegram.MessageInput{Operation: "reaction", ChatID: -100123, MessageID: 1, Reaction: []telegram.Reaction{{Type: "emoji", Emoji: "👍"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ExecuteMessage(ctx, telegram.MessageInput{Operation: "document", ChatID: -100123, Filename: "note.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("local attachment"))})
	if err != nil {
		t.Fatal(err)
	}
	file, err := client.FetchMedia(ctx, "sim_file_2_document")
	if err != nil || string(file.Data) != "local attachment" {
		t.Fatalf("media round trip: %v", err)
	}
	for _, id := range []int{100, 101} {
		if err := fake.queue(json.RawMessage(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":8,"chat":{"id":42,"type":"private"},"date":123,"text":"queued"}}`, id))); err != nil {
			t.Fatal(err)
		}
	}
	call := func(body string) []any {
		t.Helper()
		res, err := httpClient.Post("https://api.telegram.org/bot"+simulationBotToken+"/getUpdates", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Result []any `json:"result"`
		}
		if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Result
	}
	if len(call(`{"offset":101}`)) != 1 {
		t.Fatal("offset not respected")
	}
	restarted, next, _ := telegramFixture(t, path)
	if len(restarted.state.snapshot().Updates) != 1 {
		t.Fatal("updates not durable")
	}
	receipt, err := next.ExecuteMessage(ctx, telegram.MessageInput{Operation: "text", ChatID: 42, Text: "After restart"})
	if err != nil || receipt["message_id"] != int64(3) {
		t.Fatal("message identity lost", err, receipt)
	}
	if _, err = next.FetchMedia(ctx, "sim_file_2_document"); err != nil {
		t.Fatal("file lost", err)
	}
	raw, _ := json.Marshal(fake.records.snapshot())
	if strings.Contains(string(raw), simulationBotToken) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte("local attachment"))) {
		t.Fatal("credential or binary capture leaked")
	}
}
func TestTelegramProviderFaults(t *testing.T) {
	fake, client, _ := telegramFixture(t, filepath.Join(t.TempDir(), "telegram.json"))
	for _, fault := range []string{"429", "503", "blocked", "migrate", "disconnect_before", "disconnect_after", "timeout_before", "timeout_after"} {
		t.Run(fault, func(t *testing.T) {
			before := len(fake.state.snapshot().Messages)
			if err := fake.faults([]string{fault}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := client.ExecuteMessage(ctx, telegram.MessageInput{Operation: "text", ChatID: 42, Text: "Fault probe"})
			var api *telegram.APIError
			if !errors.As(err, &api) {
				t.Fatalf("missing API error: %v", err)
			}
			if fault == "429" && (api.RetryAfter != 1 || api.OutcomeUnknown) {
				t.Fatal(api)
			}
			if fault == "migrate" && api.MigrateToChatID != -1009001 {
				t.Fatal(api)
			}
			unknown := fault == "503" || strings.Contains(fault, "before") || strings.Contains(fault, "after")
			if api.OutcomeUnknown != unknown {
				t.Fatal(api)
			}
			want := before
			if strings.HasSuffix(fault, "after") {
				want++
			}
			if len(fake.state.snapshot().Messages) != want {
				t.Fatal("incorrect provider acceptance")
			}
		})
	}
	if err := fake.faults([]string{"unknown"}); err == nil {
		t.Fatal("invalid fault accepted")
	}
}
func TestTelegramConcurrentSendsAndPendingFaultRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telegram.json")
	fake, client, _ := telegramFixture(t, path)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.ExecuteMessage(context.Background(), telegram.MessageInput{Operation: "text", ChatID: 42, Text: "Concurrent"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	state := fake.state.snapshot()
	if state.Next != 24 || len(state.Messages) != 24 {
		t.Fatal("lost concurrent sends")
	}
	if err := fake.faults([]string{"429", "503"}); err != nil {
		t.Fatal(err)
	}
	restored, _, _ := telegramFixture(t, path)
	if fmt.Sprint(restored.state.snapshot().Faults) != "[429 503]" {
		t.Fatal("lost fault schedule")
	}
}
func TestChannelTransportRedactionAndDestinations(t *testing.T) {
	records := newRecorder()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"message": map[string]any{"accepted": 1}})
	}))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	tr := newTransport(origin, origin, records)
	defer tr.base.CloseIdleConnections()
	req, _ := http.NewRequest("POST", server.URL+"/api/method/relay_core.inbound.receive_channel_statuses", strings.NewReader(`{"statuses":[{}]}`))
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if records.snapshot()[0].Kind != "status" {
		t.Fatal("channel status not captured")
	}
	for _, destination := range []string{"https://example.com/", "http://127.0.0.1:9/", "http://api.telegram.org/botsecret/sendMessage"} {
		req, _ := http.NewRequest("GET", destination, nil)
		if _, err := tr.RoundTrip(req); err == nil {
			t.Fatal("unexpected destination accepted")
		}
	}
	req = httptest.NewRequest("POST", "/file/botprivate-token/path", nil)
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "private-telegram")
	req.Header.Set("X-Relay-Email-Secret", "private-email")
	records.record(req, "webhook", nil, 200, nil, time.Now(), "")
	raw, _ := json.Marshal(records.snapshot())
	if strings.Contains(string(raw), "private-") {
		t.Fatal("ingress credential leaked")
	}
}
