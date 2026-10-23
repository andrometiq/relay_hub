package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"

	relayemail "github.com/andrometiq/relay_hub/internal/email"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	return address
}
func emailFixture(t *testing.T, path string) (*fakeEmail, *relayemail.Client) {
	t.Helper()
	fake, err := newFakeEmail(path, newRecorder())
	if err != nil {
		t.Fatal(err)
	}
	if err = fake.start(freeAddress(t), freeAddress(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.close)
	client, err := fake.client("sim-v1")
	if err != nil {
		t.Fatal(err)
	}
	return fake, client
}
func testMail() relayemail.Message {
	return relayemail.Message{To: []string{"to@example.invalid"}, Cc: []string{"cc@example.invalid"}, Bcc: []string{"hidden@example.invalid"}, Subject: "Local simulation", Body: "Invoice total 125.50\n.Second line", HTML: "<p>Invoice total 125.50</p>", RequestKey: "local-mail", InReplyTo: "<original@example.invalid>", References: []string{"<original@example.invalid>"}, Attachments: []relayemail.Attachment{{Filename: "invoice.txt", ContentType: "text/plain", ContentBase64: base64.StdEncoding.EncodeToString([]byte("Total 125.50"))}}}
}
func TestEmailTLSMailboxRoundTripAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "email.json")
	fake, client := emailFixture(t, path)
	if err := client.SendMessage(context.Background(), testMail()); err != nil {
		t.Fatal(err)
	}
	sent := fake.state.snapshot().Sent
	if len(sent) != 1 || len(sent[0].Recipients) != 3 {
		t.Fatal("envelope missing recipients")
	}
	parsed, err := mail.ReadMessage(strings.NewReader(sent[0].Raw))
	if err != nil || parsed.Header.Get("Bcc") != "" {
		t.Fatal("BCC leaked into MIME", err)
	}
	raw, err := authenticatedMail(sent[0].Raw, simulationAuthserv, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fake.appendMail(raw, 7); err != nil {
		t.Fatal(err)
	}
	cursor, err := client.FetchMailbox(context.Background(), relayemail.MailboxCursor{}, func(relayemail.MailboxCursor) error { return nil }, func(validity, uid uint32, received *relayemail.Received, reason string) error {
		if uid != 7 || validity != 1 || reason != "" || received == nil {
			t.Fatalf("bad mailbox record %d %s", uid, reason)
		}
		if received.Authentication.Status != "pass" || received.InReplyTo != "<original@example.invalid>" || len(received.Attachments) != 1 || received.Attachments[0].ContentBase64 != testMail().Attachments[0].ContentBase64 || !strings.Contains(received.Body, "125.50") {
			t.Fatalf("MIME or authentication lost: %+v", received)
		}
		return nil
	})
	if err != nil || cursor.UID != 7 {
		t.Fatalf("fetch: %+v %v", cursor, err)
	}
	fake.close()
	restored, next := emailFixture(t, path)
	if len(restored.state.snapshot().Sent) != 1 {
		t.Fatal("SMTP acceptance lost")
	}
	calls := 0
	if _, err = next.FetchMailbox(context.Background(), cursor, func(relayemail.MailboxCursor) error { return nil }, func(uint32, uint32, *relayemail.Received, string) error { calls++; return nil }); err != nil || calls != 0 {
		t.Fatal("restart replayed mailbox", err)
	}
	bounce, err := bounceMail(client.MessageID("local-mail"), "to@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	received, err := next.ParseIngress([]byte(bounce))
	if err != nil || len(received.DeliveryStatuses) != 1 || received.DeliveryStatuses[0].State != "bounced" {
		t.Fatal("DSN not authenticated", err, received)
	}
	if _, err = restored.appendMail(bounce, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = next.FetchMailbox(context.Background(), cursor, func(relayemail.MailboxCursor) error { return nil }, func(_, uid uint32, r *relayemail.Received, reason string) error {
		if uid != 8 || r == nil || len(r.DeliveryStatuses) != 1 {
			t.Fatal("DSN not fetched", reason)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	captures, _ := json.Marshal(fake.records.snapshot())
	if strings.Contains(string(captures), simulationMailPassword) || strings.Contains(string(captures), base64.StdEncoding.EncodeToString([]byte("\x00"+simulationMailUser+"\x00"+simulationMailPassword))) {
		t.Fatal("SMTP authentication leaked")
	}
}
func TestSMTPStageFaults(t *testing.T) {
	fake, client := emailFixture(t, filepath.Join(t.TempDir(), "email.json"))
	for _, tc := range []struct {
		stage, fault, wantStage  string
		retry, unknown, accepted bool
	}{
		{"connect", "refuse", "connect", true, false, false},
		{"connect", "disconnect", "greeting", true, false, false}, {"auth", "451", "auth", true, false, false}, {"auth", "550", "auth", false, false, false}, {"rcpt", "451", "rcpt", true, false, false}, {"rcpt", "550", "rcpt", false, false, false}, {"data_command", "451", "data_command", true, false, false}, {"data_reply", "550", "data_reply", false, false, false}, {"data_reply", "disconnect_after", "data_reply", false, true, true}, {"data_reply", "disconnect", "data_reply", false, true, false}, {"auth", "timeout", "auth", true, false, false},
	} {
		t.Run(tc.stage+tc.fault, func(t *testing.T) {
			before := len(fake.state.snapshot().Sent)
			if err := fake.faults(tc.stage, []string{tc.fault}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := client.SendMessage(ctx, testMail())
			var outcome *relayemail.SendError
			if !errors.As(err, &outcome) || outcome.Stage != tc.wantStage || outcome.Retryable != tc.retry || outcome.OutcomeUnknown != tc.unknown {
				t.Fatalf("unexpected outcome: %+v", err)
			}
			want := before
			if tc.accepted {
				want++
			}
			if len(fake.state.snapshot().Sent) != want {
				t.Fatal("wrong acceptance state")
			}
		})
	}
}
func TestEmailTrustAndDialBoundaries(t *testing.T) {
	fake, client := emailFixture(t, filepath.Join(t.TempDir(), "email.json"))
	for _, address := range []string{"smtp.example.com:587", "127.0.0.1:25", "192.0.2.1:993", "localhost:993"} {
		if _, err := fake.dial(context.Background(), "tcp", address); err == nil {
			t.Fatal("unexpected dial allowed")
		}
	}
	cfg := relayemail.Config{AccountID: "test", Enabled: true, Host: "127.0.0.1", Port: 587, From: "inbox@sim.invalid", Username: simulationMailUser, Password: simulationMailPassword}
	untrusted, err := relayemail.NewWithTransport(cfg, x509.NewCertPool(), fake.dial)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Host = "wrong.invalid"
	wrong, err := relayemail.NewWithTransport(cfg, client.TLSConfig().RootCAs, func(ctx context.Context, network, address string) (net.Conn, error) {
		return fake.dial(ctx, network, "127.0.0.1:587")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = wrong.SendMessage(context.Background(), testMail()); err == nil {
		t.Fatal("hostname verification disabled")
	}
	if err = untrusted.SendMessage(context.Background(), testMail()); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	for _, server := range []string{simulationAuthserv, "untrusted.invalid"} {
		raw, err := authenticatedMail("From: sender@example.invalid\r\nTo: inbox@sim.invalid\r\nSubject: Incoming\r\nMessage-ID: <in@example.invalid>\r\n\r\nTest 42", server, "pass")
		if err != nil {
			t.Fatal(err)
		}
		received, err := client.ParseIngress([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		want := "unverified"
		if server == simulationAuthserv {
			want = "pass"
		}
		if received.Authentication.Status != want {
			t.Fatal("wrong authentication boundary")
		}
	}
}
func TestMailboxBacklogMalformedAndUIDGaps(t *testing.T) {
	fake, client := emailFixture(t, filepath.Join(t.TempDir(), "email.json"))
	for i := 1; i <= 23; i++ {
		raw := fmt.Sprintf("From: sender@example.invalid\r\nTo: inbox@sim.invalid\r\nSubject: Incoming\r\nMessage-ID: <in-%d@example.invalid>\r\n\r\nAmount %d", i, i)
		if i == 3 {
			raw = "malformed"
		}
		if _, err := fake.appendMail(raw, uint32(i*2)); err != nil {
			t.Fatal(err)
		}
	}
	good, bad := 0, 0
	consume := func(_, _ uint32, r *relayemail.Received, reason string) error {
		if reason != "" {
			bad++
		} else {
			good++
		}
		return nil
	}
	cursor, err := client.FetchMailbox(context.Background(), relayemail.MailboxCursor{}, func(relayemail.MailboxCursor) error { return nil }, consume)
	if err != nil || !cursor.Backlog || good != 19 || bad != 1 {
		t.Fatalf("first page %+v %d %d %v", cursor, good, bad, err)
	}
	cursor, err = client.FetchMailbox(context.Background(), cursor, func(relayemail.MailboxCursor) error { return nil }, consume)
	if err != nil || cursor.Backlog || good != 22 || bad != 1 {
		t.Fatalf("backlog %+v %d %d %v", cursor, good, bad, err)
	}
}

func TestMailboxOversizedRecord(t *testing.T) {
	fake, client := emailFixture(t, filepath.Join(t.TempDir(), "email.json"))
	raw := "From: sender@example.invalid\r\nTo: inbox@sim.invalid\r\nSubject: Oversized\r\n\r\n" + strings.Repeat("x", relayemail.MaxMessageBytes)
	if _, err := fake.appendMail(raw, 1); err != nil {
		t.Fatal(err)
	}
	rejected := false
	cursor, err := client.FetchMailbox(context.Background(), relayemail.MailboxCursor{}, func(relayemail.MailboxCursor) error { return nil }, func(_, uid uint32, r *relayemail.Received, reason string) error {
		rejected = uid == 1 && r == nil && reason != ""
		return nil
	})
	if err == nil || !rejected || cursor.UID != 1 {
		t.Fatal("oversized body did not produce a bounded rejection")
	}
}
