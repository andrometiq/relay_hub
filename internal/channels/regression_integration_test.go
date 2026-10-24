package channels

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/lifecycle"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

func TestUpgradeReplayYieldsAndReceiptsStayOffMessageLane(t *testing.T) {
	pool, dsn := channelTestDB(t)
	for i := range 60 {
		saveChannelEvent(t, pool, "a", "Email", fmt.Sprint(i), "history")
	}
	if _, err := pool.Exec(t.Context(), "UPDATE relay_channel_callback_delivery SET replay=true"); err != nil {
		t.Fatal(err)
	}
	fresh := saveChannelEvent(t, pool, "a", "Email", "fresh", "customer question")
	statuses := []relayemail.DeliveryStatus{{MessageID: "<sent@example.invalid>", Recipient: "r@example.invalid", State: "delivered", Code: "2.0.0"}}
	store := Store{DB: pool}
	if err := store.SaveEmail(t.Context(), Event{Account: "a", ProviderID: "dsn", OccurredAt: time.Now(), Payload: json.RawMessage(`{}`)}, statuses, nil); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = saveMailboxMessage(t.Context(), tx, "a", "mailbox-dsn", 1, 1, false, &relayemail.Received{MessageID: "<dsn@example.invalid>", From: "sender@example.invalid", DeliveryStatuses: statuses}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var receiptCount int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_channel_events e JOIN relay_channel_callback_delivery d ON d.event_id=e.id WHERE e.receipt AND d.acknowledged`).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatal("receipt lane", receiptCount, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []struct {
				ID int64 `json:"id"`
			}
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Events) != 50 || body.Events[0].ID != fresh {
			t.Error("fresh message behind replay", body)
		}
		fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(body.Events))
	}))
	defer server.Close()
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	d := CallbackDispatcher{URL: dsn}
	if _, err = d.Deliver(t.Context(), conn.Conn(), RuntimeCallback{Account: "a", Site: server.URL}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyEmailRecipientsAndLostAcceptanceCorrelate(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			pool, _ := channelTestDB(t)
			certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer certServer.Close()
			roots := x509.NewCertPool()
			roots.AddCert(certServer.Certificate())
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certServer.TLS.Certificates, MinVersion: tls.VersionTLS12})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			defer func() { <-done }()
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				fmt.Fprint(conn, "220 local SMTP\r\n")
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250 local\r\n")
					case strings.HasPrefix(line, "DATA"):
						fmt.Fprint(conn, "354 data\r\n")
						for {
							line, err = reader.ReadString('\n')
							if err != nil {
								return
							}
							if line == ".\r\n" {
								break
							}
						}
						if lost {
							return
						}
						fmt.Fprint(conn, "250 accepted\r\n")
					case strings.HasPrefix(line, "QUIT"):
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "250 OK\r\n")
					}
				}
			}()
			client, err := relayemail.NewWithTransport(relayemail.Config{AccountID: "a", Host: "example.com", Port: 465, From: "sender@example.invalid", Enabled: true}, roots, func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
			})
			if err != nil {
				t.Fatal(err)
			}
			outbox := Outbox{DB: pool, Registry: &Registry{Email: map[string]*relayemail.Client{"a": client}}}
			id := seedDelivery(t, outbox, "a", "legacy", "email.send", EmailInput{To: []string{"recipient@example.invalid"}, Subject: "test", Body: "meaningful local email"})
			if _, err = pool.Exec(t.Context(), "UPDATE relay_channel_outbox SET destinations=ARRAY['*'] WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			claim := nextClaim(t, outbox)
			if claim == nil {
				t.Fatal("no claim")
			}
			_, sendErr := outbox.send(t.Context(), *claim)
			if (sendErr != nil) != lost {
				t.Fatal("SMTP outcome", sendErr)
			}
			state := "submitted"
			if lost {
				state = "uncertain"
			}
			deliveryState(t, outbox, id, state)
			messageID := client.MessageID(fmt.Sprintf("delivery-%d", id))
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = saveEmailStatuses(t.Context(), tx, "a", []relayemail.DeliveryStatus{{MessageID: messageID, Recipient: "recipient@example.invalid", State: "delivered", Code: "2.0.0"}}); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			conn, err := pool.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			if err = projectEmailStatuses(t.Context(), conn.Conn(), "a"); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM relay_channel_status_journal WHERE delivery_id=$1 AND state='delivered'", id).Scan(&count); err != nil || count != 1 {
				t.Fatal("DSN not correlated", count, err)
			}
			rows, err := outbox.List(t.Context(), "a", 0, "Email")
			if err != nil || len(rows) != 1 || rows[0].Result["message_id"] != messageID {
				t.Fatal("email receipt missing", rows, err)
			}
			rows, err = outbox.List(t.Context(), "a", 0, "Telegram")
			if err != nil || len(rows) != 0 {
				t.Fatal("channel filter", rows, err)
			}
		})
	}
}

func TestTelegramPreSendFailureQueuesWithBoundedRetries(t *testing.T) {
	for _, attempt := range []int{0, 7} {
		state, _, _, _ := channelSendOutcome(&telegram.APIError{Retryable: true}, attempt, false)
		want := "queued"
		if attempt == 7 {
			want = "rejected"
		}
		if state != want {
			t.Fatal(state)
		}
	}
	state, _, _, _ := channelSendOutcome(&telegram.APIError{OutcomeUnknown: true}, 0, false)
	if state != "uncertain" {
		t.Fatal(state)
	}
}

func TestShutdownWaitsForChannelOutcomeJournal(t *testing.T) {
	pool, _ := channelTestDB(t)
	started, release := make(chan struct{}), make(chan struct{})
	client := localTelegram(t, "a", func(w http.ResponseWriter, r *http.Request) { close(started); <-release })
	defer close(release)
	outbox := Outbox{DB: pool, Registry: &Registry{Telegram: map[string]*telegram.Client{"a": client}}}
	id := seedDelivery(t, outbox, "a", "shutdown", "telegram.message", telegram.MessageInput{Operation: "text", ChatID: 123, Text: "in-flight local message"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, workers := lifecycle.WithWorkers(ctx)
	outbox.Run(ctx)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not send")
	}
	cancel()
	drain, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := workers.Wait(drain); err != nil {
		t.Fatal(err)
	}
	deliveryState(t, outbox, id, "uncertain")
}
