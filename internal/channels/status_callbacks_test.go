package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatusCallbackAcknowledgement(t *testing.T) {
	for _, reply := range []struct {
		body   string
		status int
		ok     bool
	}{{`{"message":{"accepted":1}}`, 200, true}, {`{"message":{"accepted":0}}`, 200, false}, {`{"message":{"accepted":1}}`, 503, false}, {`{}`, 200, false}} {
		t.Run(reply.body+http.StatusText(reply.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				mac := hmac.New(sha256.New, []byte("callback-secret"))
				mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
				mac.Write(raw)
				if r.URL.Path != "/api/method/relay_core.inbound.receive_channel_statuses" || r.Header.Get("Authorization") != "token key:secret" || r.Header.Get("X-Relay-Signature") != hex.EncodeToString(mac.Sum(nil)) {
					t.Error("invalid callback routing or authentication")
				}
				var body struct {
					Account  string          `json:"account"`
					Statuses []StatusReceipt `json:"statuses"`
				}
				if json.Unmarshal(raw, &body) != nil || body.Account != "alpha" || len(body.Statuses) != 1 || body.Statuses[0].RequestKey != "stable" {
					t.Error("wrong batch")
				}
				w.WriteHeader(reply.status)
				io.WriteString(w, reply.body)
			}))
			defer server.Close()
			err := postStatusBatch(context.Background(), RuntimeCallback{Account: "alpha", Site: server.URL, APIKey: "key", APISecret: "secret", CallbackSecret: "callback-secret"}, []StatusReceipt{{ID: 1, DeliveryID: 2, RequestKey: "stable", Operation: "email.send", State: "submitted", Receipt: json.RawMessage(`{"sent":true}`), OccurredAt: time.Now()}})
			if (err == nil) != reply.ok {
				t.Fatalf("acknowledgement accepted incorrectly: %v", err)
			}
		})
	}
}
