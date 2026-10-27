package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/andrometiq/relay_hub/internal/accounts"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
)

func TestEmailDSNWebhookToStatusCallback(t *testing.T) {
	pool, dsn := channelTestDB(t)
	registry, err := Load([]accounts.RuntimeCredential{{AccountID: "alpha", Channel: "Email", Config: map[string]any{"host": "smtp.invalid", "port": float64(587), "from": "sender@example.invalid", "trusted_authserv_id": "mx.example.invalid", "trusted_dsn_domain": "provider.invalid"}}})
	if err != nil {
		t.Fatal(err)
	}
	o := Outbox{DB: pool}
	id := seedDelivery(t, o, "alpha", "send-one", "email.send", relayemail.Message{To: []string{"first@example.invalid", "second@example.invalid"}, Body: "Body", Subject: "Subject"})
	master := strings.Repeat("m", 32)
	token, _ := registry.EmailWebhookSecret("alpha", master)
	post := func(event, action, code, recipient string) {
		t.Helper()
		raw := fmt.Sprintf("Authentication-Results: mx.example.invalid; dmarc=pass header.from=provider.invalid\r\nReceived: from outside.invalid by mx.example.invalid; today\r\nFrom: postmaster@provider.invalid\r\nTo: sender@example.invalid\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=dsn\r\n\r\n--dsn\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.provider.invalid\r\nOriginal-Message-ID: <sent@example.invalid>\r\n\r\nFinal-Recipient: rfc822; %s\r\nAction: %s\r\nStatus: %s\r\n\r\n--dsn--\r\n", recipient, action, code)
		body, _ := json.Marshal(map[string]any{"id": event, "raw": raw})
		if err := registry.EmailWebhook(t.Context(), Store{DB: pool}, "alpha", master, []byte(token), body); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// Evidence precedes SMTP's commit and must survive a callback attempt.
	post("bounce", "failed", "5.1.1", "first@example.invalid")
	batch, err := PendingStatusReceipts(t.Context(), conn.Conn(), "alpha")
	if err != nil || len(batch) != 0 {
		t.Fatal(batch, err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE relay_channel_outbox SET state='submitted',result='{"message_id":"<sent@example.invalid>"}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	post("bounce", "failed", "5.1.1", "first@example.invalid")
	post("bounce-copy", "failed", "5.1.1", "first@example.invalid")
	post("second-delivered", "delivered", "2.0.0", "second@example.invalid")
	post("unknown-recipient", "delivered", "2.0.0", "outsider@example.invalid")
	batch, err = PendingStatusReceipts(t.Context(), conn.Conn(), "other")
	if err != nil || len(batch) != 0 {
		t.Fatal("cross-account evidence", batch, err)
	}
	var mu sync.Mutex
	var delivered []StatusReceipt
	accepting := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("callback-secret"))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if r.Header.Get("X-Relay-Signature") != hex.EncodeToString(mac.Sum(nil)) || r.URL.Path != "/api/method/relay_core.inbound.receive_channel_statuses" {
			t.Error("invalid callback authentication/path")
		}
		mu.Lock()
		defer mu.Unlock()
		if !accepting {
			w.WriteHeader(503)
			return
		}
		var body struct {
			Statuses []StatusReceipt `json:"statuses"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		delivered = append(delivered, body.Statuses...)
		fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(body.Statuses))
	}))
	defer server.Close()
	callback := RuntimeCallback{Account: "alpha", Site: server.URL, CallbackSecret: "callback-secret"}
	dispatcher := CallbackDispatcher{URL: dsn}
	if err := dispatcher.deliverStatuses(t.Context(), callback); err == nil {
		t.Fatal("failed callback acknowledged")
	}
	batch, err = PendingStatusReceipts(t.Context(), conn.Conn(), "alpha")
	if err != nil || len(batch) != 3 {
		t.Fatal("pending evidence lost", batch, err)
	}
	mu.Lock()
	accepting = true
	mu.Unlock()
	if _, err = pool.Exec(t.Context(), `UPDATE relay_channel_status_schedule SET next_attempt=now()`); err != nil {
		t.Fatal(err)
	}
	if err = dispatcher.deliverStatuses(t.Context(), callback); err != nil {
		t.Fatal(err)
	}
	post("old-delay", "delayed", "4.0.0", "first@example.invalid")
	batch, err = PendingStatusReceipts(t.Context(), conn.Conn(), "alpha")
	if err != nil || len(batch) != 0 {
		t.Fatal("late delay reversed bounce", batch, err)
	}
	post("first-delivered", "delivered", "2.0.0", "first@example.invalid")
	post("late-failure", "failed", "5.1.1", "first@example.invalid")
	if err = dispatcher.deliverStatuses(t.Context(), callback); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 4 {
		t.Fatal("duplicate/missing callbacks", delivered)
	}
	counts := map[string]int{}
	for _, status := range delivered {
		var receipt struct {
			Recipient string `json:"recipient"`
		}
		if err := json.Unmarshal(status.Receipt, &receipt); err != nil {
			t.Fatal(err)
		}
		counts[receipt.Recipient+":"+status.State]++
	}
	for _, key := range []string{":submitted", "first@example.invalid:bounced", "second@example.invalid:delivered", "first@example.invalid:delivered"} {
		if counts[key] != 1 {
			t.Fatal(counts)
		}
	}
	var publicBytes, privateFiles int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_events WHERE payload::text LIKE '%content_base64%'`).Scan(&publicBytes); err != nil || publicBytes != 0 {
		t.Fatal("attachment bytes leaked into callbacks", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_email_attachments WHERE account='alpha'`).Scan(&privateFiles); err != nil || privateFiles != 7 {
		t.Fatal("private DSN attachments missing", privateFiles, err)
	}
	deliveryState(t, o, id, "submitted")
}
