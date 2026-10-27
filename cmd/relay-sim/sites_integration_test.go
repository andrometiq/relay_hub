package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// coreCallback is one signed callback batch a fake Core site received.
type coreCallback struct {
	Site, Path string
	Body       map[string]any
}

// fakeCores answers callbacks for both simulated sites, which share one Core
// origin and are told apart by their API key.
type fakeCores struct {
	mu        sync.Mutex
	callbacks []coreCallback
}

var simSiteKeys = map[string]string{"sim-site-1": "site-one-key", "sim-site-2": "site-two-key"}

func simSiteSecret(site string) string { return strings.Repeat(site+"-callback-", 3) }

func (c *fakeCores) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	site := ""
	for id, key := range simSiteKeys {
		if r.Header.Get("Authorization") == "token "+key+":"+key+"-secret" {
			site = id
		}
	}
	mac := hmac.New(sha256.New, []byte(simSiteSecret(site)))
	mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
	mac.Write(raw)
	if site == "" || !hmac.Equal([]byte(r.Header.Get("X-Relay-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		http.Error(w, "unauthorized", 403)
		return
	}
	var body map[string]any
	json.Unmarshal(raw, &body)
	c.mu.Lock()
	c.callbacks = append(c.callbacks, coreCallback{site, r.URL.Path, body})
	c.mu.Unlock()
	accepted := 0
	for _, key := range []string{"events", "statuses", "updates"} {
		rows, _ := body[key].([]any)
		accepted += len(rows)
	}
	writeJSON(w, map[string]any{"message": map[string]any{"accepted": accepted}})
}

// rows lists every callback row (event, status or bulk update) a site received.
func (c *fakeCores) rows(site string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := []map[string]any{}
	for _, callback := range c.callbacks {
		if callback.Site != site {
			continue
		}
		for _, key := range []string{"events", "statuses", "updates"} {
			rows, _ := callback.Body[key].([]any)
			for _, row := range rows {
				value, _ := row.(map[string]any)
				value["batch_account"] = callback.Body["account"]
				result = append(result, value)
			}
		}
	}
	return result
}

// waitRow waits until site received a row whose JSON contains every part.
func (c *fakeCores) waitRow(t *testing.T, site string, parts ...string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
	rows:
		for _, row := range c.rows(site) {
			raw, _ := json.Marshal(row)
			for _, part := range parts {
				if !strings.Contains(string(raw), part) {
					continue rows
				}
			}
			return row
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not receive a callback with %v", site, parts)
	return nil
}

// TestTwoSitesShareOneMetaApp runs one container (one Meta app) with two
// numbers in two WABAs, two bots and two mailboxes, split over two sites.
// Every inbound reaches only its own site, and every reply leaves from the
// channel the site owns.
func TestTwoSitesShareOneMetaApp(t *testing.T) {
	dsn := isolatedLauncherDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cores := &fakeCores{}
	core := httptest.NewServer(cores)
	defer core.Close()
	l := newLauncher(t, ctx, dsn, core.URL)
	l.start(false)
	bindings := []map[string]any{}
	for _, site := range []string{"sim-site-1", "sim-site-2"} {
		key := simSiteKeys[site]
		bindings = append(bindings, map[string]any{"site": "http://" + site + ".invalid", "api_key": key, "api_secret": key + "-secret", "callback_secret": simSiteSecret(site)})
	}
	tokens := l.call("provision-sites", map[string]any{"sites": bindings})["tokens"].(map[string]any)
	one, two := tokens["sim-site-1"].(string), tokens["sim-site-2"].(string)

	listed := l.mcp(two, "channels.list", map[string]any{}, 200)
	raw, _ := json.Marshal(listed)
	if !strings.Contains(string(raw), "sim-wa-2") || !strings.Contains(string(raw), "sim-bot-2") || !strings.Contains(string(raw), "sim-mail-2") || strings.Contains(string(raw), "sim-wa-1") {
		t.Fatalf("site 2 channels: %s", raw)
	}

	exerciseSiteWhatsApp(t, l, cores, one, two)
	exerciseSiteBots(t, l, cores, two)
	exerciseSiteMailboxes(t, l, cores, two)
	exerciseSiteBulk(t, l, cores, two)
}

func whatsappInbound(phone, from, id string) map[string]any {
	number := simulationNumbers[phone]
	value := map[string]any{"messaging_product": "whatsapp", "metadata": map[string]any{"display_phone_number": number.Display, "phone_number_id": phone},
		"contacts": []any{map[string]any{"profile": map[string]any{"name": "Customer"}, "wa_id": from}},
		"messages": []any{map[string]any{"id": id, "from": from, "timestamp": fmt.Sprint(time.Now().Unix()), "type": "text", "text": map[string]any{"body": "Hello " + phone}}}}
	return map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": number.WABA, "changes": []any{map[string]any{"field": "messages", "value": value}}}}}
}

func exerciseSiteWhatsApp(t *testing.T, l *launcher, cores *fakeCores, one, two string) {
	for _, phone := range []string{"110", "111"} {
		if l.call("emit", map[string]any{"payload": whatsappInbound(phone, "919800000"+phone, "wamid.in."+phone)})["status"] != float64(200) {
			t.Fatal("WhatsApp ingress rejected")
		}
	}
	first := cores.waitRow(t, "sim-site-1", "wamid.in.110")
	second := cores.waitRow(t, "sim-site-2", "wamid.in.111")
	if first["batch_account"] != "sim-site-1" || second["batch_account"] != "sim-site-2" || !strings.Contains(fmt.Sprint(second), "sim-wa-2") || !strings.Contains(fmt.Sprint(second), "919999999998") {
		t.Fatalf("inbound routing: %v %v", first, second)
	}
	for _, row := range cores.rows("sim-site-1") {
		if strings.Contains(fmt.Sprint(row), "wamid.in.111") {
			t.Fatal("site 1 received site 2's message")
		}
	}

	l.mcpError(one, "whatsapp.send", map[string]any{"channel": "sim-wa-2", "message": map[string]any{"key": "foreign", "message": map[string]any{"operation": "messages.send", "to": "919800000111", "body": "Not mine"}}})
	l.mcp(two, "whatsapp.send", map[string]any{"message": map[string]any{"key": "reply-two", "message": map[string]any{"operation": "messages.send", "to": "919800000111", "body": "Reply from site 2", "reply_to": "wamid.in.111"}}}, 200)
	state := l.wait(func(s map[string]any) bool {
		for _, d := range s["deliveries"].([]any) {
			row := d.(map[string]any)
			if row["request_key"] == "reply-two" {
				return row["state"] == "submitted"
			}
		}
		return false
	})
	posts := map[string]int{}
	for _, r := range state["records"].([]any) {
		row := r.(map[string]any)
		if row["kind"] == "graph" && row["method"] == "POST" && strings.HasSuffix(row["path"].(string), "/messages") {
			posts[row["path"].(string)]++
		}
	}
	if posts["/v25.0/111/messages"] != 1 || posts["/v25.0/110/messages"] != 0 {
		t.Fatalf("reply left from the wrong number: %v", posts)
	}
	provider := ""
	for _, d := range state["deliveries"].([]any) {
		row := d.(map[string]any)
		if row["request_key"] == "reply-two" {
			provider, _ = row["provider_message_id"].(string)
			if row["site_id"] != "sim-site-2" {
				t.Fatalf("send journaled for %v", row["site_id"])
			}
		}
	}
	status := map[string]any{"id": provider, "status": "delivered", "recipient_id": "919800000111", "timestamp": fmt.Sprint(time.Now().Unix())}
	value := map[string]any{"messaging_product": "whatsapp", "metadata": map[string]any{"phone_number_id": "111"}, "statuses": []any{status}}
	l.call("emit", map[string]any{"payload": map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "221", "changes": []any{map[string]any{"field": "messages", "value": value}}}}}})
	cores.waitRow(t, "sim-site-2", provider, "delivered", "sim-wa-2")
}

func exerciseSiteBots(t *testing.T, l *launcher, cores *fakeCores, two string) {
	for i, bot := range []string{"sim-bot-1", "sim-bot-2"} {
		update := map[string]any{"update_id": 950 + i, "message": map[string]any{"message_id": 70 + i, "date": time.Now().Unix(), "chat": map[string]any{"id": 42, "type": "private"}, "from": map[string]any{"id": 43, "first_name": "Customer"}, "text": "Hello " + bot}}
		if l.call("telegram/emit", map[string]any{"key": bot, "payload": update})["status"] != float64(200) {
			t.Fatal("Telegram ingress rejected for " + bot)
		}
	}
	cores.waitRow(t, "sim-site-1", "Hello sim-bot-1", "sim-bot-1")
	cores.waitRow(t, "sim-site-2", "Hello sim-bot-2", "relay_sim_bot_2")
	l.mcpError(two, "telegram.message", map[string]any{"channel": "sim-bot-1", "request_key": "foreign-bot", "operation": "text", "chat_id": 42, "text": "Not mine"})
	l.mcp(two, "telegram.message", map[string]any{"request_key": "bot-reply-two", "operation": "text", "chat_id": 42, "text": "Reply from bot 2"}, 200)
	l.wait(func(s map[string]any) bool {
		for _, v := range s["channel_deliveries"].([]any) {
			r := v.(map[string]any)
			if r["request_key"] == "bot-reply-two" {
				return r["state"] == "submitted" && r["site_id"] == "sim-site-2"
			}
		}
		return false
	})
	messages := l.call("providers", nil)["telegram"].(map[string]any)["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["bot"] != "relay_sim_bot_2" {
		t.Fatalf("reply left from the wrong bot: %v", messages)
	}
}

func exerciseSiteMailboxes(t *testing.T, l *launcher, cores *fakeCores, two string) {
	raw := "From: customer@example.invalid\r\nTo: inbox2@sim.invalid\r\nSubject: Order two\r\nMessage-ID: <site-two@example.invalid>\r\n\r\nHello mailbox 2"
	if l.call("email/emit", map[string]any{"key": "sim-mail-2", "raw": raw, "authentication": "pass", "id": "mail-two", "webhook": true})["status"] != float64(200) {
		t.Fatal("email webhook rejected")
	}
	cores.waitRow(t, "sim-site-2", "Order two", "inbox2@sim.invalid")
	l.mcp(two, "email.send", map[string]any{"request_key": "mail-reply-two", "to": []string{"customer@example.invalid"}, "subject": "Re: Order two", "body": "Reply from mailbox 2"}, 200)
	l.wait(func(s map[string]any) bool {
		for _, v := range s["channel_deliveries"].([]any) {
			r := v.(map[string]any)
			if r["request_key"] == "mail-reply-two" {
				return r["state"] == "submitted"
			}
		}
		return false
	})
	sent := l.call("providers", nil)["email"].(map[string]any)["sent"].([]any)
	if len(sent) != 1 || sent[0].(map[string]any)["from"] != "<inbox2@sim.invalid>" {
		t.Fatalf("reply left from the wrong mailbox: %v", sent)
	}
	for _, row := range cores.rows("sim-site-1") {
		if strings.Contains(fmt.Sprint(row), "Order two") {
			t.Fatal("site 1 received site 2's mail")
		}
	}
}

func exerciseSiteBulk(t *testing.T, l *launcher, cores *fakeCores, two string) {
	l.call("bulk", map[string]any{"numbers": []string{"916200000001", "916200000002"}})
	messages := []any{}
	for _, to := range []string{"916200000001", "916200000002"} {
		messages = append(messages, map[string]any{"key": "bulk-" + to, "message": map[string]any{"operation": "templates.send", "to": to, "name": "offer", "language": "en_US"}})
	}
	l.mcp(two, "whatsapp.bulk", map[string]any{"messages": messages}, 200)
	cores.waitRow(t, "sim-site-2", "916200000002", "read", "sim-wa-2")
	for _, row := range cores.rows("sim-site-1") {
		if strings.Contains(fmt.Sprint(row), "916200000002") {
			t.Fatal("site 1 received site 2's campaign updates")
		}
	}
}
