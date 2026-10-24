package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	relayemail "github.com/andrometiq/relay_hub/internal/email"
)

const simulationMailUser = "sim-mail"
const simulationMailPassword = "sim-mail-local-only"
const simulationAuthserv = "mx.sim.invalid"

type smtpMessage struct {
	From       string   `json:"from"`
	Recipients []string `json:"recipients"`
	Raw        string   `json:"raw"`
}
type mailboxMessage struct {
	UID uint32 `json:"uid"`
	Raw string `json:"raw"`
}
type emailState struct {
	Sent     []smtpMessage       `json:"sent"`
	Inbox    []mailboxMessage    `json:"inbox"`
	Validity uint32              `json:"uid_validity"`
	Next     uint32              `json:"uid_next"`
	Faults   map[string][]string `json:"faults"`
}
type fakeEmail struct {
	state       *providerState[emailState]
	records     *recorder
	certificate tls.Certificate
	roots       *x509.CertPool
	smtp, imap  net.Listener
	mu          sync.Mutex
	connections map[net.Conn]bool
	closed      bool
	idlers      map[chan struct{}]bool
	workers     sync.WaitGroup
}

func newFakeEmail(path string, records *recorder) (*fakeEmail, error) {
	state, err := openProviderState(path, emailState{Validity: 1, Next: 1, Faults: map[string][]string{}})
	if err != nil {
		return nil, err
	}
	if state.value.Faults == nil || state.value.Validity == 0 || state.value.Next == 0 {
		return nil, errors.New("invalid email provider state")
	}
	certificate, roots, err := mailCertificate()
	if err != nil {
		return nil, err
	}
	return &fakeEmail{state: state, records: records, certificate: certificate, roots: roots, connections: map[net.Conn]bool{}, idlers: map[chan struct{}]bool{}}, nil
}
func mailCertificate() (tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Relay local simulation CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Relay local mail"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}, roots, err
}
func (e *fakeEmail) start(smtpAddr, imapAddr string) error {
	for _, addr := range []string{smtpAddr, imapAddr} {
		if _, err := localURL("http://" + addr); err != nil {
			return err
		}
	}
	var err error
	e.smtp, err = net.Listen("tcp", smtpAddr)
	if err != nil {
		return err
	}
	e.imap, err = net.Listen("tcp", imapAddr)
	if err != nil {
		e.smtp.Close()
		return err
	}
	for _, server := range []struct {
		listener net.Listener
		serve    func(net.Conn)
	}{{e.smtp, e.serveSMTP}, {e.imap, e.serveIMAP}} {
		e.workers.Add(1)
		go func() {
			defer e.workers.Done()
			for {
				conn, err := server.listener.Accept()
				if err != nil {
					return
				}
				e.mu.Lock()
				if e.closed {
					e.mu.Unlock()
					conn.Close()
					return
				}
				e.connections[conn] = true
				e.workers.Add(1)
				e.mu.Unlock()
				go func() {
					defer e.workers.Done()
					defer conn.Close()
					defer func() { e.mu.Lock(); delete(e.connections, conn); e.mu.Unlock() }()
					_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
					server.serve(conn)
				}()
			}
		}()
	}
	return nil
}
func (e *fakeEmail) close() {
	e.mu.Lock()
	e.closed = true
	if e.smtp != nil {
		e.smtp.Close()
	}
	if e.imap != nil {
		e.imap.Close()
	}
	for c := range e.connections {
		c.Close()
	}
	e.mu.Unlock()
	e.workers.Wait()
}
func (e *fakeEmail) client(account string) (*relayemail.Client, error) {
	return relayemail.NewWithTransport(relayemail.Config{AccountID: account, Enabled: true, Host: "127.0.0.1", Port: 587, Username: simulationMailUser, Password: simulationMailPassword, From: "inbox@sim.invalid", IMAPHost: "127.0.0.1", IMAPPort: 993, TrustedAuthservID: simulationAuthserv, TrustedDSNDomain: "sim.invalid"}, e.roots, e.dial)
}
func (e *fakeEmail) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("simulation refused mail network")
	}
	var target net.Listener
	switch address {
	case "127.0.0.1:587":
		target = e.smtp
		refused := false
		err := e.state.change(func(s *emailState) error {
			if queue := s.Faults["connect"]; len(queue) > 0 && queue[0] == "refuse" {
				s.Faults["connect"] = queue[1:]
				refused = true
			}
			return nil
		})
		if err != nil || refused {
			e.capture("smtp", "connect", map[string]any{"stage": "connect"}, 0, "refuse")
			return nil, errors.New("simulation refused SMTP connection")
		}
	case "127.0.0.1:993":
		target = e.imap
	default:
		return nil, errors.New("simulation refused mail destination")
	}
	if target == nil {
		return nil, errors.New("simulation mail listener unavailable")
	}
	return (&net.Dialer{}).DialContext(ctx, network, target.Addr().String())
}
func (e *fakeEmail) faults(stage string, faults []string) error {
	switch stage {
	case "connect", "auth", "rcpt", "data_command", "data_reply":
	default:
		return errors.New("invalid SMTP stage")
	}
	for _, f := range faults {
		if f != "451" && f != "550" && f != "disconnect" && f != "timeout" && !(stage == "data_reply" && f == "disconnect_after") && !(stage == "connect" && f == "refuse") {
			return errors.New("invalid SMTP fault")
		}
	}
	return e.state.change(func(s *emailState) error { s.Faults[stage] = faults; return nil })
}
func (e *fakeEmail) smtpFault(conn net.Conn, stage string) bool {
	fault := ""
	err := e.state.change(func(s *emailState) error {
		if len(s.Faults[stage]) > 0 {
			fault = s.Faults[stage][0]
			s.Faults[stage] = s.Faults[stage][1:]
		}
		return nil
	})
	if err != nil {
		return true
	}
	if fault == "" {
		return false
	}
	e.capture("smtp", stage, map[string]any{"stage": stage}, 0, fault)
	switch fault {
	case "451", "550":
		fmt.Fprintf(conn, "%s Simulation failure\r\n", fault)
	case "timeout":
		_, _ = io.Copy(io.Discard, conn)
	}
	return true
}
func (e *fakeEmail) capture(kind, path string, body any, status int, fault string) {
	raw, _ := json.Marshal(body)
	req := &http.Request{Method: "EXCHANGE", URL: &url.URL{Path: "/" + path}, Header: http.Header{}}
	e.records.record(req, kind, raw, status, nil, time.Now(), fault)
}
func (e *fakeEmail) serveSMTP(conn net.Conn) {
	if e.smtpFault(conn, "connect") {
		return
	}
	reader := bufio.NewReaderSize(conn, 8192)
	secure, authenticated := false, false
	message := smtpMessage{}
	reply := func(code int, text string) { fmt.Fprintf(conn, "%d %s\r\n", code, text) }
	reply(220, "Local simulation SMTP")
	for {
		line, err := readProtocolLine(reader)
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO":
			if secure {
				io.WriteString(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
			} else {
				io.WriteString(conn, "250-localhost\r\n250 STARTTLS\r\n")
			}
		case "STARTTLS":
			if secure {
				reply(503, "Already secure")
				continue
			}
			reply(220, "Begin TLS")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{e.certificate}, MinVersion: tls.VersionTLS12})
			if tlsConn.Handshake() != nil {
				return
			}
			conn = tlsConn
			reader = bufio.NewReaderSize(conn, 8192)
			secure = true
		case "AUTH":
			if !secure {
				reply(530, "TLS required")
				continue
			}
			if e.smtpFault(conn, "auth") {
				return
			}
			encoded := strings.TrimPrefix(arg, "PLAIN ")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || string(decoded) != "\x00"+simulationMailUser+"\x00"+simulationMailPassword {
				reply(535, "Authentication failed")
				continue
			}
			authenticated = true
			reply(235, "Authenticated")
		case "MAIL":
			if !authenticated {
				reply(530, "Authentication required")
				continue
			}
			if !strings.HasPrefix(arg, "FROM:") {
				reply(501, "Invalid sender")
				continue
			}
			message = smtpMessage{From: strings.TrimPrefix(arg, "FROM:")}
			reply(250, "Sender accepted")
		case "RCPT":
			if !authenticated || message.From == "" {
				reply(503, "MAIL required")
				continue
			}
			if e.smtpFault(conn, "rcpt") {
				return
			}
			if !strings.HasPrefix(arg, "TO:") {
				reply(501, "Invalid recipient")
				continue
			}
			message.Recipients = append(message.Recipients, strings.TrimPrefix(arg, "TO:"))
			reply(250, "Recipient accepted")
		case "DATA":
			if !authenticated || len(message.Recipients) == 0 {
				reply(503, "RCPT required")
				continue
			}
			if !e.smtpData(conn, reader, message) {
				return
			}
			message = smtpMessage{}
		case "RSET":
			message = smtpMessage{}
			reply(250, "Reset")
		case "NOOP":
			reply(250, "OK")
		case "QUIT":
			reply(221, "Bye")
			return
		default:
			reply(502, "Unsupported command")
		}
	}
}
func (e *fakeEmail) smtpData(conn net.Conn, reader *bufio.Reader, message smtpMessage) bool {
	reply := func(code int, text string) { fmt.Fprintf(conn, "%d %s\r\n", code, text) }
	if e.smtpFault(conn, "data_command") {
		return false
	}
	reply(354, "End with a dot")
	raw, err := io.ReadAll(io.LimitReader(textproto.NewReader(reader).DotReader(), relayemail.MaxMessageBytes+1))
	if err != nil {
		return false
	}
	if len(raw) > relayemail.MaxMessageBytes {
		reply(552, "Too large")
		return false
	}
	message.Raw = string(raw)
	// Lost final acknowledgement persists acceptance; all other faults reject it.
	fault := ""
	err = e.state.change(func(s *emailState) error {
		if len(s.Faults["data_reply"]) > 0 {
			fault = s.Faults["data_reply"][0]
			s.Faults["data_reply"] = s.Faults["data_reply"][1:]
		}
		if fault == "" || fault == "disconnect_after" {
			s.Sent = append(s.Sent, message)
		}
		return nil
	})
	if err != nil {
		reply(451, "State unavailable")
		return false
	}
	status := 0
	if fault == "" {
		status = 250
	} else if fault == "451" || fault == "550" {
		status = mustCode(fault)
	}
	e.capture("smtp", "data_reply", message, status, fault)
	if fault != "" {
		switch fault {
		case "451", "550":
			reply(mustCode(fault), "Simulation failure")
		case "timeout":
			_, _ = io.Copy(io.Discard, conn)
		}
		return false
	}
	reply(250, "Stored")
	return true
}
func mustCode(value string) int { n, _ := strconv.Atoi(value); return n }
func readProtocolLine(reader *bufio.Reader) (string, error) {
	raw, err := reader.ReadSlice('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// This read-only IMAP subset implements the commands issued by FetchMailbox.
func (e *fakeEmail) serveIMAP(raw net.Conn) {
	conn := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{e.certificate}, MinVersion: tls.VersionTLS12})
	if conn.Handshake() != nil {
		return
	}
	io.WriteString(conn, "* OK [CAPABILITY IMAP4rev1 IDLE] Local simulation IMAP\r\n")
	reader := bufio.NewReaderSize(conn, 8192)
	authenticated, selected := false, false
	for {
		line, err := readProtocolLine(reader)
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		tag, command := fields[0], strings.ToUpper(fields[1])
		ok := func() { fmt.Fprintf(conn, "%s OK Completed\r\n", tag) }
		switch command {
		case "CAPABILITY":
			io.WriteString(conn, "* CAPABILITY IMAP4rev1 IDLE\r\n")
			ok()
		case "LOGIN":
			if len(fields) == 4 && imapAtom(fields[2]) == simulationMailUser && imapAtom(fields[3]) == simulationMailPassword {
				authenticated = true
				ok()
			} else {
				fmt.Fprintf(conn, "%s NO Authentication failed\r\n", tag)
			}
		case "EXAMINE":
			if !authenticated || len(fields) != 3 || imapAtom(fields[2]) != "INBOX" {
				fmt.Fprintf(conn, "%s NO Mailbox unavailable\r\n", tag)
				continue
			}
			s := e.state.snapshot()
			selected = true
			fmt.Fprintf(conn, "* FLAGS (\\Seen)\r\n* %d EXISTS\r\n* OK [UIDVALIDITY %d] Validity\r\n* OK [UIDNEXT %d] Next\r\n%s OK [READ-ONLY] Examined\r\n", len(s.Inbox), s.Validity, s.Next, tag)
		case "IDLE":
			if !selected {
				fmt.Fprintf(conn, "%s BAD Select mailbox first\r\n", tag)
				continue
			}
			if !e.imapIdle(conn, reader, tag) {
				return
			}
		case "UID":
			if !selected || len(fields) < 4 {
				fmt.Fprintf(conn, "%s BAD Select mailbox first\r\n", tag)
				continue
			}
			s := e.state.snapshot()
			switch strings.ToUpper(fields[2]) {
			case "SEARCH":
				imapSearch(conn, tag, fields, s)
			case "FETCH":
				e.imapFetch(conn, tag, fields, line, s)
			default:
				fmt.Fprintf(conn, "%s BAD Unsupported UID command\r\n", tag)
			}
		case "LOGOUT":
			io.WriteString(conn, "* BYE Closing\r\n")
			ok()
			return
		case "NOOP":
			ok()
		default:
			fmt.Fprintf(conn, "%s BAD Unsupported command\r\n", tag)
		}
	}
}
func imapSearch(conn net.Conn, tag string, fields []string, s emailState) {
	var start, end uint32
	if len(fields) != 5 || strings.ToUpper(fields[3]) != "UID" {
		fmt.Fprintf(conn, "%s BAD Unsupported search\r\n", tag)
		return
	}
	first, last, ranged := strings.Cut(fields[4], ":")
	if !ranged {
		last = first
	}
	low, lowErr := strconv.ParseUint(first, 10, 32)
	high, highErr := strconv.ParseUint(last, 10, 32)
	if lowErr != nil || highErr != nil {
		fmt.Fprintf(conn, "%s BAD Invalid range\r\n", tag)
		return
	}
	start, end = uint32(low), uint32(high)

	io.WriteString(conn, "* SEARCH")
	for _, m := range s.Inbox {
		if m.UID >= start && m.UID <= end {
			fmt.Fprintf(conn, " %d", m.UID)
		}
	}
	io.WriteString(conn, "\r\n")
	fmt.Fprintf(conn, "%s OK Completed\r\n", tag)
}
func (e *fakeEmail) imapFetch(conn net.Conn, tag string, fields []string, line string, s emailState) {
	uid, err := strconv.ParseUint(fields[3], 10, 32)
	if err != nil || !strings.Contains(line, "BODY.PEEK[]<0.") {
		fmt.Fprintf(conn, "%s BAD Unsupported fetch\r\n", tag)
		return
	}
	for i, m := range s.Inbox {
		if uint64(m.UID) == uid {
			content := []byte(m.Raw)
			size := len(content)
			limit := relayemail.MaxMessageBytes + 1
			if _, tail, found := strings.Cut(line, "BODY.PEEK[]<0."); found {
				end, _, _ := strings.Cut(tail, ">")
				if n, err := strconv.Atoi(end); err == nil && n >= 0 {
					limit = min(limit, n)
				}
			}
			content = content[:min(len(content), limit)]
			fmt.Fprintf(conn, "* %d FETCH (UID %d RFC822.SIZE %d BODY[]<0> {%d}\r\n", i+1, m.UID, size, len(content))
			conn.Write(content)
			io.WriteString(conn, ")\r\n")
			e.capture("imap", "fetch", map[string]any{"uid": m.UID, "size": size}, 200, "")
		}
	}
	fmt.Fprintf(conn, "%s OK Completed\r\n", tag)
}
func imapAtom(s string) string {
	if strings.HasPrefix(s, "\"") {
		v, err := strconv.Unquote(s)
		if err == nil {
			return v
		}
	}
	return s
}
func authenticatedMail(raw, server, result string) (string, error) {
	if server == "" || strings.ContainsAny(server, "\r\n ;") {
		return "", errors.New("invalid authserv-id")
	}
	if result != "pass" && result != "fail" && result != "none" {
		return "", errors.New("invalid authentication result")
	}
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return "", err
	}
	from, err := mail.ParseAddress(message.Header.Get("From"))
	if err != nil {
		return "", err
	}
	domain := from.Address[strings.LastIndex(from.Address, "@")+1:]
	// A new trusted Received boundary makes supplied authentication headers untrusted.
	return fmt.Sprintf("Authentication-Results: %s; dmarc=%s header.from=%s\r\nReceived: from local.invalid by %s; %s\r\n%s", server, result, domain, server, time.Now().Format(time.RFC1123Z), raw), nil
}
func (e *fakeEmail) appendMail(raw string, uid uint32) (uint32, error) {
	if len(raw) > relayemail.MaxMessageBytes+1024 {
		return 0, errors.New("mail exceeds simulation limit")
	}
	err := e.state.change(func(s *emailState) error {
		if uid == 0 {
			uid = s.Next
		}
		if uid < s.Next || uid == ^uint32(0) {
			return errors.New("UID must advance")
		}
		s.Inbox = append(s.Inbox, mailboxMessage{uid, raw})
		s.Next = uid + 1
		return nil
	})
	if err == nil {
		e.mu.Lock()
		for wake := range e.idlers {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		e.mu.Unlock()
	}
	return uid, err
}
func bounceMail(messageID, recipient string) (string, error) {
	if !relayemail.ValidMessageID(messageID) || strings.ContainsAny(recipient, "\r\n") {
		return "", errors.New("invalid DSN identifiers")
	}
	address, err := mail.ParseAddress(recipient)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: postmaster@sim.invalid\r\nTo: inbox@sim.invalid\r\nMessage-ID: <%s@sim.invalid>\r\nSubject: Delivery failed\r\nMIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=sim-dsn\r\n\r\n", providerID("dsn"))
	fmt.Fprintf(&b, "--sim-dsn\r\nContent-Type: text/plain\r\n\r\nLocal simulation bounce.\r\n--sim-dsn\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.sim.invalid\r\nOriginal-Message-ID: %s\r\n\r\nFinal-Recipient: rfc822; %s\r\nAction: failed\r\nStatus: 5.1.1\r\n\r\n--sim-dsn--\r\n", messageID, address.Address)
	return authenticatedMail(b.String(), simulationAuthserv, "pass")
}

func (e *fakeEmail) imapIdle(conn net.Conn, reader *bufio.Reader, tag string) bool {
	wake := make(chan struct{}, 1)
	e.mu.Lock()
	e.idlers[wake] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.idlers, wake); e.mu.Unlock() }()
	if _, err := io.WriteString(conn, "+ Idling\r\n"); err != nil {
		return false
	}
	e.capture("imap", "idle", nil, 200, "")
	// Send current EXISTS after subscribing so arrivals between EXAMINE and IDLE
	// cannot disappear into the notification gap.
	select {
	case wake <- struct{}{}:
	default:
	}
	done := make(chan bool, 1)
	go func() { line, err := readProtocolLine(reader); done <- err == nil && strings.EqualFold(line, "DONE") }()
	for {
		select {
		case valid := <-done:
			if valid {
				fmt.Fprintf(conn, "%s OK IDLE completed\r\n", tag)
			}
			return valid
		case <-wake:
			if _, err := fmt.Fprintf(conn, "* %d EXISTS\r\n", len(e.state.snapshot().Inbox)); err != nil {
				return false
			}
		}
	}
}
