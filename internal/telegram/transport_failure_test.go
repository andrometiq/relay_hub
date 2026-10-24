package telegram

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestTransportFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"dns", &net.DNSError{Err: "lookup failed"}, true},
		{"dial", &net.OpError{Op: "dial", Err: errors.New("refused")}, true},
		{"read", &net.OpError{Op: "read", Err: errors.New("reset")}, false},
		{"timeout", errors.New("timeout after sending"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewWithHTTPClient(Config{AccountID: "local", BotToken: "123456:abcdefghijklmnopqrstuv", Enabled: true}, &http.Client{Transport: webhookTransport(func(*http.Request) (*http.Response, error) { return nil, tc.err })})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.call(t.Context(), "sendMessage", map[string]any{"chat_id": 1, "text": "test"})
			var api *APIError
			if !errors.As(err, &api) || api.Retryable != tc.retry || api.OutcomeUnknown == tc.retry {
				t.Fatal("incorrect outcome", err)
			}
		})
	}
}
