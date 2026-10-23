package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"net"
	"testing"
)

func TestMailboxTLSReadOnlyPagingResumeAndEpoch(t *testing.T) {
	certificate, roots := smtpCertificate(t, false)
	memory := imapmemserver.New()
	user := imapmemserver.NewUser("fixture", "synthetic-password")
	memory.AddUser(user)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	appendMessage := func(id int) {
		raw := []byte(fmt.Sprintf("From: sender@example.invalid\r\nTo: inbox@example.invalid\r\nSubject: Synthetic %d\r\nMessage-ID: <fixture-%d@example.invalid>\r\n\r\nBody", id, id))
		if _, err := user.Append("INBOX", bytes.NewReader(raw), &imap.AppendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 23 {
		appendMessage(i)
	}
	server := imapserver.New(&imapserver.Options{NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return memory.NewSession(), nil, nil
	}})
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client, err := New(Config{AccountID: "fixture", Host: "smtp.example.invalid", Port: 587, From: "sender@example.invalid", Enabled: true, IMAPHost: "127.0.0.1", IMAPPort: 993, IMAPUsername: "fixture", IMAPPassword: "synthetic-password"})
	if err != nil {
		t.Fatal(err)
	}
	client.tlsRoots = roots
	client.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	}
	count := 0
	consume := func(validity, uid uint32, message *Received, reason string) error {
		if message == nil || reason != "" || message.From != "sender@example.invalid" {
			t.Fatal("message parsing failed", reason)
		}
		count++
		return nil
	}
	cursor, err := client.FetchMailbox(t.Context(), MailboxCursor{}, consume)
	if err != nil || cursor.UID != 20 || count != 20 {
		t.Fatal("first bounded page", cursor, count, err)
	}
	cursor, err = client.FetchMailbox(t.Context(), cursor, consume)
	if err != nil || cursor.UID != 23 || count != 23 {
		t.Fatal("resumed page", cursor, count, err)
	}
	if _, err = client.FetchMailbox(t.Context(), cursor, consume); err != nil || count != 23 {
		t.Fatal("replayed mailbox", err)
	}
	status, err := user.Status("INBOX", &imap.StatusOptions{NumUnseen: true})
	if err != nil || *status.NumUnseen != 23 {
		t.Fatal("fetch marked messages read", status, err)
	}
	if err = user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	if err = user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	appendMessage(100)
	next, err := client.FetchMailbox(t.Context(), cursor, consume)
	if err != nil || next.Validity == cursor.Validity || next.UID != 1 || count != 24 {
		t.Fatal("UIDVALIDITY reset failed", next, err)
	}
	_, err = client.FetchMailbox(t.Context(), MailboxCursor{}, func(uint32, uint32, *Received, string) error { return errors.New("synthetic database failure") })
	if err == nil {
		t.Fatal("sink failure ignored")
	}
	client.tlsRoots = nil
	if _, err = client.FetchMailbox(t.Context(), MailboxCursor{}, consume); err == nil || count != 24 {
		t.Fatal("untrusted TLS accepted")
	}
}
