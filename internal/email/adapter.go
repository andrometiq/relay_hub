// Package email provides bounded, provider-neutral email message handling.
package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"github.com/andrometiq/relay_hub/internal/deliverypolicy"
)

type Config struct {
	TrustedAuthservID, TrustedDSNDomain              string
	IMAPHost, IMAPUsername, IMAPPassword, IMAPFolder string
	IMAPPort                                         int
	AccountID, Host, Username, Password, From        string
	Port                                             int
	Enabled                                          bool
}
type Client struct {
	cfg Config
	// Private roots let package simulations trust their local certificate authority.
	// Production clients use the system trust store and always verify certificates.
	tlsRoots *x509.CertPool
	// Simulations can route fixed protocol ports to an ephemeral loopback listener.
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func New(cfg Config) (*Client, error) {
	if cfg.AccountID == "" || cfg.Host == "" || cfg.Port < 1 || cfg.Port > 65535 {
		return nil, errors.New("invalid email configuration")
	}
	if _, e := mail.ParseAddress(cfg.From); e != nil {
		return nil, errors.New("invalid from address")
	}
	if cfg.IMAPHost != "" {
		if cfg.IMAPPort == 0 {
			cfg.IMAPPort = 993
		}
		if cfg.IMAPPort < 1 || cfg.IMAPPort > 65535 {
			return nil, errors.New("invalid IMAP port")
		}
		if cfg.IMAPUsername == "" {
			cfg.IMAPUsername = cfg.Username
		}
		if cfg.IMAPPassword == "" {
			cfg.IMAPPassword = cfg.Password
		}
		if cfg.IMAPFolder == "" {
			cfg.IMAPFolder = "INBOX"
		}
		if len(cfg.IMAPFolder) > 200 {
			return nil, errors.New("invalid IMAP folder")
		}
	}
	return &Client{cfg: cfg}, nil
}

// NewWithTransport supplies private CA roots and a mandatory restricted dialer
// for isolated protocol simulations. Certificate verification remains enabled.
func NewWithTransport(cfg Config, roots *x509.CertPool, dial func(context.Context, string, string) (net.Conn, error)) (*Client, error) {
	if roots == nil || dial == nil {
		return nil, errors.New("private roots and restricted dialer are required")
	}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	c.tlsRoots, c.dialContext = roots.Clone(), dial
	return c, nil
}

func (c *Client) Send(ctx context.Context, to []string, subject, body string, cc, bcc []string) error {
	return c.SendMessage(ctx, Message{To: to, Cc: cc, Bcc: bcc, Subject: subject, Body: body})
}
func (c *Client) SendMessage(ctx context.Context, in Message) (sendErr error) {
	stage := "configuration"
	dataStarted := false
	defer func() {
		if sendErr == nil {
			return
		}
		code := 0
		var protocol *textproto.Error
		if errors.As(sendErr, &protocol) {
			code = protocol.Code
		}
		unknown := dataStarted && (code < 400 || code >= 600)
		// Once DATA begins, even explicit transient rejection requires review;
		// only failures before DATA are automatically retryable.
		retry := !dataStarted && stage != "configuration" && stage != "compose" && (code == 0 || code/100 == 4)
		sendErr = &SendError{Stage: stage, Code: code, OutcomeUnknown: unknown, Retryable: retry, cause: sendErr}
	}()
	if !c.cfg.Enabled {
		return errors.New("email account is disabled")
	}
	stage = "compose"
	msg, recipients, err := c.compose(in)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	address := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	dial := c.dialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	stage = "connect"
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	stage = "tls"
	if c.cfg.Port == 465 {
		secure := tls.Client(conn, c.TLSConfig())
		if err = secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	}
	stage = "greeting"
	client, err := smtp.NewClient(conn, c.cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	stage = "tls"
	if c.cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err = client.StartTLS(c.TLSConfig()); err != nil {
				return err
			}
		} else {
			return errors.New("SMTP server does not support required TLS")
		}
	}
	stage = "auth"
	if c.cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)); err != nil {
			return err
		}
	}
	from, _ := mail.ParseAddress(c.cfg.From)
	stage = "mail"
	if err = client.Mail(from.Address); err != nil {
		return err
	}
	stage = "rcpt"
	for _, recipient := range recipients {
		if err = client.Rcpt(recipient); err != nil {
			return err
		}
	}
	stage = "data_command"
	if err := deliverypolicy.BeforeSubmission(ctx); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	stage = "data"
	dataStarted = true
	if _, err = writer.Write(msg); err != nil {
		// Closing the DATA writer would terminate and potentially submit a partial message.
		return err
	}
	stage = "data_reply"
	if err = writer.Close(); err != nil {
		return err
	}
	// DATA acceptance is authoritative; a failed QUIT must not suggest resending.
	_ = client.Quit()
	return nil
}
func (c *Client) TLSConfig() *tls.Config {
	return &tls.Config{ServerName: c.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: c.tlsRoots}
}

// SendError exposes safe protocol metadata without provider text or credentials.
type SendError struct {
	Stage          string `json:"stage"`
	Code           int    `json:"code"`
	OutcomeUnknown bool   `json:"outcome_unknown"`
	Retryable      bool   `json:"retryable"`
	cause          error
}

func (e *SendError) Error() string {
	return fmt.Sprintf("SMTP delivery failed at %s (code %d, outcome unknown: %t)", e.Stage, e.Code, e.OutcomeUnknown)
}
func (e *SendError) Unwrap() error { return e.cause }
