package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	var cut, oversized atomic.Bool
	server := imapserver.New(&imapserver.Options{NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return &interruptedMailboxSession{Session: memory.NewSession(), conn: conn, cut: &cut, oversized: &oversized}, nil, nil
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
	if err := client.TestMailbox(t.Context()); err != nil {
		t.Fatal("read-only mailbox connection check", err)
	}
	var snapshots []MailboxCursor
	checkpoint := func(cursor MailboxCursor) error {
		snapshots = append(snapshots, cursor)
		return nil
	}
	count := 0
	consume := func(validity, uid uint32, message *Received, reason string) error {
		if message == nil || reason != "" || message.From != "sender@example.invalid" {
			t.Fatal("message parsing failed", reason)
		}
		if len(snapshots) == 0 || snapshots[len(snapshots)-1].Validity != validity || snapshots[len(snapshots)-1].ResetUIDNext == 0 {
			t.Fatal("body consumed before reset checkpoint")
		}
		count++
		return nil
	}
	oversized.Store(true)
	rejected := false
	large, err := client.FetchMailbox(t.Context(), MailboxCursor{}, checkpoint, func(validity, uid uint32, message *Received, reason string) error {
		if message != nil || uid != 1 || reason == "" {
			t.Fatal("oversized literal not rejected")
		}
		rejected = true
		return nil
	})
	if err == nil || !rejected || large.UID != 1 {
		t.Fatal("oversized literal blocked checkpoint", large, err)
	}
	cut.Store(true)
	interrupted, err := client.FetchMailbox(t.Context(), MailboxCursor{}, checkpoint, consume)
	if err == nil || interrupted.UID != 0 || count != 0 {
		t.Fatal("partial literal was consumed", interrupted, count, err)
	}
	cursor, err := client.FetchMailbox(t.Context(), MailboxCursor{}, checkpoint, consume)
	if err != nil || cursor.UID != 20 || cursor.ResetUIDNext != 24 || count != 20 || !cursor.Backlog {
		t.Fatal("first bounded page", cursor, count, err)
	}
	cursor, err = client.FetchMailbox(t.Context(), cursor, checkpoint, consume)
	if err != nil || cursor.UID != 23 || count != 23 || cursor.Backlog {
		t.Fatal("resumed page", cursor, count, err)
	}
	if _, err = client.FetchMailbox(t.Context(), cursor, checkpoint, consume); err != nil || count != 23 {
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
	next, err := client.FetchMailbox(t.Context(), cursor, checkpoint, consume)
	if err != nil || next.Validity == cursor.Validity || next.UID != 1 || next.ResetUIDNext != 2 || count != 24 {
		t.Fatal("UIDVALIDITY reset failed", next, err)
	}
	_, err = client.FetchMailbox(t.Context(), MailboxCursor{}, checkpoint, func(uint32, uint32, *Received, string) error { return errors.New("synthetic database failure") })
	if err == nil {
		t.Fatal("sink failure ignored")
	}

	// A transport failure must leave the checkpoint reusable on reconnect.
	goodDial := client.dialContext
	client.dialContext = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("offline") }
	failed, err := client.FetchMailbox(t.Context(), next, checkpoint, consume)
	if err == nil || failed != next {
		t.Fatal("failed connection changed checkpoint", failed, err)
	}
	client.dialContext = goodDial
	appendMessage(101)
	resumed, err := client.FetchMailbox(t.Context(), next, checkpoint, consume)
	if err != nil || resumed.UID != 2 || count != 25 {
		t.Fatal("reconnect lost message", resumed, count, err)
	}
	// A sink failure must not advance past an uncommitted body.
	failed, err = client.FetchMailbox(t.Context(), MailboxCursor{}, checkpoint, func(uint32, uint32, *Received, string) error { return errors.New("rollback") })
	if err == nil || failed.UID != 0 {
		t.Fatal("sink failure advanced cursor", failed, err)
	}
	// Trusted malformed DSNs must be rejected without blocking following mail.
	client.cfg.TrustedAuthservID = "mx.example.invalid"
	client.cfg.TrustedDSNDomain = "example.invalid"
	var address strings.Builder
	for range 200 {
		address.WriteString(rand.Text())
	}
	trusted := "Authentication-Results: mx.example.invalid; dmarc=pass header.from=example.invalid\r\nReceived: from provider.invalid by mx.example.invalid; today\r\n" + dsnFixture("failed", "5.1.1")
	for _, raw := range []string{strings.Replace(trusted, "First@example.invalid", address.String()+"@example.invalid", 1), trusted} {
		if _, err := user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rejections, reports := 0, 0
	progress, err := client.FetchMailbox(t.Context(), resumed, checkpoint, func(validity, uid uint32, message *Received, reason string) error {
		switch uid {
		case 3:
			if message != nil || reason == "" {
				t.Fatal("oversized DSN not rejected")
			}
			rejections++
		case 4:
			if message == nil || reason != "" || len(message.DeliveryStatuses) != 2 {
				t.Fatal("valid DSN blocked")
			}
			reports++
		default:
			t.Fatal("unexpected UID", uid)
		}
		return nil
	})
	if err != nil || progress.UID != 4 || rejections != 1 || reports != 1 {
		t.Fatal(progress, rejections, reports, err)
	}

	// Empty resets also establish a boundary, before subsequent arrivals.
	if err := user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	empty, err := client.FetchMailbox(t.Context(), progress, checkpoint, consume)
	if err != nil || empty.UID != 0 || empty.ResetUIDNext != 1 {
		t.Fatal(empty, err)
	}
	appendMessage(101)
	arrived, err := client.FetchMailbox(t.Context(), empty, checkpoint, consume)
	if err != nil || arrived.UID != 1 || arrived.ResetUIDNext != 1 || count != 26 {
		t.Fatal(arrived, count, err)
	}
	// A failed reset checkpoint prevents bodies from reaching the consumer.
	_, err = client.FetchMailbox(t.Context(), MailboxCursor{}, func(MailboxCursor) error { return errors.New("checkpoint failure") }, consume)
	if err == nil || count != 26 {
		t.Fatal("failed checkpoint consumed mail", err)
	}
	client.tlsRoots = nil
	if _, err = client.FetchMailbox(t.Context(), MailboxCursor{}, checkpoint, consume); err == nil || count != 26 {
		t.Fatal("untrusted TLS accepted")
	}
}

type interruptedMailboxSession struct {
	imapserver.Session
	conn      *imapserver.Conn
	cut       *atomic.Bool
	oversized *atomic.Bool
}

func (s *interruptedMailboxSession) Fetch(w *imapserver.FetchWriter, nums imap.NumSet, options *imap.FetchOptions) error {
	size := int64(8192)
	if s.oversized.Swap(false) {
		size = MaxMessageBytes + 1
	} else if !s.cut.Swap(false) {
		return s.Session.Fetch(w, nums, options)
	}
	response := w.CreateMessage(1)
	response.WriteUID(1)
	literal := response.WriteBodySection(options.BodySection[0], size)
	_, _ = literal.Write(make([]byte, 4096))
	_ = s.conn.NetConn().Close()
	return io.ErrUnexpectedEOF
}

func TestMailboxWatchBoundedRecheckWithAndWithoutIDLE(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(fmt.Sprint(idle), func(t *testing.T) {
			certificate, roots := smtpCertificate(t, false)
			memory := imapmemserver.New()
			user := imapmemserver.NewUser("fixture", "synthetic-password")
			memory.AddUser(user)
			if err := user.Create("INBOX", nil); err != nil {
				t.Fatal(err)
			}
			caps := imap.CapSet{imap.CapIMAP4rev1: {}}
			if idle {
				caps[imap.CapIdle] = struct{}{}
			}
			server := imapserver.New(&imapserver.Options{Caps: caps, NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
				return memory.NewSession(), nil, nil
			}})
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			if err != nil {
				t.Fatal(err)
			}
			go server.Serve(listener)
			defer server.Close()
			client, err := New(Config{AccountID: "fixture", Host: "smtp.invalid", Port: 587, From: "sender@example.invalid", Enabled: true, IMAPHost: "127.0.0.1", IMAPPort: 993, IMAPUsername: "fixture", IMAPPassword: "synthetic-password"})
			if err != nil {
				t.Fatal(err)
			}
			client.tlsRoots = roots
			client.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
			}
			notifications := 0
			for range 2 {
				started := time.Now()
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				err := client.watchMailbox(ctx, func() { notifications++ }, 30*time.Millisecond)
				cancel()
				if err != nil || time.Since(started) >= time.Second {
					t.Fatal("fallback did not reconnect cleanly", err)
				}
			}
			if notifications < 2 {
				t.Fatal("missing bounded mailbox re-check", notifications)
			}
		})
	}
}
