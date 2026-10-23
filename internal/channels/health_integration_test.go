package channels

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/telegram"
)

func TestHealthBoundedHistoryAndIndexes(t *testing.T) {
	db, _ := channelTestDB(t)
	ctx := t.Context()
	for _, sql := range []string{
		`INSERT INTO relay_channel_events(account,channel,direction,provider_id)
 SELECT 'health-load','Telegram','inbound',n::text FROM generate_series(1,15000) n`,
		`INSERT INTO relay_channel_events(account,channel,direction,provider_id)
 SELECT 'other','Email','inbound',n::text FROM generate_series(1,15000) n`,
		`INSERT INTO relay_channel_outbox(account,operation,request_key,payload,payload_hash,state,attempts,provider_error,next_attempt)
 SELECT 'health-load','telegram.message',n::text,'{}','hash','queued',2,'{"ErrorCode":429}',now()+interval '1 hour' FROM generate_series(1,15000) n`,
		// Newer completed history must neither enter the sample nor be scanned.
		`INSERT INTO relay_channel_outbox(account,operation,request_key,payload,payload_hash,state)
 SELECT 'health-load','telegram.message','done-'||n,'{}','hash','submitted' FROM generate_series(1,30000) n`,
		`ANALYZE relay_channel_events`, `ANALYZE relay_channel_callback_delivery`, `ANALYZE relay_channel_outbox`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	h, err := AccountHealth(ctx, db, "health-load", "Telegram")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Health over 30,000 events and 45,000 deliveries: %s", time.Since(started))
	if h.Pending != healthRowLimit || h.DeliveryPending != healthRowLimit || h.RateLimited != healthRowLimit ||
		!h.InboundTruncated || !h.OutboxTruncated || h.NextAttempt == "" || h.LastUpdate == "" {
		t.Fatalf("incorrect capped health: %+v", h)
	}
	for _, tc := range []struct{ sql, channel, index string }{
		{inboundHealthSQL, "Telegram", "relay_channel_health_inbound"},
		{outboxHealthSQL, "telegram", "relay_channel_health_outbox"},
	} {
		var raw []byte
		if err := db.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+tc.sql,
			"health-load", tc.channel, healthRowLimit+1, healthRowLimit).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plans []map[string]any
		if err := json.Unmarshal(raw, &plans); err != nil {
			t.Fatal(err)
		}
		found := false
		var check func(map[string]any)
		check = func(node map[string]any) {
			if node["Index Name"] == tc.index {
				found = true
			}
			if relation, ok := node["Relation Name"].(string); ok {
				rows := node["Actual Rows"].(float64) * node["Actual Loops"].(float64)
				if removed, ok := node["Rows Removed by Filter"].(float64); ok {
					rows += removed * node["Actual Loops"].(float64)
				}
				if rows > healthRowLimit+1 {
					t.Errorf("%s examined %.0f rows beyond cap", relation, rows)
				}
			}
			children, _ := node["Plans"].([]any)
			for _, child := range children {
				check(child.(map[string]any))
			}
		}
		check(plans[0]["Plan"].(map[string]any))
		if !found {
			t.Errorf("missing bounded index %s: %s", tc.index, raw)
		}
	}
	// A blocked database must not tie up an HTTP health refresh indefinitely.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE relay_channel_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	if _, err = AccountHealth(ctx, db, "health-load", "Telegram"); err == nil {
		t.Fatal("blocked query did not time out")
	}
	if elapsed := time.Since(started); elapsed > healthQueryTimeout+time.Second {
		t.Fatalf("query deadline exceeded: %s", elapsed)
	}
}

func TestHealthUsesHubReceptionForTelegramEdits(t *testing.T) {
	db, _ := channelTestDB(t)
	bot, err := telegram.New(telegram.Config{AccountID: "local-edit", BotToken: "123456:" + strings.Repeat("a", 24), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &Registry{Telegram: map[string]*telegram.Client{"local-edit": bot}}
	master := strings.Repeat("m", 32)
	secret, err := r.TelegramWebhookSecret("local-edit", master)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	message := []byte(`{"update_id":1,"message":{"message_id":7,"date":946684800,"chat":{"id":42},"text":"Original"}}`)
	edit := []byte(`{"update_id":2,"edited_message":{"message_id":7,"date":946684800,"edit_date":1790000000,"chat":{"id":42},"text":"Edited"}}`)
	if err = r.TelegramWebhook(ctx, Store{db}, "local-edit", master, []byte(secret), message); err != nil {
		t.Fatal(err)
	}
	first, err := AccountHealth(ctx, db, "local-edit", "Telegram")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.TelegramWebhook(ctx, Store{db}, "local-edit", master, []byte(secret), edit); err != nil {
		t.Fatal(err)
	}
	after, err := AccountHealth(ctx, db, "local-edit", "Telegram")
	if err != nil {
		t.Fatal(err)
	}
	if first.LastUpdate == after.LastUpdate || after.Pending != 2 {
		t.Fatal("new edit did not advance reception health")
	}
	var current bool
	if err = db.QueryRow(ctx, `SELECT bool_and(occurred_at=to_timestamp(946684800) AND received_at>now()-interval '1 minute') FROM relay_channel_events WHERE account='local-edit'`).Scan(&current); err != nil || !current {
		t.Fatal("provider and reception timestamps not preserved", err)
	}
	if err = r.TelegramWebhook(ctx, Store{db}, "local-edit", master, []byte(secret), edit); err != nil {
		t.Fatal(err)
	}
	duplicate, err := AccountHealth(ctx, db, "local-edit", "Telegram")
	if err != nil || duplicate.LastUpdate != after.LastUpdate || duplicate.Pending != 2 {
		t.Fatal("duplicate update changed reception", err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,received_at) VALUES('legacy','Email','inbound','old',NULL)`); err != nil {
		t.Fatal(err)
	}
	legacy, err := AccountHealth(ctx, db, "legacy", "Email")
	if err != nil || legacy.LastUpdate != "" || legacy.Pending != 1 || legacy.InboundTruncated || legacy.OutboxTruncated {
		t.Fatal("unknown reception should stay unknown", err)
	}
}
