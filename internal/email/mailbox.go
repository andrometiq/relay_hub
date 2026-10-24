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
	"slices"
	"strconv"
	"time"
)

type MailboxCursor struct {
	Validity     uint32
	UID          uint32
	Backlog      bool
	ResetUIDNext uint32
}

func (c *Client) MailboxConfigured() bool { return c.cfg.Enabled && c.cfg.IMAPHost != "" }

// FetchMailbox uses read-only EXAMINE and BODY.PEEK, so ingestion does not mark
// mail read. A page covers at most 20 UIDs and one bounded body at a time.
func (c *Client) FetchMailbox(ctx context.Context, cursor MailboxCursor, checkpoint func(MailboxCursor) error, consume func(uint32, uint32, *Received, string) error) (MailboxCursor, error) {
	fail := errors.New("mailbox connection or synchronization failed")
	if !c.MailboxConfigured() {
		return cursor, fail
	}
	client, mailbox, closeConnection, err := c.connectMailbox(ctx)
	defer closeConnection()
	if err != nil {
		return cursor, fail
	}
	if cursor.Validity != mailbox.UIDValidity {
		cursor = MailboxCursor{Validity: mailbox.UIDValidity, ResetUIDNext: uint32(mailbox.UIDNext)}
		// Persist the reset snapshot even when empty or the first fetch fails.
		if err := checkpoint(cursor); err != nil {
			return cursor, err
		}
	}
	ceiling := uint32(mailbox.UIDNext) - 1
	if cursor.UID >= ceiling {
		cursor.Backlog = false
		return cursor, nil
	}
	// Bound the UID search window as well as the number of bodies per page.
	end := uint32(min(uint64(cursor.UID)+10000, uint64(ceiling)))
	search, err := client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{{{Start: imap.UID(cursor.UID + 1), Stop: imap.UID(end)}}}}, nil).Wait()
	if err != nil {
		return cursor, fail
	}
	uids := search.AllUIDs()
	slices.Sort(uids)
	uids = slices.Compact(uids)
	more := len(uids) > 20
	if more {
		uids = uids[:20]
	}
	cursor.Backlog = more || end < ceiling
	for _, found := range uids {
		uid := uint32(found)
		if uid <= cursor.UID || uid > end {
			return cursor, fail
		}
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
						if value.Literal.Size() > MaxMessageBytes {
							// Release the literal reader before waiting for the decoder.
							closeConnection()
							_, _ = io.Copy(io.Discard, value.Literal)
							client.Close()
							if e := consume(cursor.Validity, uint32(uid), nil, "Message exceeds 6 MiB"); e != nil {
								return cursor, e
							}
							cursor.UID = uint32(uid)
							return cursor, fail
						}
						raw, err = io.ReadAll(value.Literal)
						if err != nil || int64(len(raw)) != value.Literal.Size() {
							client.Close()
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
			received, parseErr := c.ParseIngress(raw)
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
	if !more {
		cursor.UID = end
	}
	cursor.Backlog = cursor.UID < ceiling
	return cursor, nil
}

func (c *Client) MailboxKey() string {
	sum := sha256.Sum256([]byte(c.cfg.IMAPHost + ":" + strconv.Itoa(c.cfg.IMAPPort) + ":" + c.cfg.IMAPUsername + ":" + c.cfg.IMAPFolder))
	return hex.EncodeToString(sum[:])
}

func (c *Client) connectMailbox(ctx context.Context) (*imapclient.Client, *imap.SelectData, func(), error) {
	return c.connectMailboxUpdates(ctx, nil)
}

func (c *Client) connectMailboxUpdates(ctx context.Context, updates *imapclient.UnilateralDataHandler) (*imapclient.Client, *imap.SelectData, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)

	cleanup := func() { cancel() }
	dial := c.dialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(c.cfg.IMAPHost, strconv.Itoa(c.cfg.IMAPPort)))
	if err != nil {
		return nil, nil, cleanup, errors.New("mailbox connection failed; check settings, TLS and credentials")
	}

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cleanup = func() { stop(); conn.Close(); cancel() }

	options := &imapclient.Options{UnilateralDataHandler: updates, TLSConfig: &tls.Config{ServerName: c.cfg.IMAPHost, MinVersion: tls.VersionTLS12, RootCAs: c.tlsRoots}}
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
		return nil, nil, cleanup, errors.New("mailbox connection failed; check settings, TLS and credentials")
	}

	if err = client.Login(c.cfg.IMAPUsername, c.cfg.IMAPPassword).Wait(); err != nil {
		return nil, nil, cleanup, errors.New("mailbox connection failed; check settings, TLS and credentials")
	}
	mailbox, err := client.Select(c.cfg.IMAPFolder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || mailbox.UIDValidity == 0 || mailbox.UIDNext == 0 {
		return nil, nil, cleanup, errors.New("mailbox connection failed; check settings, TLS and credentials")
	}
	return client, mailbox, cleanup, nil
}

// TestMailbox authenticates and opens the folder read-only without fetching mail.
func (c *Client) TestMailbox(ctx context.Context) error {
	if !c.MailboxConfigured() {
		return errors.New("mailbox is not configured")
	}
	_, _, closeConnection, err := c.connectMailbox(ctx)
	defer closeConnection()
	return err
}

// WatchMailbox holds no database connection or fetch worker while waiting for
// EXISTS. Reopening every 30 seconds bounds missed notifications and supports
// servers without IDLE; ingestion still uses the durable UID/UIDVALIDITY cursor.
func (c *Client) WatchMailbox(ctx context.Context, notify func()) error {
	return c.watchMailbox(ctx, notify, 30*time.Second)
}

func (c *Client) watchMailbox(ctx context.Context, notify func(), recheck time.Duration) error {
	updates := make(chan struct{}, 1)
	client, _, closeConnection, err := c.connectMailboxUpdates(ctx, &imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				select {
				case updates <- struct{}{}:
				default:
				}
			}
		},
	})
	defer closeConnection()
	if err != nil {
		return err
	}
	defer client.Close()
	// Close the gap between the last fetch and EXAMINE, even without an EXISTS.
	notify()
	timer := time.NewTimer(recheck)
	defer timer.Stop()
	if !client.Caps().Has(imap.CapIdle) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	idle, err := client.Idle()
	if err != nil {
		return err
	}
	defer idle.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-client.Closed():
			return errors.New("mailbox IDLE disconnected")
		case <-timer.C:
			return nil
		case <-updates:
			notify()
		}
	}
}
