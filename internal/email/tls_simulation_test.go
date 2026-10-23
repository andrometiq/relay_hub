package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/mail"
	"net/textproto"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These simulations perform the actual TLS and SMTP exchanges over loopback.
// Only a generated local CA is trusted; verification is never disabled.
type smtpSimulation struct {
	implicitTLS                                        bool
	rejectRecipient                                    int
	failQuit                                           bool
	stallAt                                            string
	wrongHostname                                      bool
	authCode, rcptCode, dataCommandCode, dataReplyCode int
	dropFinalReply                                     bool
}

type smtpTranscript struct {
	commands       []string
	beforeTLS      []string
	auth           string
	from           string
	recipients     []string
	message        []byte
	accepted       bool
	tlsVersion     uint16
	handshakeError error
	err            error
}

type smtpFixture struct {
	client  *Client
	done    <-chan smtpTranscript
	stalled <-chan struct{}
}

func newSMTPFixture(t *testing.T, options smtpSimulation) smtpFixture {
	t.Helper()
	certificate, roots := smtpCertificate(t, options.wrongHostname)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	transcript := make(chan smtpTranscript, 1)
	stalled := make(chan struct{})
	go func() {
		var result smtpTranscript
		defer func() { transcript <- result }()
		conn, err := listener.Accept()
		if err != nil {
			result.err = err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		secure := false
		reply := func(s string) bool {
			_, err := io.WriteString(conn, s+"\r\n")
			if err != nil {
				result.err = err
			}
			return err == nil
		}
		stall := func(stage string) bool {
			if options.stallAt != stage {
				return false
			}
			close(stalled)
			// Wait for cancellation to close the real connection, not a test timer.
			_, result.err = io.Copy(io.Discard, conn)
			return true
		}
		startTLS := func() bool {
			if stall("TLS") {
				return false
			}
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			if err := tlsConn.Handshake(); err != nil {
				result.handshakeError = err
				return false
			}
			result.tlsVersion = tlsConn.ConnectionState().Version
			conn = tlsConn
			reader = bufio.NewReader(conn)
			secure = true
			return true
		}
		if options.implicitTLS && !startTLS() {
			return
		}
		if !reply("220 local SMTP simulation") {
			return
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if !errors.Is(err, io.EOF) {
					result.err = err
				}
				return
			}
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			result.commands = append(result.commands, line)
			if !secure {
				result.beforeTLS = append(result.beforeTLS, line)
			}
			verb, arg, _ := strings.Cut(line, " ")
			switch verb {
			case "EHLO":
				if secure {
					if !reply("250-localhost\r\n250 AUTH PLAIN") {
						return
					}
				} else if !reply("250-localhost\r\n250 STARTTLS") {
					return
				}
			case "STARTTLS":
				if secure || !reply("220 Begin TLS") {
					return
				}
				if !startTLS() {
					return
				}
			case "AUTH":
				method, encoded, _ := strings.Cut(arg, " ")
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if !secure || method != "PLAIN" || err != nil {
					result.err = fmt.Errorf("invalid or unencrypted authentication")
					return
				}
				result.auth = string(decoded)
				if options.authCode != 0 {
					if !reply(fmt.Sprintf("%d Synthetic auth rejection", options.authCode)) {
						return
					}
					continue
				}
				if stall("AUTH") || !reply("235 Authentication successful") {
					return
				}
			case "MAIL":
				result.from = strings.TrimPrefix(arg, "FROM:")
				if !reply("250 Sender OK") {
					return
				}
			case "RCPT":
				result.recipients = append(result.recipients, strings.TrimPrefix(arg, "TO:"))
				if stall("RCPT") {
					return
				}
				if options.rcptCode != 0 {
					if !reply(fmt.Sprintf("%d Synthetic recipient rejection", options.rcptCode)) {
						return
					}
					continue
				}
				if options.rejectRecipient == len(result.recipients) {
					if !reply("550 Recipient rejected") {
						return
					}
				} else if !reply("250 Recipient OK") {
					return
				}
			case "DATA":
				if options.dataCommandCode != 0 {
					if !reply(fmt.Sprintf("%d Synthetic DATA rejection", options.dataCommandCode)) {
						return
					}
					continue
				}
				if !reply("354 End with a single dot") {
					return
				}
				result.message, result.err = textproto.NewReader(reader).ReadDotBytes()
				if result.err != nil || stall("DATA") {
					return
				}
				if options.dataReplyCode != 0 {
					if !reply(fmt.Sprintf("%d Synthetic DATA rejection", options.dataReplyCode)) {
						return
					}
					continue
				}
				result.accepted = true
				if options.dropFinalReply {
					return
				}
				if !reply("250 Stored") {
					return
				}
			case "QUIT":
				if !options.failQuit {
					reply("221 Bye")
				}
				return
			default:
				result.err = fmt.Errorf("unexpected SMTP command: %s", verb)
				return
			}
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	client, err := New(Config{Enabled: true,
		AccountID: "synthetic-smtp", Host: host, Port: n,
		From: "Relay Simulation <sender@example.invalid>", Username: "synthetic-user", Password: "synthetic-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	client.tlsRoots = roots
	if options.implicitTLS {
		// Exercise Send's actual port-465 branch without binding a privileged port.
		client.cfg.Port = 465
		client.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != "127.0.0.1:465" {
				return nil, fmt.Errorf("unexpected SMTP dial target: %s %s", network, address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		}
	}
	return smtpFixture{client: client, done: transcript, stalled: stalled}
}

func smtpCertificate(t *testing.T, wrongHostname bool) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Relay ephemeral simulation CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Relay local SMTP simulation"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	if wrongHostname {
		leaf.IPAddresses = []net.IP{net.ParseIP("127.0.0.2")}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: key}, roots
}

func smtpResult(t *testing.T, fixture smtpFixture) smtpTranscript {
	t.Helper()
	select {
	case result := <-fixture.done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP simulation did not finish")
		return smtpTranscript{}
	}
}

func TestSMTPSTARTTLSDeliveryAndEnvelopePrivacy(t *testing.T) {
	testSMTPDeliveryAndEnvelopePrivacy(t, false)
}

func TestSMTPImplicitTLSDeliveryAndEnvelopePrivacy(t *testing.T) {
	testSMTPDeliveryAndEnvelopePrivacy(t, true)
}

func testSMTPDeliveryAndEnvelopePrivacy(t *testing.T, implicitTLS bool) {
	t.Helper()
	fixture := newSMTPFixture(t, smtpSimulation{implicitTLS: implicitTLS})
	body := "First line\r\n.line beginning with dot\r\nLast line\r\n"
	err := fixture.client.Send(t.Context(), []string{"Visible Recipient <to@example.invalid>"}, "Synthetic delivery", body,
		[]string{"Copy Recipient <cc@example.invalid>"}, []string{"Hidden Recipient <bcc@example.invalid>"})
	if err != nil {
		t.Fatalf("TLS SMTP delivery failed: %v", err)
	}
	result := smtpResult(t, fixture)
	if result.err != nil || !result.accepted || result.tlsVersion < tls.VersionTLS12 {
		t.Fatalf("delivery transcript: %+v", result)
	}
	if result.auth != "\x00synthetic-user\x00synthetic-password" {
		t.Fatalf("unexpected AUTH PLAIN payload: %q", result.auth)
	}
	if implicitTLS && len(result.beforeTLS) != 0 {
		t.Fatalf("SMTP commands sent before implicit TLS: %v", result.beforeTLS)
	}
	if !implicitTLS && (len(result.beforeTLS) != 2 || !strings.HasPrefix(result.beforeTLS[0], "EHLO ") || result.beforeTLS[1] != "STARTTLS") {
		t.Fatalf("credentials or content sent before TLS: %v", result.beforeTLS)
	}
	if result.from != "<sender@example.invalid>" || !reflect.DeepEqual(result.recipients,
		[]string{"<to@example.invalid>", "<cc@example.invalid>", "<bcc@example.invalid>"}) {
		t.Fatalf("incorrect SMTP envelope: sender=%s recipients=%v", result.from, result.recipients)
	}
	message, err := mail.ReadMessage(strings.NewReader(string(result.message)))
	if err != nil {
		t.Fatal(err)
	}
	if message.Header.Get("To") != "Visible Recipient <to@example.invalid>" ||
		message.Header.Get("Cc") != "Copy Recipient <cc@example.invalid>" ||
		message.Header.Get("Subject") != "Synthetic delivery" ||
		message.Header.Get("Bcc") != "" || strings.Contains(string(result.message), "bcc@example.invalid") {
		t.Fatalf("invalid visible headers or leaked BCC: %s", result.message)
	}
	gotBody, err := io.ReadAll(message.Body)
	if err != nil || string(gotBody) != strings.ReplaceAll(body, "\r\n", "\n") {
		t.Fatalf("body/dot-stuffing mismatch: %q, %v", gotBody, err)
	}
}

func TestSMTPRejectedRecipientPreventsData(t *testing.T) {
	fixture := newSMTPFixture(t, smtpSimulation{rejectRecipient: 2})
	err := fixture.client.Send(t.Context(), []string{"one@example.invalid", "two@example.invalid", "three@example.invalid"},
		"Synthetic rejection", "No partial send is permitted", nil, nil)
	var smtpErr *textproto.Error
	if !errors.As(err, &smtpErr) || smtpErr.Code != 550 {
		t.Fatalf("expected SMTP recipient rejection, got %v", err)
	}
	result := smtpResult(t, fixture)
	if result.err != nil || len(result.recipients) != 2 || result.accepted || len(result.message) != 0 {
		t.Fatalf("rejected recipient did not stop delivery: %+v", result)
	}
	for _, command := range result.commands {
		if command == "DATA" {
			t.Fatal("DATA sent after recipient rejection")
		}
	}
}

func TestSMTPAcceptedDataSurvivesFailedQuit(t *testing.T) {
	fixture := newSMTPFixture(t, smtpSimulation{failQuit: true})
	if err := fixture.client.Send(t.Context(), []string{"recipient@example.invalid"}, "Accepted", "synthetic body", nil, nil); err != nil {
		t.Fatalf("successful DATA reported failure due to QUIT: %v", err)
	}
	result := smtpResult(t, fixture)
	if !result.accepted || result.commands[len(result.commands)-1] != "QUIT" {
		t.Fatalf("failed QUIT scenario not exercised: %+v", result)
	}
}

func TestSMTPRejectsInvalidCertificates(t *testing.T) {
	for _, mode := range []string{"STARTTLS", "implicitTLS"} {
		for _, name := range []string{"untrusted authority", "wrong hostname"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				fixture := newSMTPFixture(t, smtpSimulation{implicitTLS: mode == "implicitTLS", wrongHostname: name == "wrong hostname"})
				if name == "untrusted authority" {
					fixture.client.tlsRoots = x509.NewCertPool()
				}
				err := fixture.client.Send(t.Context(), []string{"recipient@example.invalid"}, "Rejected", "synthetic body", nil, nil)
				var certErr *tls.CertificateVerificationError
				if !errors.As(err, &certErr) {
					t.Fatalf("certificate not rejected by verification: %v", err)
				}
				result := smtpResult(t, fixture)
				if result.handshakeError == nil || result.auth != "" || result.from != "" || result.accepted {
					t.Fatalf("SMTP activity continued past invalid certificate: %+v", result)
				}
			})
		}
	}
}

func TestSMTPCancellationDuringTLSAndDelivery(t *testing.T) {
	for _, mode := range []string{"STARTTLS", "implicitTLS"} {
		for _, stage := range []string{"TLS", "AUTH", "RCPT", "DATA"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				fixture := newSMTPFixture(t, smtpSimulation{implicitTLS: mode == "implicitTLS", stallAt: stage})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				finished := make(chan error, 1)
				go func() {
					finished <- fixture.client.Send(ctx, []string{"recipient@example.invalid"}, "Cancelled", "synthetic body", nil, nil)
				}()
				select {
				case <-fixture.stalled:
				case err := <-finished:
					t.Fatalf("send stopped before %s: %v", stage, err)
				case <-time.After(2 * time.Second):
					t.Fatalf("send never reached %s", stage)
				}
				cancel()
				select {
				case err := <-finished:
					var outcome *SendError
					if !errors.As(err, &outcome) || outcome.OutcomeUnknown != (stage == "DATA") || outcome.Retryable == (stage == "DATA") {
						t.Fatalf("wrong cancellation outcome at %s: %v", stage, err)
					}
				case <-time.After(time.Second):
					t.Fatalf("cancellation did not interrupt %s", stage)
				}
				result := smtpResult(t, fixture)
				if result.accepted {
					t.Fatal("cancelled send was unexpectedly accepted")
				}
			})
		}
	}
}

func TestSMTPStageOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, stage              string
		options                  smtpSimulation
		code                     int
		retry, unknown, accepted bool
	}{
		{name: "auth permanent", stage: "auth", options: smtpSimulation{authCode: 535}, code: 535},
		{name: "auth transient", stage: "auth", options: smtpSimulation{authCode: 454}, code: 454, retry: true},
		{name: "recipient permanent", stage: "rcpt", options: smtpSimulation{rcptCode: 550}, code: 550},
		{name: "recipient transient", stage: "rcpt", options: smtpSimulation{rcptCode: 450}, code: 450, retry: true},
		{name: "before DATA transient", stage: "data_command", options: smtpSimulation{dataCommandCode: 451}, code: 451, retry: true},
		{name: "after DATA transient", stage: "data_reply", options: smtpSimulation{dataReplyCode: 451}, code: 451},
		{name: "after DATA permanent", stage: "data_reply", options: smtpSimulation{dataReplyCode: 554}, code: 554},
		{name: "lost DATA acceptance", stage: "data_reply", options: smtpSimulation{dropFinalReply: true}, unknown: true, accepted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSMTPFixture(t, tc.options)
			err := fixture.client.Send(t.Context(), []string{"recipient@example.invalid"}, "Stage test", "Representative message body", nil, nil)
			var outcome *SendError
			if !errors.As(err, &outcome) || outcome.Stage != tc.stage || outcome.Code != tc.code || outcome.Retryable != tc.retry || outcome.OutcomeUnknown != tc.unknown {
				t.Fatalf("incorrect outcome: %+v", err)
			}
			result := smtpResult(t, fixture)
			if result.accepted != tc.accepted {
				t.Fatalf("provider acceptance mismatch %+v", result)
			}
		})
	}
	client, _ := New(Config{Enabled: true, AccountID: "local", Host: "127.0.0.1", Port: 465, From: "local@example.invalid"})
	client.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("synthetic connection refusal containing private provider details")
	}
	err := client.Send(t.Context(), []string{"recipient@example.invalid"}, "Connection failure", "Representative body", nil, nil)
	var outcome *SendError
	if !errors.As(err, &outcome) || outcome.Stage != "connect" || !outcome.Retryable || outcome.OutcomeUnknown || strings.Contains(err.Error(), "private") {
		t.Fatal("incorrect connect outcome", err)
	}
}
