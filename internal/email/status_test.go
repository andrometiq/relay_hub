package email

import (
	"fmt"
	"strings"
	"testing"
)

func dsnFixture(action, code string) string {
	return fmt.Sprintf("From: postmaster@example.invalid\r\nTo: sender@example.invalid\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=dsn\r\n\r\n--dsn\r\nContent-Type: text/plain\r\n\r\nReport\r\n--dsn\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.example.invalid\r\n\r\nFinal-Recipient: rfc822; First@example.invalid\r\nAction: %s\r\nStatus: %s\r\n\r\nFinal-Recipient: rfc822; second@example.invalid\r\nAction: delivered\r\nStatus: 2.0.0\r\n\r\n--dsn\r\nContent-Type: text/rfc822-headers\r\n\r\nMessage-ID: <original@example.invalid>\r\n\r\n--dsn--\r\n", action, code)
}
func TestDeliveryStatusRecipients(t *testing.T) {
	for _, tc := range []struct{ action, code, state string }{{"failed", "5.1.1", "bounced"}, {"delayed", "4.2.0", "delayed"}, {"delivered", "2.0.0", "delivered"}} {
		statuses, err := ParseDeliveryStatuses([]byte(dsnFixture(tc.action, tc.code)))
		if err != nil || len(statuses) != 2 {
			t.Fatal(statuses, err)
		}
		if statuses[0].State != tc.state || statuses[0].Recipient != "first@example.invalid" || statuses[1].Recipient != "second@example.invalid" || statuses[1].State != "delivered" || statuses[0].MessageID != "<original@example.invalid>" {
			t.Fatal(statuses)
		}
	}
	for _, raw := range []string{
		dsnFixture("failed", "2.0.0"),
		strings.ReplaceAll(dsnFixture("failed", "5.1.1"), "<original@example.invalid>", "bad"),
		strings.Replace(dsnFixture("failed", "5.1.1"), "Action: failed", "Action: failed\r\nAction: delivered", 1),
	} {
		if _, err := ParseDeliveryStatuses([]byte(raw)); err == nil {
			t.Fatal("invalid DSN accepted")
		}
	}
	statuses, err := ParseDeliveryStatuses([]byte(dsnFixture("relayed", "2.0.0")))
	if err != nil || len(statuses) != 1 {
		t.Fatal("relay is not delivery", statuses, err)
	}
}

func TestDeliveryStatusRecipientByteLimit(t *testing.T) {
	for _, tc := range []struct {
		name, recipient string
		valid           bool
	}{
		{"boundary", strings.Repeat("a", MaxRecipientBytes-len("@example.invalid")) + "@example.invalid", true},
		{"too long", strings.Repeat("a", MaxRecipientBytes-len("@example.invalid")+1) + "@example.invalid", false},
		{"unicode bytes", strings.Repeat("é", 150) + "@example.invalid", false},
		{"lowercase expands bytes", strings.Repeat("Ⱥ", 100) + "@example.invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(dsnFixture("failed", "5.1.1"), "First@example.invalid", tc.recipient, 1)
			statuses, err := ParseDeliveryStatuses([]byte(raw))
			if (err == nil) != tc.valid {
				t.Fatal("recipient bound", err)
			}
			if tc.valid && statuses[0].Recipient != tc.recipient {
				t.Fatal(statuses)
			}
		})
	}
}
