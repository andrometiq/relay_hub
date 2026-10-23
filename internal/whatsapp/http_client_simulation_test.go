package whatsapp

import (
	"net/http"
	"testing"
	"time"
)

func TestSimulationHTTPClientKeepsProductionGuards(t *testing.T) {
	if _, err := NewWithHTTPClient([]Account{testAccount()}, nil); err == nil {
		t.Fatal("nil injected HTTP client accepted")
	}
	invalid := testAccount()
	invalid.PhoneID = "invalid-phone"
	if _, err := NewWithHTTPClient([]Account{invalid}, &http.Client{}); err == nil {
		t.Fatal("injected HTTP client bypassed account validation")
	}
	for _, timeout := range []time.Duration{0, 2 * time.Second, time.Hour} {
		original := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
		service, err := NewWithHTTPClient([]Account{testAccount()}, original)
		if err != nil {
			t.Fatal(err)
		}
		if service.base != "https://graph.facebook.com" || service.client.Timeout <= 0 || service.client.Timeout > 12*time.Second {
			t.Fatal("injected client removed fixed destination or request deadline")
		}
		if service.client.CheckRedirect(nil, nil) == nil {
			t.Fatal("injected client allowed provider redirects")
		}
		if original.Timeout != timeout || original.CheckRedirect(nil, nil) != nil {
			t.Fatal("constructor mutated caller-owned HTTP configuration")
		}
	}
}
