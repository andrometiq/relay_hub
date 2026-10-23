// Package email provides bounded, provider-neutral email message handling.
package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"time"
)

type Config struct {
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
func (c *Client) Send(ctx context.Context, to []string, subject, body string, cc, bcc []string) error {
	return c.SendMessage(ctx, Message{To: to, Cc: cc, Bcc: bcc, Subject: subject, Body: body})
}
func (c *Client) SendMessage(ctx context.Context, in Message) error {
	if !c.cfg.Enabled {
		return errors.New("email account is disabled")
	}
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
	if c.cfg.Port == 465 {
		secure := tls.Client(conn, c.TLSConfig())
		if err = secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, c.cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if c.cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err = client.StartTLS(c.TLSConfig()); err != nil {
				return err
			}
		} else {
			return errors.New("SMTP server does not support required TLS")
		}
	}
	if c.cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)); err != nil {
			return err
		}
	}
	from, _ := mail.ParseAddress(c.cfg.From)
	if err = client.Mail(from.Address); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err = client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = writer.Write(msg); err != nil {
		writer.Close()
		return err
	}
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
