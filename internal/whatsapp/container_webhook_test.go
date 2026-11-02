package whatsapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type routedEvents struct {
	routes []Route
	bodies [][]byte
}

func (f *routedEvents) Save(context.Context, string, []byte) error { return nil }
func (f *routedEvents) List(context.Context, string, int64) ([]Event, error) {
	return []Event{}, nil
}
func (f *routedEvents) SaveRouted(_ context.Context, route Route, body, _ []byte) error {
	f.routes, f.bodies = append(f.routes, route), append(f.bodies, body)
	return nil
}

func TestContainerWithoutNumbersVerifiesAndStoresWebhooks(t *testing.T) {
	s, _ := New(nil)
	store := &routedEvents{}
	s.Events = store
	updates := [][2]string{}
	s.AccountUpdate = func(_ context.Context, waba, event string) error {
		updates = append(updates, [2]string{waba, event})
		return nil
	}
	s.ReplaceApps([]App{{ContainerID: "signup", WebhookID: "public-path", AppSecret: "app-secret", VerifyToken: "verify-secret"}})

	r := httptest.NewRequest("GET", "/webhooks/whatsapp/public-path?hub.mode=subscribe&hub.verify_token=verify-secret&hub.challenge=42", nil)
	r.SetPathValue("container", "public-path")
	w := httptest.NewRecorder()
	s.Webhook(w, r)
	if w.Code != 200 || w.Body.String() != "42" {
		t.Fatalf("verification %d %q", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/webhooks/whatsapp/other?hub.mode=subscribe&hub.verify_token=verify-secret&hub.challenge=42", nil)
	r.SetPathValue("container", "other")
	w = httptest.NewRecorder()
	s.Webhook(w, r)
	if w.Code != 404 {
		t.Fatalf("unknown container path %d", w.Code)
	}

	body := `{"object":"whatsapp_business_account","entry":[{"id":"230","changes":[{"field":"account_update","value":{"event":"PARTNER_REMOVED","waba_info":{"waba_id":"230","owner_business_id":"330"}}}]}]}`
	if code := postWebhook(s, "public-path", "wrong-secret", body); code != 403 {
		t.Fatalf("unsigned webhook %d", code)
	}
	if code := postWebhook(s, "public-path", "app-secret", body); code != 200 {
		t.Fatalf("signed webhook %d", code)
	}
	if len(store.routes) != 1 || store.routes[0].Container != "signup" || store.routes[0].Account != "" {
		t.Fatalf("container webhook route %+v", store.routes)
	}
	if len(updates) != 1 || updates[0] != [2]string{"230", "PARTNER_REMOVED"} {
		t.Fatalf("account update hook %v", updates)
	}
}

func TestLargeHistoryWebhookIsAcceptedAndNotIncoming(t *testing.T) {
	a := testAccount()
	a.AppSecret = "app-secret"
	s, _ := New([]Account{a})
	store := &routedEvents{}
	s.Events = store
	text := strings.Repeat("x", 3<<20)
	history := map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": a.WABAID, "changes": []any{map[string]any{
		"field": "history", "value": map[string]any{"messaging_product": "whatsapp", "metadata": map[string]any{"display_phone_number": "15550001111", "phone_number_id": a.PhoneID},
			"history": []any{map[string]any{"metadata": map[string]any{"phase": 0, "chunk_order": 1, "progress": 30},
				"threads": []any{map[string]any{"id": "15550002222", "messages": []any{map[string]any{"from": "15550002222", "id": "wamid.H1", "timestamp": "1", "type": "text", "text": map[string]any{"body": text}}}}}}}}}}}}}
	raw, _ := json.Marshal(history)
	if code := postWebhook(s, a.ID, "app-secret", string(raw)); code != 200 {
		t.Fatalf("history webhook %d", code)
	}
	if len(store.routes) != 1 || store.routes[0].Account != a.ID {
		t.Fatalf("history route %+v", store.routes)
	}
	if incoming := extractIncoming(store.bodies[0]); len(incoming) != 0 {
		t.Fatal("history treated as incoming messages")
	}
	if statuses := extractStatuses(store.bodies[0]); len(statuses) != 0 {
		t.Fatal("history treated as statuses")
	}
	echo := `{"object":"whatsapp_business_account","entry":[{"id":"` + a.WABAID + `","changes":[{"field":"smb_message_echoes","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"` + a.PhoneID + `"},"message_echoes":[{"from":"15550001111","to":"15550002222","id":"wamid.E1","timestamp":"1","type":"text","text":{"body":"hi"}}]}}]}]}`
	if code := postWebhook(s, a.ID, "app-secret", echo); code != 200 {
		t.Fatalf("echo webhook %d", code)
	}
	if incoming := extractIncoming(store.bodies[1]); len(incoming) != 0 {
		t.Fatal("echo treated as incoming message")
	}
	tooLarge := `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"value":{"x":"` + strings.Repeat("y", MaxWebhookBytes) + `"}}]}]}`
	if code := postWebhook(s, a.ID, "app-secret", tooLarge); code != 413 {
		t.Fatalf("oversized webhook %d", code)
	}
}
