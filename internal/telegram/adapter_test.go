package telegram

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseUpdate(t *testing.T) {
	c, _ := New(Config{AccountID: "a", BotToken: "123456:abcdefghijklmnopqrstuvwxyzABCDE", Enabled: true})
	m, e := c.ParseUpdate([]byte(`{"update_id":4,"message":{"message_id":8,"date":1700000000,"text":"hi","chat":{"id":99},"from":{"username":"u"}}}`))
	if e != nil || m.ChatID != 99 || m.Text != "hi" {
		t.Fatal(m, e)
	}
}
func TestRejectsMalformedUpdate(t *testing.T) {
	c, _ := New(Config{AccountID: "a", BotToken: "123456:abcdefghijklmnopqrstuvwxyzABCDE", Enabled: true})
	if _, e := c.ParseUpdate([]byte(`{"update_id":4}`)); e == nil {
		t.Fatal("accepted malformed update")
	}
}

func TestDisabledAccountRejectsEveryEntryPoint(t *testing.T) {
	c, err := NewWithHTTPClient(Config{AccountID: "disabled", BotToken: groupSimulationToken, Enabled: false}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("disabled account reached network")
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.SendMessage(t.Context(), -123, "disabled", 0); err == nil {
		t.Fatal("disabled account sent a message")
	}
	if err = c.SendMessageToThread(t.Context(), -123, "disabled", 12, 7); err == nil {
		t.Fatal("disabled account sent a topic reply")
	}
	if _, err = c.ExecuteGroup(t.Context(), GroupInput{Operation: "info", ChatID: -123}); err == nil {
		t.Fatal("disabled account read group")
	}
	if _, err = c.ExecuteGroup(t.Context(), GroupInput{Operation: "photo.set", ChatID: -123, PhotoBase64: "invalid"}); err == nil || err.Error() != "Telegram account is disabled" {
		t.Fatal("disabled account entered photo pipeline")
	}
	if _, err = c.ParseUpdate([]byte(`{"update_id":4,"message":{"message_id":8,"chat":{"id":-123},"text":"ignored"}}`)); err == nil {
		t.Fatal("disabled account accepted update")
	}
}

func TestTelegramOutcomeClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		unknown    bool
		retry      int
	}{
		{"rate limited", `{"ok":false,"error_code":429,"parameters":{"retry_after":17}}`, 429, false, 17},
		{"migration", `{"ok":false,"error_code":400,"parameters":{"migrate_to_chat_id":-456}}`, 400, false, 0},
		{"blocked", `{"ok":false,"error_code":403}`, 403, false, 0},
		{"ambiguous provider failure", `{"ok":false,"error_code":500}`, 200, true, 0},
		{"ambiguous gateway", `{"ok":false,"error_code":502}`, 502, true, 0},
		{"malformed", `not JSON`, 200, true, 0},
		{"missing acceptance", `{}`, 200, true, 0},
		{"contradictory reply", `{"ok":true,"result":true}`, 429, true, 0},
		{"null receipt", `{"ok":true,"result":null}`, 200, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, err := NewWithHTTPClient(Config{AccountID: "fixture", BotToken: groupSimulationToken, Enabled: true}, &http.Client{Transport: simulationTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.call(t.Context(), "sendMessage", map[string]any{"chat_id": 123, "text": "synthetic"})
			var provider *APIError
			if !errors.As(err, &provider) || provider.OutcomeUnknown != tc.unknown || provider.RetryAfter != tc.retry || calls != 1 {
				t.Fatalf("wrong outcome %+v calls=%d", err, calls)
			}
			if tc.name == "migration" && provider.MigrateToChatID != -456 {
				t.Fatal("migration evidence lost")
			}
		})
	}
}
