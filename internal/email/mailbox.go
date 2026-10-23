package email

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"io"
	"net"
	"strconv"
	"time"
)

type MailboxCursor struct {
	Validity uint32
	UID      uint32
}

func (c *Client) MailboxConfigured() bool { return c.cfg.Enabled && c.cfg.IMAPHost != "" }

// FetchMailbox uses read-only EXAMINE and BODY.PEEK, so ingestion does not mark
// mail read. A page covers at most 20 UIDs and one bounded body at a time.
func (c *Client) FetchMailbox(ctx context.Context, cursor MailboxCursor, consume func(uint32, uint32, *Received, string) error) (MailboxCursor, error) {
	fail := errors.New("mailbox connection or synchronization failed")
	if !c.MailboxConfigured() {
		return cursor, fail
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	dial := c.dialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(c.cfg.IMAPHost, strconv.Itoa(c.cfg.IMAPPort)))
	if err != nil {
		return cursor, fail
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	options := &imapclient.Options{TLSConfig: &tls.Config{ServerName: c.cfg.IMAPHost, MinVersion: tls.VersionTLS12, RootCAs: c.tlsRoots}}
	var client *imapclient.Client
	if c.cfg.IMAPPort == 143 {
		client, err = imapclient.NewStartTLS(conn, options)
	} else {
		secure := tls.Client(conn, options.TLSConfig)
		if err = secure.HandshakeContext(ctx); err == nil {
			client = imapclient.New(secure, options)
		}
	}
	if err != nil {
		return cursor, fail
	}
	defer client.Close()
	if err = client.Login(c.cfg.IMAPUsername, c.cfg.IMAPPassword).Wait(); err != nil {
		return cursor, fail
	}
	mailbox, err := client.Select(c.cfg.IMAPFolder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || mailbox.UIDValidity == 0 || mailbox.UIDNext == 0 {
		return cursor, fail
	}
	if cursor.Validity != mailbox.UIDValidity {
		cursor = MailboxCursor{Validity: mailbox.UIDValidity}
	}
	ceiling := uint32(mailbox.UIDNext) - 1
	if cursor.UID >= ceiling {
		return cursor, nil
	}
	end := min(uint64(cursor.UID)+20, uint64(ceiling))
	for uid := uint64(cursor.UID) + 1; uid <= end; uid++ {
		section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: MaxMessageBytes + 1}}
		command := client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}})
		var raw []byte
		var returned imap.UID
		var size int64
		count := 0
		for message := command.Next(); message != nil; message = command.Next() {
			count++
			if count > 1 {
				client.Close()
				return cursor, fail
			}
			for item := message.Next(); item != nil; item = message.Next() {
				switch value := item.(type) {
				case imapclient.FetchItemDataUID:
					returned = value.UID
				case imapclient.FetchItemDataRFC822Size:
					size = value.Size
				case imapclient.FetchItemDataBodySection:
					if value.Literal != nil {
						raw, err = io.ReadAll(io.LimitReader(value.Literal, MaxMessageBytes+1))
						if err != nil || len(raw) > MaxMessageBytes {
							client.Close()
							if e := consume(cursor.Validity, uint32(uid), nil, "Message exceeds 6 MiB"); e != nil {
								return cursor, e
							}
							cursor.UID = uint32(uid)
							return cursor, fail
						}
					}
				}
			}
		}
		if err = command.Close(); err != nil {
			return cursor, fail
		}
		if count == 1 {
			if returned != imap.UID(uid) {
				return cursor, fail
			}
			received, parseErr := Parse(raw)
			reason := ""
			if size > MaxMessageBytes || parseErr != nil {
				reason = "Message MIME or size exceeds supported limits"
			}
			if reason != "" {
				err = consume(cursor.Validity, uint32(uid), nil, reason)
			} else {
				err = consume(cursor.Validity, uint32(uid), &received, "")
			}
			if err != nil {
				return cursor, err
			}
		}
		cursor.UID = uint32(uid)
	}
	return cursor, nil
}

func (c *Client) MailboxKey() string {
	sum := sha256.Sum256([]byte(c.cfg.IMAPHost + ":" + strconv.Itoa(c.cfg.IMAPPort) + ":" + c.cfg.IMAPUsername + ":" + c.cfg.IMAPFolder))
	return hex.EncodeToString(sum[:])
}
