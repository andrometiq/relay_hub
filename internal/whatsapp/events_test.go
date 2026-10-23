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

func TestWebhookUnknownAccountIsForbidden(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/webhooks/whatsapp/missing", strings.NewReader(`{}`))
		r.SetPathValue("account", "missing")
		w := httptest.NewRecorder()
		s.Webhook(w, r)
		if w.Code != 403 {
			t.Fatalf("%s status %d want 403", method, w.Code)
		}
	}
}

func TestSharedWebhookRoutesByVerificationTokenAndPayload(t *testing.T) {
	a := testAccount()
	a.AppSecret = "app-secret"
	a.VerifyToken = "verify-secret"
	s, err := New([]Account{a})
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
