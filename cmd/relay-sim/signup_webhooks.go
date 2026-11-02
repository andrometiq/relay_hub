package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// signupWebhooks sends the webhooks that signup calls cause, one at a time
// and in order, through the signed simulation webhook path.
type signupWebhooks struct {
	mu      sync.Mutex
	pending chan []byte
	sent    []map[string]any
	gap     time.Duration
	post    func(context.Context, []byte) (int, error)
}

func newSignupWebhooks() *signupWebhooks {
	return &signupWebhooks{pending: make(chan []byte, 4096), sent: []map[string]any{}, gap: 100 * time.Millisecond}
}

func (w *signupWebhooks) queue(payload []byte) {
	w.pending <- payload
}

// run posts queued webhooks. Like Meta it retries a refused or failed post a
// few times, then drops it.
func (w *signupWebhooks) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case payload := <-w.pending:
			status, attempts := w.deliver(ctx, payload)
			w.mu.Lock()
			w.sent = append(w.sent, map[string]any{"fields": webhookFields(payload), "bytes": len(payload), "status": status, "attempts": attempts})
			w.mu.Unlock()
			time.Sleep(w.gap)
		}
	}
}

func (w *signupWebhooks) deliver(ctx context.Context, payload []byte) (int, int) {
	status := 0
	for attempt := 1; attempt <= 5; attempt++ {
		var err error
		status, err = w.post(ctx, payload)
		if err == nil && status == 200 {
			return status, attempt
		}
		select {
		case <-ctx.Done():
			return status, attempt
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
	return status, 5
}

func (w *signupWebhooks) snapshot() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]any{}, w.sent...)
}

// webhookFields lists the change fields of one webhook payload.
func webhookFields(payload []byte) []string {
	var body struct {
		Entry []struct {
			Changes []struct {
				Field string `json:"field"`
			} `json:"changes"`
		} `json:"entry"`
	}
	fields := []string{}
	if json.Unmarshal(payload, &body) == nil {
		for _, entry := range body.Entry {
			for _, change := range entry.Changes {
				fields = append(fields, change.Field)
			}
		}
	}
	return fields
}

// webhookCapture keeps small webhook bodies and only the size of large ones.
func webhookCapture(payload []byte) []byte {
	if len(payload) <= 64<<10 {
		return payload
	}
	summary, _ := json.Marshal(map[string]any{"fields": webhookFields(payload), "bytes": len(payload)})
	return summary
}

func envelope(entry, field string, value any) []byte {
	payload, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": entry, "time": time.Now().Unix(),
		"changes": []any{map[string]any{"field": field, "value": value}}}}})
	return payload
}

func (p *esPhone) metadata() map[string]any {
	return map[string]any{"display_phone_number": p.Display, "phone_number_id": p.ID}
}

func accountUpdate(waba *esWABA, event string) []byte {
	return envelope(waba.ID, "account_update", map[string]any{"event": event, "waba_info": map[string]any{"waba_id": waba.ID, "owner_business_id": waba.Business}})
}

// esContacts are the Business app contacts that every state sync returns.
var esContacts = []struct{ Full, First, Phone string }{
	{"Sim Contact One", "Sim", "919900000001"},
	{"Sim Contact Two", "Sim", "919900000002"},
	{"Sim Contact Three", "Sim", "919900000003"},
}

func stateSync(phone *esPhone) []byte {
	rows := []any{}
	for i, c := range esContacts {
		rows = append(rows, map[string]any{"type": "contact", "action": "add", "contact": map[string]any{"full_name": c.Full, "first_name": c.First, "phone_number": c.Phone},
			"metadata": map[string]any{"timestamp": fmt.Sprint(1767225600 + i)}})
	}
	return envelope(phone.WABA, "smb_app_state_sync", map[string]any{"messaging_product": "whatsapp", "metadata": phone.metadata(), "state_sync": rows})
}

func historyDeclined(phone *esPhone) []byte {
	return envelope(phone.WABA, "history", map[string]any{"messaging_product": "whatsapp", "metadata": phone.metadata(),
		"history": []any{map[string]any{"errors": []any{map[string]any{"code": 2593109, "title": "History sharing was declined by the business",
			"message": "History sharing was declined by the business", "error_data": map[string]any{"details": "The business chose not to share chat history"}}}}}})
}

// historyPhase is one history webhook: phase 0, 1 and 2 report 30, 70 and
// 100 percent. Phase 1 is one chunk over 2 MiB with long text messages.
func historyPhase(phone *esPhone, phase int, now time.Time) []byte {
	count, body := 3, "History message"
	if phase == 1 {
		count, body = 600, strings.Repeat("Long history text. ", 200)
	}
	threads := []any{}
	for t, contact := range esContacts[:2] {
		messages := []any{}
		for i := range count {
			from, status := contact.Phone, "READ"
			if i%2 == 1 {
				from, status = phone.Display, "DELIVERED"
			}
			at := now.Add(-time.Duration(30*(3-phase)) * 24 * time.Hour).Add(time.Duration(i) * time.Minute).Unix()
			messages = append(messages, map[string]any{"from": from, "id": fmt.Sprintf("wamid.sim.history.%s.%d.%d.%d", phone.ID, phase, t, i), "timestamp": fmt.Sprint(at),
				"type": "text", "text": map[string]any{"body": fmt.Sprintf("%s %d.%d.%d", body, phase, t, i)}, "history_context": map[string]any{"status": status}})
		}
		threads = append(threads, map[string]any{"id": contact.Phone, "messages": messages})
		if phase == 1 {
			break
		}
	}
	progress := []int{30, 70, 100}[phase]
	return envelope(phone.WABA, "history", map[string]any{"messaging_product": "whatsapp", "metadata": phone.metadata(),
		"history": []any{map[string]any{"metadata": map[string]any{"phase": phase, "chunk_order": 1, "progress": progress}, "threads": threads}}})
}

func messageEcho(phone *esPhone, to, text string, now time.Time) []byte {
	return envelope(phone.WABA, "smb_message_echoes", map[string]any{"messaging_product": "whatsapp", "metadata": phone.metadata(),
		"message_echoes": []any{map[string]any{"from": phone.Display, "to": to, "id": providerID("wamid.sim.echo"), "timestamp": fmt.Sprint(now.Unix()),
			"type": "text", "text": map[string]any{"body": text}}}})
}

func nameUpdate(phone *esPhone) []byte {
	return envelope(phone.WABA, "phone_number_name_update", map[string]any{"display_phone_number": phone.Display, "decision": phone.NameStatus,
		"requested_verified_name": phone.VerifiedName, "rejection_reason": nil})
}

func messageStatus(phone *esPhone, id, to, status string, now time.Time) []byte {
	return envelope(phone.WABA, "messages", map[string]any{"messaging_product": "whatsapp", "metadata": phone.metadata(),
		"statuses": []any{map[string]any{"id": id, "status": status, "timestamp": fmt.Sprint(now.Unix()), "recipient_id": to}}})
}

// sortRows orders state rows by id, else by hash, so dumps are stable.
func sortRows(rows []map[string]any) {
	key := func(row map[string]any) string {
		if id, ok := row["id"].(string); ok {
			return id
		}
		value, _ := row["hash"].(string)
		return value
	}
	sort.Slice(rows, func(i, j int) bool { return key(rows[i]) < key(rows[j]) })
}
