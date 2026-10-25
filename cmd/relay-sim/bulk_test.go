package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBulkReceiptsScheduleStatusesFailuresAndReplies(t *testing.T) {
	b := newBulkReceipts()
	b.numbers = map[string]bool{"916200000001": true, "916200000002": true}
	b.failures["916200000002"] = 131049
	b.repliers["916200000001"] = true
	if b.accept("916200000009", "wamid.other", "relay-outbox:9") {
		t.Fatal("a number outside the campaign was answered")
	}
	b.accept("916200000001", "wamid.one", "relay-outbox:1")
	b.accept("916200000002", "wamid.two", "relay-outbox:2")
	b.accept("916200000001", "wamid.again", "relay-outbox:3")
	if b.stats.Sends != 3 || b.stats.Duplicates != 1 {
		t.Fatalf("sends %d duplicates %d", b.stats.Sends, b.stats.Duplicates)
	}
	rows := b.take(time.Now().Add(time.Minute))
	statuses, replies := map[string][]string{}, 0
	for _, row := range rows {
		if row.reply != nil {
			replies++
			if row.reply.message["context"].(map[string]any)["id"] != "wamid.one" && row.reply.message["context"].(map[string]any)["id"] != "wamid.again" {
				t.Fatal("reply lacks the campaign message context")
			}
			continue
		}
		statuses[row.status["id"].(string)] = append(statuses[row.status["id"].(string)], row.status["status"].(string))
	}
	if replies != 2 || len(statuses["wamid.one"]) != 3 || len(statuses["wamid.two"]) != 1 || statuses["wamid.two"][0] != "failed" {
		t.Fatalf("unexpected schedule: %v replies %d", statuses, replies)
	}
	if len(b.due) != 0 {
		t.Fatal("taken receipts stayed due")
	}
}

func TestBulkReceiptsRetryWebhooksHubRefused(t *testing.T) {
	b := newBulkReceipts()
	b.numbers["916200000001"] = true
	b.accept("916200000001", "wamid.one", "relay-outbox:1")
	codes := []int{503, 200, 200, 200}
	posted := []map[string]any{}
	b.post = func(_ context.Context, payload []byte) (int, error) {
		var body map[string]any
		json.Unmarshal(payload, &body)
		posted = append(posted, body)
		code := codes[0]
		codes = codes[1:]
		return code, nil
	}
	b.flush(context.Background(), b.take(time.Now().Add(time.Minute)))
	if b.stats.WebhookFailures[503] != 1 || b.stats.Statuses != 0 || len(b.due) != 3 {
		t.Fatalf("refused envelope was not kept: %+v due %d", b.stats, len(b.due))
	}
	b.flush(context.Background(), b.take(time.Now().Add(time.Minute)))
	if b.stats.Statuses != 3 || len(b.due) != 0 || len(posted) != 2 {
		t.Fatalf("retry did not deliver every status once: %+v posted %d", b.stats, len(posted))
	}
}
