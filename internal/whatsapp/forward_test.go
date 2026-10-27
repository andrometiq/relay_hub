package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type forwarded struct {
	container, signature string
	body                 []byte
}

func forwardingService(t *testing.T) (*Service, *fakeEvents, *[]forwarded) {
	t.Helper()
	a := testAccount()
	a.AppSecret, a.VerifyToken = "app-secret", "verify-secret"
	s, err := New([]Account{a})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeEvents{}
	s.Events = store
	calls := &[]forwarded{}
	s.Forward = func(_ context.Context, container string, header http.Header, body []byte) {
		*calls = append(*calls, forwarded{container, header.Get("X-Hub-Signature-256"), body})
	}
	return s, store, calls
}

func TestWebhookForwardsOnlyVerifiedStoredRequests(t *testing.T) {
	s, store, calls := forwardingService(t)
	known := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"value":{"metadata":{"phone_number_id":"111"},"messages":[{"id":"m1","from":"91"}]}}]}]}`
	unknown := strings.ReplaceAll(known, `"111"`, `"999"`)

	if code := postWebhook(s, "site-a", "wrong", known); code != 403 || len(*calls) != 0 {
		t.Fatalf("unverified request: status %d, forwards %d", code, len(*calls))
	}
	store.fail = true
	if code := postWebhook(s, "site-a", "app-secret", known); code != 503 || len(*calls) != 0 {
		t.Fatalf("unstored request: status %d, forwards %d", code, len(*calls))
	}
	store.fail = false
	for _, body := range []string{known, unknown} {
		mac := hmac.New(sha256.New, []byte("app-secret"))
		mac.Write([]byte(body))
		signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if code := postWebhook(s, "site-a", "app-secret", body); code != 200 {
			t.Fatalf("status %d", code)
		}
		last := (*calls)[len(*calls)-1]
		if string(last.body) != body || last.signature != signature || last.container != "site-a" {
			t.Fatalf("forward changed the request: %+v", last)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("forwards %d, want 2", len(*calls))
	}

	r := httptest.NewRequest("GET", "/webhooks/whatsapp/site-a?hub.mode=subscribe&hub.verify_token=verify-secret&hub.challenge=7", nil)
	r.SetPathValue("container", "site-a")
	w := httptest.NewRecorder()
	s.Webhook(w, r)
	if w.Code != 200 || len(*calls) != 2 {
		t.Fatal("verification handshake was forwarded")
	}
}
