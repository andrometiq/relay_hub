package telegram

import (
	"net/http"
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
