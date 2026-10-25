package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Meta-like receipt timing after an accepted campaign send. Replies come after
// one bulk update window, when Core already knows the provider message ID.
const (
	bulkSentAfter      = 300 * time.Millisecond
	bulkDeliveredAfter = 800 * time.Millisecond
	bulkReadAfter      = 1500 * time.Millisecond
	bulkReplyAfter     = 40 * time.Second
	bulkEnvelopeSize   = 100
)

var bulkErrorTitles = map[int]string{131026: "Message undeliverable", 131049: "This message was not delivered to maintain healthy ecosystem engagement."}

// bulkReceipts answers campaign sends with signed status and reply webhooks
// and counts sends, so 5,000-row runs keep no per-message capture.
type bulkReceipts struct {
	mu       sync.Mutex
	numbers  map[string]bool
	failures map[string]int
	repliers map[string]bool
	sent     map[string]int
	due      []bulkWebhook
	stats    bulkStats
	post     func(context.Context, []byte) (int, error)
}

type bulkWebhook struct {
	at     time.Time
	status map[string]any
	reply  *bulkReply
}

type bulkReply struct {
	ID        string `json:"id"`
	To        string `json:"to"`
	Context   string `json:"context"`
	EmittedNS int64  `json:"emitted_unix_ns"`
	message   map[string]any
}

type bulkStats struct {
	Sends           int          `json:"sends"`
	Duplicates      int          `json:"duplicates"`
	FirstNS         int64        `json:"first_unix_ns"`
	LastNS          int64        `json:"last_unix_ns"`
	Statuses        int          `json:"statuses"`
	Webhooks        int          `json:"webhooks"`
	WebhookFailures map[int]int  `json:"webhook_failures"`
	Replies         []*bulkReply `json:"replies"`
}

func newBulkReceipts() *bulkReceipts {
	return &bulkReceipts{numbers: map[string]bool{}, failures: map[string]int{}, repliers: map[string]bool{}, sent: map[string]int{}, stats: bulkStats{Replies: []*bulkReply{}, WebhookFailures: map[int]int{}}}
}

// accept schedules receipts for a send to a configured campaign number.
func (b *bulkReceipts) accept(to, id, tracking string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.numbers[to] {
		return false
	}
	now := time.Now()
	if b.stats.Sends == 0 {
		b.stats.FirstNS = now.UnixNano()
	}
	b.stats.Sends++
	b.stats.LastNS = now.UnixNano()
	b.sent[to]++
	if b.sent[to] > 1 {
		b.stats.Duplicates++
	}
	status := func(name string) map[string]any {
		return map[string]any{"id": id, "recipient_id": to, "status": name, "timestamp": strconv.FormatInt(now.Unix(), 10), "biz_opaque_callback_data": tracking}
	}
	if code := b.failures[to]; code != 0 {
		failed := status("failed")
		failed["errors"] = []any{map[string]any{"code": code, "title": bulkErrorTitles[code], "message": bulkErrorTitles[code], "error_data": map[string]any{"details": bulkErrorTitles[code]}}}
		b.due = append(b.due, bulkWebhook{at: now.Add(bulkSentAfter), status: failed})
		return true
	}
	b.due = append(b.due, bulkWebhook{at: now.Add(bulkSentAfter), status: status("sent")},
		bulkWebhook{at: now.Add(bulkDeliveredAfter), status: status("delivered")},
		bulkWebhook{at: now.Add(bulkReadAfter), status: status("read")})
	if b.repliers[to] {
		reply := &bulkReply{ID: providerID("wamid.sim.bulkreply"), To: to, Context: id}
		reply.message = map[string]any{"id": reply.ID, "from": to, "timestamp": strconv.FormatInt(now.Unix(), 10), "type": "text",
			"text": map[string]any{"body": "Yes, interested"}, "context": map[string]any{"from": "919999999999", "id": id}}
		b.due = append(b.due, bulkWebhook{at: now.Add(bulkReplyAfter), reply: reply})
	}
	return true
}

// take removes receipts that are due, in schedule order.
func (b *bulkReceipts) take(now time.Time) []bulkWebhook {
	b.mu.Lock()
	defer b.mu.Unlock()
	ready, later := []bulkWebhook{}, b.due[:0]
	for _, row := range b.due {
		if row.at.After(now) {
			later = append(later, row)
		} else {
			ready = append(ready, row)
		}
	}
	b.due = later
	return ready
}

func (b *bulkReceipts) run(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			b.flush(ctx, b.take(now))
		}
	}
}

// flush posts due statuses in envelopes of 100 and each reply as its own
// webhook. Like Meta, a webhook that Hub does not accept is retried.
func (b *bulkReceipts) flush(ctx context.Context, rows []bulkWebhook) {
	retry := []bulkWebhook{}
	batch := []bulkWebhook{}
	for _, row := range rows {
		if row.reply == nil {
			batch = append(batch, row)
			if len(batch) == bulkEnvelopeSize {
				retry = append(retry, b.emitStatuses(ctx, batch)...)
				batch = []bulkWebhook{}
			}
			continue
		}
		if row.reply.EmittedNS == 0 {
			row.reply.EmittedNS = time.Now().UnixNano()
		}
		if !b.emit(ctx, map[string]any{"contacts": []any{map[string]any{"profile": map[string]any{"name": "Sim Bulk Reply"}, "wa_id": row.reply.To}}, "messages": []any{row.reply.message}}) {
			retry = append(retry, row)
			continue
		}
		b.mu.Lock()
		b.stats.Replies = append(b.stats.Replies, row.reply)
		b.mu.Unlock()
	}
	if len(batch) > 0 {
		retry = append(retry, b.emitStatuses(ctx, batch)...)
	}
	b.mu.Lock()
	for _, row := range retry {
		row.at = time.Now().Add(time.Second)
		b.due = append(b.due, row)
	}
	b.mu.Unlock()
}

func (b *bulkReceipts) emitStatuses(ctx context.Context, rows []bulkWebhook) []bulkWebhook {
	statuses := make([]any, len(rows))
	for i, row := range rows {
		statuses[i] = row.status
	}
	if !b.emit(ctx, map[string]any{"statuses": statuses}) {
		return rows
	}
	b.mu.Lock()
	b.stats.Statuses += len(statuses)
	b.mu.Unlock()
	return nil
}

func (b *bulkReceipts) emit(ctx context.Context, value map[string]any) bool {
	value["messaging_product"] = "whatsapp"
	value["metadata"] = map[string]any{"display_phone_number": "919999999999", "phone_number_id": "110"}
	payload, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "220", "changes": []any{map[string]any{"field": "messages", "value": value}}}}})
	status, err := b.post(ctx, payload)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.Webhooks++
	if err != nil || status != 200 {
		b.stats.WebhookFailures[status]++
		return false
	}
	return true
}

func (s *simulation) bulkControl(w http.ResponseWriter, r *http.Request) {
	b := s.meta.bulk
	if r.Method == "GET" {
		b.mu.Lock()
		defer b.mu.Unlock()
		writeJSON(w, map[string]any{"stats": b.stats, "pending": len(b.due), "sent": b.sent})
		return
	}
	var in struct {
		Numbers  []string       `json:"numbers"`
		Failures map[string]int `json:"failures"`
		Repliers []string       `json:"repliers"`
	}
	if r.Method != "POST" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, number := range in.Numbers {
		b.numbers[number] = true
	}
	for number, code := range in.Failures {
		if bulkErrorTitles[code] == "" || !b.numbers[number] {
			http.Error(w, "invalid failure", 400)
			return
		}
		b.failures[number] = code
	}
	for _, number := range in.Repliers {
		b.repliers[number] = true
	}
	writeJSON(w, map[string]int{"numbers": len(b.numbers)})
}
