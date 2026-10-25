package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
