package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeEvents struct {
	saved int
	fail  bool
}

func (f *fakeEvents) Save(context.Context, string, []byte) error {
	if f.fail {
		return errors.New("db down")
	}
	f.saved++
	return nil
}
func (f *fakeEvents) List(context.Context, string, int64) ([]Event, error) { return []Event{}, nil }
func TestWebhookVerificationAndPersistence(t *testing.T) {
	a := testAccount()
	a.AppSecret = "app-secret"
	a.VerifyToken = "verify-secret"
	s, _ := New([]Account{a})
	store := &fakeEvents{}
	s.Events = store
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"value":{"metadata":{"phone_number_id":"111"},"calls":[{"id":"call-1","event":"connect"}]}}]}]}`
	for _, tc := range []struct {
		signature bool
		fail      bool
		status    int
	}{{false, false, 403}, {true, false, 200}, {true, true, 503}} {
		store.fail = tc.fail
		r := httptest.NewRequest("POST", "/webhooks/whatsapp/site-a", strings.NewReader(body))
		r.SetPathValue("account", "site-a")
		if tc.signature {
			mac := hmac.New(sha256.New, []byte(a.AppSecret))
			mac.Write([]byte(body))
			r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		w := httptest.NewRecorder()
		s.Webhook(w, r)
		if w.Code != tc.status {
			t.Fatalf("status %d want %d", w.Code, tc.status)
		}
		select {
		case <-s.callbackWake:
			if tc.status != 200 || store.saved != 1 {
				t.Fatal("woke before persistence succeeded")
			}
		default:
			if tc.status == 200 {
				t.Fatal("committed realtime event did not wake delivery")
			}
		}
	}
	if store.saved != 1 {
		t.Fatal("unverified event persisted")
	}
	r := httptest.NewRequest("GET", "/webhooks/whatsapp/site-a?hub.mode=subscribe&hub.verify_token=verify-secret&hub.challenge=123", nil)
	r.SetPathValue("account", "site-a")
	w := httptest.NewRecorder()
	s.Webhook(w, r)
	if w.Code != 200 || w.Body.String() != "123" {
		t.Fatal("verification failed")
	}
}

func TestStatusWebhookDoesNotWakeRealtime(t *testing.T) {
	a := testAccount()
	a.AppSecret = "app-secret"
	s, _ := New([]Account{a})
	s.Events = &fakeEvents{}
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"value":{"metadata":{"phone_number_id":"111"},"statuses":[{"id":"out","status":"read"}]}}]}]}`
	mac := hmac.New(sha256.New, []byte(a.AppSecret))
	mac.Write([]byte(body))
	r := httptest.NewRequest("POST", "/webhooks/whatsapp/site-a", strings.NewReader(body))
	r.SetPathValue("account", "site-a")
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.Webhook(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	select {
	case <-s.callbackWake:
		t.Fatal("status storm woke realtime lane")
	default:
	}
}

func TestWebhookUnknownPathIsNotFound(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/webhooks/whatsapp/missing", strings.NewReader(`{}`))
		r.SetPathValue("account", "missing")
		w := httptest.NewRecorder()
		s.Webhook(w, r)
		if w.Code != 404 {
			t.Fatalf("%s status %d want 404", method, w.Code)
		}
	}
}

func TestSharedWebhookRoutesByVerificationTokenAndPayload(t *testing.T) {
	a := testAccount()
	a.AppSecret = "app-secret"
	a.VerifyToken = "verify-secret"
	b := a
	b.ID = "second"
	b.PhoneID = "333"
	b.CoreToken = ""
	s, err := New([]Account{a, b})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeEvents{}
	s.Events = store

	verify := httptest.NewRequest("GET", "/webhook/whatsapp?hub.mode=subscribe&hub.verify_token=verify-secret&hub.challenge=shared", nil)
	verified := httptest.NewRecorder()
	s.Webhook(verified, verify)
	if verified.Code != 200 || verified.Body.String() != "shared" {
		t.Fatalf("verification: %d %q", verified.Code, verified.Body.String())
	}

	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"value":{"metadata":{"phone_number_id":"111"},"messages":[{"id":"wamid.1","from":"919999999999"}]}}]}]}`
	request := httptest.NewRequest("POST", "/webhook/whatsapp", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(a.AppSecret))
	mac.Write([]byte(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	s.Webhook(response, request)
	if response.Code != 200 || store.saved != 1 {
		t.Fatalf("shared webhook: status=%d saved=%d", response.Code, store.saved)
	}
}

// Meta sends a customer's poll as type unsupported (code 131051) with no poll
// content, and a reply-button tap as interactive with the poll message in
// context.id. Both reach Core unchanged on the realtime lane.
func TestPollWebhooksKeepMetaReasonAndReplyContext(t *testing.T) {
	body := []byte(`{"entry":[{"changes":[{"field":"messages","value":{"messaging_product":"whatsapp","messages":[
{"from":"919999999999","id":"wamid.poll","timestamp":"1792948421","type":"unsupported","unsupported":{"type":"poll_creation"},"errors":[{"code":131051,"title":"Message type unknown","error_data":{"details":"Message type is currently not supported."}}]},
{"from":"919999999999","id":"wamid.vote","timestamp":"1792948422","type":"interactive","context":{"from":"919876543210","id":"wamid.sent-poll"},"interactive":{"type":"button_reply","button_reply":{"id":"poll-2","title":"Blue"}}}]}}]}]}`)
	if lane := eventLane(body); lane != "realtime" {
		t.Fatalf("lane = %s", lane)
	}
	incoming := extractIncoming(body)
	if len(incoming) != 2 {
		t.Fatalf("got %d messages", len(incoming))
	}
	poll, vote := incoming[0], incoming[1]
	if poll["type"] != "unsupported" || poll["unsupported"].(map[string]any)["type"] != "poll_creation" || poll["errors"].([]any)[0].(map[string]any)["code"] != float64(131051) {
		t.Fatalf("poll reason lost: %#v", poll)
	}
	reply := vote["interactive"].(map[string]any)["button_reply"].(map[string]any)
	if vote["context"].(map[string]any)["id"] != "wamid.sent-poll" || reply["id"] != "poll-2" {
		t.Fatalf("vote context lost: %#v", vote)
	}
}
