package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// siteCall runs one tool and returns its structured answer, its error code and the raw answer.
func (l *launcher) siteCall(token, name string, args map[string]any) (map[string]any, string, string) {
	l.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	res := l.mcpPost(token, raw)
	defer res.Body.Close()
	var envelope struct {
		Result struct {
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	body := new(strings.Builder)
	json.NewDecoder(teeReader{res.Body, body}).Decode(&envelope)
	failure, _ := envelope.Result.Structured["error"].(map[string]any)
	code, _ := failure["code"].(string)
	for _, secret := range []string{simulationBotToken, simulationBotToken2, simulationMailPassword, "wrong-mail-password", l.controlToken} {
		if strings.Contains(body.String(), secret) {
			l.t.Fatalf("%s answered with a credential: %s", name, body)
		}
	}
	return envelope.Result.Structured, code, body.String()
}

type teeReader struct {
	r interface{ Read([]byte) (int, error) }
	w *strings.Builder
}

func (t teeReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.w.Write(p[:n])
	return n, err
}

// waitCore waits until the site's Core received a callback that contains every part.
func waitCore(t *testing.T, core *fakeEnrolCore, parts ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		core.mu.Lock()
		for _, body := range core.bodies {
			found := true
			for _, part := range parts {
				found = found && strings.Contains(body, part)
			}
			if found {
				core.mu.Unlock()
				return
			}
		}
		core.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Core did not receive %v", parts)
}

// TestSiteConnectsItsOwnBotsAndMailboxes drives the site bot and mailbox
// tools against the fake Telegram and mail servers, from connect to remove.
func TestSiteConnectsItsOwnBotsAndMailboxes(t *testing.T) {
	dsn := isolatedLauncherDatabase(t)
	t.Setenv("RELAY_SIM_MAIL_ALLOW", "127.0.0.1/32")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	core := &fakeEnrolCore{secret: strings.Repeat("e", 64)}
	server := httptest.NewServer(core)
	defer server.Close()
	l := newLauncher(t, ctx, dsn, server.URL)
	l.start(false)
	port := server.URL[strings.LastIndex(server.URL, ":")+1:]
	l.call("signup-container", map[string]any{"token": enrolToken, "origin_pattern": "http://*.localhost:" + port})
	acme := l.enrolSite("acme.localhost:"+port, core)
	other := l.enrolSite("other.localhost:"+port, core)

	bot := exerciseSiteBotSetup(t, l, core, acme, other)
	exerciseSiteMailboxSetup(t, l, core, acme, other)
	if _, code, body := l.siteCall(other, "telegram.bot.remove", map[string]any{"channel": bot, "confirm": true}); code != "not_found" {
		t.Fatalf("another site removed the bot: %s", body)
	}
}

func exerciseSiteBotSetup(t *testing.T, l *launcher, core *fakeEnrolCore, acme, other string) string {
	if _, code, body := l.siteCall(acme, "telegram.bot.connect", map[string]any{"bot_token": "123459:not_a_simulation_bot_token_x"}); code != "invalid_token" {
		t.Fatalf("bad token: %s", body)
	}
	health, code, body := l.siteCall(acme, "telegram.bot.connect", map[string]any{"bot_token": simulationBotToken, "label": "Acme Textiles"})
	bot, _ := health["channel"].(string)
	if code != "" || health["webhook_status"] != "registered" || health["username"] != "relay_sim_bot" {
		t.Fatalf("connect: %s", body)
	}
	hooks := l.call("providers", nil)["telegram_webhooks"].(map[string]any)
	if hook, _ := hooks["relay_sim_bot"].(map[string]any); !strings.HasPrefix(hook["url"].(string), simulationPublicURL+"/webhooks/telegram/") {
		t.Fatalf("webhook: %v", hooks)
	}
	if _, code, body = l.siteCall(other, "telegram.bot.health", map[string]any{"channel": bot}); code != "not_found" {
		t.Fatalf("another site read the bot: %s", body)
	}
	update := map[string]any{"update_id": 9101, "message": map[string]any{"message_id": 5, "date": time.Now().Unix(), "chat": map[string]any{"id": 4242, "type": "private"}, "from": map[string]any{"id": 4243, "first_name": "Customer"}, "text": "Hello Acme Textiles"}}
	if status := l.call("telegram/deliver", map[string]any{"payload": update, "invalid_signature": true})["status"]; status == float64(200) {
		t.Fatal("a wrong webhook secret was accepted")
	}
	if status := l.call("telegram/deliver", map[string]any{"payload": update})["status"]; status != float64(200) {
		t.Fatalf("webhook delivery: %v", status)
	}
	waitCore(t, core, "Hello Acme Textiles", bot)

	second, code, body := l.siteCall(acme, "telegram.bot.connect", map[string]any{"bot_token": simulationBotToken2})
	if code != "" || second["channel"] == bot {
		t.Fatalf("second bot: %s", body)
	}
	l.call("telegram/clear-webhook", map[string]any{"bot": "relay_sim_bot"})
	if checked, _, body := l.siteCall(acme, "telegram.bot.check", map[string]any{"channel": bot}); checked["webhook_status"] != "not_registered" {
		t.Fatalf("check after the webhook was lost: %s", body)
	}
	if checked, _, body := l.siteCall(acme, "telegram.bot.check", map[string]any{"channel": bot, "repair": true}); checked["webhook_status"] != "registered" {
		t.Fatalf("repair: %s", body)
	}
	if _, code, body = l.siteCall(acme, "telegram.bot.settings", map[string]any{"channel": bot, "bot_token": simulationBotToken2}); code != "bot_changed" {
		t.Fatalf("token of another bot: %s", body)
	}
	if removed, code, body := l.siteCall(acme, "telegram.bot.remove", map[string]any{"channel": second["channel"], "confirm": true}); code != "" || removed["webhook_deleted"] != true {
		t.Fatalf("remove: %s", body)
	}
	if _, ok := l.call("providers", nil)["telegram_webhooks"].(map[string]any)["relay_sim_bot_2"]; ok {
		t.Fatal("the removed bot still has a webhook")
	}
	if again, code, body := l.siteCall(acme, "telegram.bot.connect", map[string]any{"bot_token": simulationBotToken2}); code != "" || again["channel"] != second["channel"] {
		t.Fatalf("connect again: %s", body)
	}
	return bot
}

func exerciseSiteMailboxSetup(t *testing.T, l *launcher, core *fakeEnrolCore, acme, other string) {
	mailbox := func(change map[string]any) map[string]any {
		in := map[string]any{"from": "sales@sim.invalid", "label": "Acme Textiles sales", "host": "127.0.0.1", "port": 587, "username": simulationMailUser,
			"password": simulationMailPassword, "imap_host": "127.0.0.1", "imap_port": 993, "trusted_authserv_id": simulationAuthserv}
		for key, value := range change {
			in[key] = value
		}
		return in
	}
	for code, change := range map[string]map[string]any{"login_failed": {"password": "wrong-mail-password"}, "host_refused": {"imap_host": "10.0.0.1"}, "port_refused": {"port": 8025}} {
		if _, got, body := l.siteCall(acme, "email.mailbox.connect", mailbox(change)); got != code {
			t.Fatalf("%s: %s", code, body)
		}
	}
	health, code, body := l.siteCall(acme, "email.mailbox.connect", mailbox(nil))
	key, _ := health["channel"].(string)
	if code != "" || health["connection_status"] != "connected" || health["imap_status"] != "connected" || health["password_saved"] != true {
		t.Fatalf("connect mailbox: %s", body)
	}
	if _, code, body = l.siteCall(other, "email.mailbox.health", map[string]any{"channel": key}); code != "not_found" {
		t.Fatalf("another site read the mailbox: %s", body)
	}
	// The reader starts at new mail, so wait for its first sync before the message arrives.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if read, _, _ := l.siteCall(acme, "email.mailbox.health", map[string]any{"channel": key}); len(read["sync"].([]any)) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	raw := "From: customer@example.invalid\r\nTo: sales@sim.invalid\r\nSubject: Order from Acme\r\nMessage-ID: <acme-order@example.invalid>\r\n\r\nHello sales"
	l.call("email/emit", map[string]any{"raw": raw, "authentication": "pass"})
	waitCore(t, core, "Order from Acme", key)
	l.mcp(acme, "email.send", map[string]any{"channel": key, "request_key": "acme-mail-reply", "to": []string{"customer@example.invalid"}, "subject": "Re: Order from Acme", "body": "Thank you"}, 200)
	l.wait(func(s map[string]any) bool {
		for _, sent := range l.call("providers", nil)["email"].(map[string]any)["sent"].([]any) {
			if message := sent.(map[string]any); strings.Contains(message["raw"].(string), "Re: Order from Acme") {
				return message["from"] == "<sales@sim.invalid>" && strings.HasPrefix(message["raw"].(string), "From: sales@sim.invalid\n")
			}
		}
		return false
	})
	if changed, code, body := l.siteCall(acme, "email.mailbox.settings", map[string]any{"channel": key, "imap_folder": "INBOX", "label": "Acme sales"}); code != "" || changed["label"] != "Acme sales" {
		t.Fatalf("settings: %s", body)
	}
	if removed, code, body := l.siteCall(acme, "email.mailbox.remove", map[string]any{"channel": key, "confirm": true}); code != "" || removed["removed"] != true {
		t.Fatalf("remove mailbox: %s", body)
	}
	if _, code, body = l.siteCall(acme, "email.mailbox.health", map[string]any{"channel": key}); code != "not_found" {
		t.Fatalf("health after remove: %s", body)
	}
}
