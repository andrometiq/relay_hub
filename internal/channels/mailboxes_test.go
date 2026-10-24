package channels

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	relayemail "github.com/andrometiq/relay_hub/internal/email"
)

type pagedMailbox struct {
	key       string
	epoch     uint32
	messages  []string
	calls     atomic.Int32
	failAfter int
	block     <-chan struct{}
	parser    *relayemail.Client
}

func (m *pagedMailbox) MailboxKey() string { return m.key }
func (m *pagedMailbox) FetchMailbox(ctx context.Context, cursor relayemail.MailboxCursor, checkpoint func(relayemail.MailboxCursor) error, consume func(uint32, uint32, *relayemail.Received, string) error) (relayemail.MailboxCursor, error) {
	m.calls.Add(1)
	if m.block != nil {
		select {
		case <-m.block:
		case <-ctx.Done():
			return cursor, ctx.Err()
		}
	}
	if cursor.Validity != m.epoch {
		cursor = relayemail.MailboxCursor{Validity: m.epoch, ResetUIDNext: uint32(len(m.messages) + 1)}
		if err := checkpoint(cursor); err != nil {
			return cursor, err
		}
	}
	end := min(int(cursor.UID)+20, len(m.messages))
	count := 0
	for int(cursor.UID) < end {
		parse := relayemail.Parse
		if m.parser != nil {
			parse = m.parser.ParseIngress
		}
		message, err := parse([]byte(m.messages[cursor.UID]))
		if err != nil {
			err = consume(m.epoch, cursor.UID+1, nil, "Message MIME or size exceeds supported limits")
		} else {
			err = consume(m.epoch, cursor.UID+1, &message, "")
		}
		if err != nil {
			return cursor, err
		}
		cursor.UID++
		count++
		if count == m.failAfter {
			return cursor, errors.New("synthetic disconnect")
		}
	}
	cursor.Backlog = int(cursor.UID) < len(m.messages)
	return cursor, nil
}
func mailboxMessages(count int) []string {
	result := make([]string, count)
	for i := range result {
		result[i] = fmt.Sprintf("From: sender@example.invalid\r\nTo: inbox@example.invalid\r\nMessage-ID: <mail-%d@example.invalid>\r\nSubject: Test\r\n\r\nBody %d", i, i)
	}
	return result
}
func TestMailboxDurableReconnectEpochAndIsolation(t *testing.T) {
	pool, _ := channelTestDB(t)
	p := MailboxPoller{DB: pool}
	m := &pagedMailbox{key: rand.Text(), epoch: 1, messages: mailboxMessages(27), failAfter: 7}
	if err := p.Poll(t.Context(), "alpha", m); err == nil {
		t.Fatal("disconnect ignored")
	}
	var uid, failures int
	var ready bool
	if err := pool.QueryRow(t.Context(), `SELECT last_uid,failures,next_attempt<=now() FROM relay_mailbox_cursor WHERE account='alpha'`).Scan(&uid, &failures, &ready); err != nil || uid != 7 || failures != 1 || ready {
		t.Fatal(uid, failures, ready, err)
	}
	if err := p.poll(t.Context(), "alpha", m, true); err != nil || m.calls.Load() != 1 {
		t.Fatal("backoff bypassed", err)
	}
	m.failAfter = 0
	if err := p.Poll(t.Context(), "alpha", m); err != nil {
		t.Fatal(err)
	}
	// Same raw content at a distinct UID is a real second occurrence.
	m.messages = append(m.messages, m.messages[0])
	if err := p.Poll(t.Context(), "alpha", m); err != nil {
		t.Fatal(err)
	}
	// Reorder retained mail after UIDVALIDITY changes, then append a new message
	// reusing a Message-ID with different content; none may disappear.
	m.epoch = 2
	m.messages[0], m.messages[4] = m.messages[4], m.messages[0]
	m.messages = append(m.messages, m.messages[0]+" changed")
	for range 2 {
		if err := p.Poll(t.Context(), "alpha", m); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Poll(t.Context(), "beta", m); err != nil {
		t.Fatal(err)
	}
	if err := p.Poll(t.Context(), "beta", m); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"alpha", "beta"} {
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_events WHERE account=$1`, account).Scan(&count); err != nil || count != 29 {
			t.Fatal(account, count, err)
		}
	}
	if err := pool.QueryRow(t.Context(), `SELECT last_uid,failures FROM relay_mailbox_cursor WHERE account='alpha'`).Scan(&uid, &failures); err != nil || uid != 29 || failures != 0 {
		t.Fatal(uid, failures, err)
	}
}
func TestMailboxSchedulerFairBacklogAndCompetingWorkers(t *testing.T) {
	pool, _ := channelTestDB(t)
	p := MailboxPoller{DB: pool}
	blocked := make(chan struct{})
	slow := &pagedMailbox{key: rand.Text(), epoch: 1, messages: mailboxMessages(1), block: blocked}
	readers := map[string]MailboxReader{"slow": slow}
	for i := range 12 {
		readers[fmt.Sprintf("account-%02d", i)] = &pagedMailbox{key: rand.Text(), epoch: 1, messages: mailboxMessages(43)}
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			p.run(ctx, 5*time.Millisecond, func() map[string]MailboxReader { return readers })
		}()
	}
	defer func() { cancel(); close(blocked); workers.Wait() }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_events WHERE account<>'slow'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 12*43 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slow mailbox blocked backlog: %d", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if slow.calls.Load() != 1 {
		t.Fatal("competing worker entered same mailbox", slow.calls.Load())
	}
	for name, reader := range readers {
		if name != "slow" && reader.(*pagedMailbox).calls.Load() != 3 {
			t.Fatal("incorrect paging", name, reader.(*pagedMailbox).calls.Load())
		}
	}
}

func TestMailboxResetSnapshotDoesNotSuppressNewCopies(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			pool, _ := channelTestDB(t)
			p := MailboxPoller{DB: pool}
			messages := mailboxMessages(25)
			m := &pagedMailbox{key: rand.Text(), epoch: 1, messages: messages}
			for range 2 {
				if err := p.Poll(t.Context(), "alpha", m); err != nil {
					t.Fatal(err)
				}
			}
			m.epoch = 2
			if empty {
				m.messages = nil
			} else {
				m.failAfter = 1
			}
			err := p.Poll(t.Context(), "alpha", m)
			if (err != nil) != !empty {
				t.Fatal("reset result", err)
			}
			var boundary uint32
			if err := pool.QueryRow(t.Context(), `SELECT reset_uid_next FROM relay_mailbox_cursor WHERE account='alpha'`).Scan(&boundary); err != nil || boundary != uint32(len(m.messages)+1) {
				t.Fatal(boundary, err)
			}
			m.failAfter = 0
			// Recreate the poller after a checkpointed reset, then append an exact
			// old copy outside its snapshot while catch-up may still be pending.
			p = MailboxPoller{DB: pool}
			m.messages = append(append([]string{}, m.messages...), messages[0])
			for range 2 {
				if err := p.Poll(t.Context(), "alpha", m); err != nil {
					t.Fatal(err)
				}
			}
			m.messages = append(m.messages, messages[1])
			if err := p.Poll(t.Context(), "alpha", m); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_events`).Scan(&count); err != nil || count != 27 {
				t.Fatal("new identical arrivals suppressed", count, err)
			}
		})
	}
}

func TestMailboxPopulatedUpgradeDoesNotReplayAcknowledgedMail(t *testing.T) {
	for _, seedPage := range []bool{false, true} {
		t.Run(fmt.Sprintf("seed_page=%t", seedPage), func(t *testing.T) {
			key := rand.Text()
			messages := mailboxMessages(26)
			// Include absent IDs, duplicate occurrences, and binary attachments.
			messages[0] = strings.ReplaceAll(messages[0], "Message-ID: <mail-0@example.invalid>\r\n", "")
			messages[1] = messages[0]
			messages[25] = "From: sender@example.invalid\r\nTo: inbox@example.invalid\r\nSubject: Attachment\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=test.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("binary bytes", 100)))
			pool, _ := channelTestDBWithMigration(t, func(ctx context.Context, conn *pgx.Conn) error {
				entries, err := database.Files.ReadDir("migrations")
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if entry.Name() >= "0033" {
						break
					}
					raw, err := database.Files.ReadFile("migrations/" + entry.Name())
					if err != nil {
						return err
					}
					if _, err = conn.Exec(ctx, string(raw)); err != nil {
						return err
					}
				}
				// This is the exact legacy representation: no raw-content hash.
				for i, raw := range messages {
					message, err := relayemail.Parse([]byte(raw))
					if err != nil {
						return err
					}
					tx, err := conn.Begin(ctx)
					if err != nil {
						return err
					}
					defer tx.Rollback(ctx)
					providerID := fmt.Sprintf("imap:%s:1:%d", key, i+1)
					metadata, err := saveEmailAttachments(ctx, tx, "alpha", providerID, message.Attachments)
					if err != nil {
						return err
					}
					payload, err := json.Marshal(map[string]any{"message_id": message.MessageID, "in_reply_to": message.InReplyTo, "references": message.References, "to": message.To, "cc": message.Cc, "html": message.HTML, "auto_submitted": message.AutoSubmitted, "attachments": metadata})
					if err != nil {
						return err
					}
					if _, err = tx.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,sender,recipient,subject,body,payload) VALUES('alpha','Email','inbound',$1,$2,$3,$4,$5,$6)`, providerID, message.From, strings.Join(message.To, ","), message.Subject, message.Body, payload); err != nil {
						return err
					}
					if err = tx.Commit(ctx); err != nil {
						return err
					}
				}
				if _, err = conn.Exec(ctx, `UPDATE relay_channel_callback_delivery SET acknowledged=true`); err != nil {
					return err
				}
				if _, err = conn.Exec(ctx, `INSERT INTO relay_mailbox_cursor(account,mailbox,uid_validity,last_uid) VALUES('alpha',$1,1,26)`, key); err != nil {
					return err
				}
				raw, err := database.Files.ReadFile("migrations/0033_mailbox_scheduling.sql")
				if err != nil {
					return err
				}
				if _, err = conn.Exec(ctx, string(raw)); err != nil {
					return err
				}
				for _, name := range []string{"0034_channel_health.sql", "0035_channel_callback_lanes.sql", "0036_live_invalidations.sql"} {
					raw, err = database.Files.ReadFile("migrations/" + name)
					if err != nil {
						return err
					}
					if _, err = conn.Exec(ctx, string(raw)); err != nil {
						return err
					}
				}
				return nil
			})
			p := MailboxPoller{DB: pool}
			m := &pagedMailbox{key: key, epoch: 1, messages: messages}
			if seedPage {
				if err := p.Poll(t.Context(), "alpha", m); err != nil {
					t.Fatal(err)
				}
			}
			// Reset before all legacy raw hashes can be seeded; reorder UIDs.
			m.epoch = 2
			m.messages = append([]string{}, messages...)
			slices.Reverse(m.messages)
			for range 2 {
				if err := p.Poll(t.Context(), "alpha", m); err != nil {
					t.Fatal(err)
				}
			}
			var count, pending int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_events`).Scan(&count); err != nil || count != len(messages) {
				t.Fatal("upgrade replayed mail", count, err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_callback_delivery WHERE NOT acknowledged`).Scan(&pending); err != nil || pending != 0 {
				t.Fatal("upgrade enqueued callbacks", pending, err)
			}
			// Same Message-ID with changed content must still arrive.
			m.messages = append(m.messages, messages[2]+" changed", strings.Replace(messages[25], base64.StdEncoding.EncodeToString([]byte(strings.Repeat("binary bytes", 100))), base64.StdEncoding.EncodeToString([]byte("new attachment")), 1))
			if err := p.Poll(t.Context(), "alpha", m); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_callback_delivery WHERE NOT acknowledged`).Scan(&pending); err != nil || pending != 2 {
				t.Fatal("fresh mail lost", pending, err)
			}
		})
	}
}

func TestStalledMailboxesLeaveThreeConnectionRequestPoolAvailable(t *testing.T) {
	pool, _ := channelTestDB(t)
	cfg := pool.Config()
	cfg.MaxConns = 3
	requests, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer requests.Close()
	mailboxes, err := NewMailboxPool(t.Context(), requests.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer mailboxes.Close()
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	blocked := make(chan struct{})
	readers := make([]*pagedMailbox, 8)
	for i := range readers {
		readers[i] = &pagedMailbox{key: rand.Text(), epoch: 1, block: blocked}
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			_ = (MailboxPoller{DB: mailboxes}).Poll(ctx, fmt.Sprint(i), readers[i])
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		started := 0
		for _, reader := range readers {
			started += int(reader.calls.Load())
		}
		if started == len(readers) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mailbox workers did not reach stalled I/O")
		}
		time.Sleep(time.Millisecond)
	}
	request, done := context.WithTimeout(t.Context(), time.Second)
	defer done()
	// Hold all three request connections simultaneously while eight IMAP
	// operations are stalled, then exercise a real ingress write.
	for range 3 {
		conn, err := requests.Acquire(request)
		if err != nil {
			t.Fatal("mailbox I/O starved requests", err)
		}
		defer conn.Release()
		if _, err = conn.Exec(request, `SELECT 1`); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(request, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,sender,recipient,body,payload) VALUES('webhook','Email','inbound',$1,'sender','recipient','body','{}')`, rand.Text()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMailboxRejectsOversizedTrustedDSNAndContinues(t *testing.T) {
	pool, _ := channelTestDB(t)
	client, err := relayemail.New(relayemail.Config{AccountID: "alpha", Host: "smtp.invalid", Port: 587, From: "sender@example.invalid", TrustedAuthservID: "mx.example.invalid", TrustedDSNDomain: "provider.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	var recipient strings.Builder
	for range 200 {
		recipient.WriteString(rand.Text())
	}
	raw := fmt.Sprintf("Authentication-Results: mx.example.invalid; dmarc=pass header.from=provider.invalid\r\nReceived: from outside.invalid by mx.example.invalid; today\r\nFrom: postmaster@provider.invalid\r\nTo: sender@example.invalid\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=dsn\r\n\r\n--dsn\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.provider.invalid\r\nOriginal-Message-ID: <sent@example.invalid>\r\n\r\nFinal-Recipient: rfc822; %s@example.invalid\r\nAction: failed\r\nStatus: 5.1.1\r\n\r\n--dsn--\r\n", recipient.String())
	// Authentication succeeds, so rejection must come from DSN validation.
	parsed, err := client.ParseIngress([]byte(raw))
	if err == nil || parsed.Authentication.Status != "pass" {
		t.Fatal("fixture did not reach trusted DSN parser", err)
	}
	valid := strings.Replace(raw, recipient.String(), "valid", 1)
	m := &pagedMailbox{key: rand.Text(), epoch: 1, parser: client, messages: []string{raw, valid, mailboxMessages(1)[0]}}
	p := MailboxPoller{DB: pool}
	for range 2 {
		if err := p.Poll(t.Context(), "alpha", m); err != nil {
			t.Fatal(err)
		}
	}
	var uid, rejected, events, evidence int
	if err := pool.QueryRow(t.Context(), `SELECT last_uid,(SELECT count(*) FROM relay_mailbox_rejections),(SELECT count(*) FROM relay_channel_events),(SELECT count(*) FROM relay_email_delivery_evidence) FROM relay_mailbox_cursor WHERE account='alpha'`).Scan(&uid, &rejected, &events, &evidence); err != nil || uid != 3 || rejected != 1 || events != 2 || evidence != 1 {
		t.Fatal(uid, rejected, events, evidence, err)
	}
}

// A notification received during an active fetch must survive its final cursor
// update, otherwise the newly arrived message waits for the fallback interval.
func TestMailboxWakeDuringFetchSurvivesCheckpoint(t *testing.T) {
	pool, _ := channelTestDB(t)
	p := MailboxPoller{DB: pool}
	release := make(chan struct{})
	reader := &pagedMailbox{key: rand.Text(), epoch: 1, block: release}
	done := make(chan error, 1)
	go func() { done <- p.Poll(t.Context(), "wake", reader) }()
	deadline := time.Now().Add(time.Second)
	for reader.calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("fetch did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE relay_mailbox_cursor SET next_attempt=clock_timestamp() WHERE account='wake'`); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var ready bool
	if err := pool.QueryRow(t.Context(), `SELECT next_attempt<=now() FROM relay_mailbox_cursor WHERE account='wake'`).Scan(&ready); err != nil || !ready {
		t.Fatal("notification was lost", err)
	}
}
