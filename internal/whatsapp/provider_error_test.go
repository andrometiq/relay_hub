package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProviderErrorKeepsOnlySafeMetaText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"(#132001) Template name does not exist in the translation","code":132001,"error_subcode":2494073,"error_user_title":"Template name does not exist","error_user_msg":"winter_launch\u0000 does not exist in en_US.\r\nCheck it.","error_data":{"details":"to 919999999999"},"fbtrace_id":"trace-secret"}}`))
	}))
	defer server.Close()
	svc, _ := New([]Account{testAccount()})
	svc.base = server.URL
	_, err := svc.Execute(context.Background(), testAccount(), Input{Operation: "templates.list"})
	var provider *ProviderError
	if !errors.As(err, &provider) {
		t.Fatal("expected provider error", err)
	}
	if provider.Code != 132001 || provider.Subcode != 2494073 || provider.Title != "Template name does not exist" || provider.Message != "winter_launch does not exist in en_US. Check it." {
		t.Fatalf("unexpected detail %+v", provider)
	}
	text := err.Error()
	if !strings.Contains(text, "(code 132001): Template name does not exist: winter_launch does not exist") {
		t.Fatal("error text lacks Meta detail:", text)
	}
	raw, _ := json.Marshal(provider)
	if strings.Contains(string(raw)+text, "trace-secret") || strings.Contains(string(raw)+text, "919999999999") {
		t.Fatal("provider body leaked", string(raw), text)
	}
	if long := safeText(strings.Repeat("é", 400)); len([]rune(long)) != 300 {
		t.Fatal("safe text is not bounded", len([]rune(long)))
	}
}

func TestReceiptErrorsAndBulkLaneShape(t *testing.T) {
	raw := []byte(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"wamid.a","status":"failed","timestamp":"10","biz_opaque_callback_data":"relay-outbox:42","errors":[{"code":131049,"title":"Not delivered","message":"Not delivered","error_data":{"details":"Healthy ecosystem engagement."}}]},{"id":"wamid.b","status":"deleted","timestamp":"11"}]}}]}]}`)
	statuses := extractStatuses(raw)
	if len(statuses) != 1 || statuses[0].TrackingID != 42 || string(statuses[0].Error) != `{"code":131049,"message":"Healthy ecosystem engagement.","title":"Not delivered"}` {
		t.Fatalf("unexpected receipt projection %+v %s", statuses, statuses[0].Error)
	}
	// An unknown receipt kind keeps the event in the status lane.
	if statusCount(raw) != 2 {
		t.Fatal("raw receipts must be counted")
	}
	if bulkError([]byte(`{"http_status":400,"code":131009,"title":"Bad","message":"Fix","retry_after":0}`)) == nil || bulkError([]byte(`{"http_status":503}`)) != nil {
		t.Fatal("bulk error must keep coded provider errors only")
	}
}

func TestParseBulkWindow(t *testing.T) {
	for value, want := range map[string]time.Duration{"": DefaultBulkWindow, "45s": 45 * time.Second, "1s": time.Second, "5m": 5 * time.Minute} {
		if got, err := ParseBulkWindow(value); err != nil || got != want {
			t.Fatal(value, got, err)
		}
	}
	for _, value := range []string{"500ms", "6m", "thirty"} {
		if _, err := ParseBulkWindow(value); err == nil {
			t.Fatal("accepted", value)
		}
	}
}
