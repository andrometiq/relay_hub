package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/andrometiq/relay_hub/internal/containers"
)

func TestBotEventsReachTheSiteOfTheirBot(t *testing.T) {
	ctx := context.Background()
	pool, dsn := channelTestDB(t)
	store, err := containers.New(pool, "site-callback-key")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveContainer(ctx, containers.Container{ID: "main", Enabled: true}, nil, true); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"bot-a", "bot-b", "bot-c"} {
		token := fmt.Sprintf("50000%d:bot-secret-abcdefghijklmnopqrstu", i+1)
		bot := containers.Channel{Key: key, Channel: "Telegram", Container: "main", Label: key, Enabled: true, Config: map[string]any{"username": key + "_bot"}}
		if err = store.SaveChannel(ctx, bot, token, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, site := range []string{"site-a", "site-b"} {
		if err = store.SaveSite(ctx, containers.Site{ID: site, Origin: "https://" + site + ".invalid", Enabled: true}, nil, true); err != nil {
			t.Fatal(err)
		}
	}
	for site, bot := range map[string]string{"site-a": "bot-a", "site-b": "bot-b"} {
		if err = store.AssignChannels(ctx, site, []containers.ChannelRef{{Channel: "Telegram", Key: bot}}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	received := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(body["events"].([]any)))
	}))
	defer server.Close()
	for _, key := range []string{"bot-a", "bot-b", "bot-c"} {
		saveChannelEvent(t, pool, key, "Telegram", "update-"+key, "hello "+key)
	}
	var unassigned int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM relay_channel_events WHERE site_id IS NULL`).Scan(&unassigned); err != nil || unassigned != 1 {
		t.Fatalf("unassigned bot events: %d %v", unassigned, err)
	}
	var outboxSite string
	if err = pool.QueryRow(ctx, `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash)
 VALUES('bot-a','send-1','telegram.message','{}','hash') RETURNING coalesce(site_id,'')`).Scan(&outboxSite); err != nil || outboxSite != "site-a" {
		t.Fatalf("send site: %q %v", outboxSite, err)
	}

	d := &CallbackDispatcher{URL: dsn}
	for _, a := range []RuntimeCallback{
		{Account: "bot-a", Channel: "Telegram", SiteID: "site-a", Site: server.URL, To: map[string]string{"Telegram": "@bot-a_bot"}, CallbackSecret: "secret-a"},
		{Account: "bot-b", Channel: "Telegram", SiteID: "site-b", Site: server.URL, To: map[string]string{"Telegram": "@bot-b_bot"}, CallbackSecret: "secret-b"},
		// A stale entry for the unassigned bot must not deliver its event.
		{Account: "bot-c", Channel: "Telegram", SiteID: "site-a", Site: server.URL, CallbackSecret: "secret-a"},
	} {
		d.deliver(ctx, a)
	}
	if len(received) != 2 {
		t.Fatalf("callbacks: %d", len(received))
	}
	for _, body := range received {
		key := body["channel_key"].(string)
		event := body["events"].([]any)[0].(map[string]any)
		if body["account"] != map[string]string{"bot-a": "site-a", "bot-b": "site-b"}[key] || event["channel_key"] != key || event["to"] != "@"+key+"_bot" {
			t.Fatalf("callback: %v", body)
		}
	}
}

func TestStatusTargetsJoinALegacyBotAndMailbox(t *testing.T) {
	targets := statusTargets([]RuntimeCallback{
		{Account: "legacy", Channel: "Telegram", To: map[string]string{"Telegram": "@bot"}},
		{Account: "legacy", Channel: "Email", To: map[string]string{"Email": "desk@example.test"}},
		{Account: "mail-b", Channel: "Email", To: map[string]string{"Email": "b@example.test"}},
	})
	if len(targets) != 2 || targets[0].to("Telegram") != "@bot" || targets[0].to("Email") != "desk@example.test" || targets[1].to("Email") != "b@example.test" {
		t.Fatalf("targets: %+v", targets)
	}
}
