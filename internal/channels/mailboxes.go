package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/callbackworker"
	"github.com/andrometiq/relay_hub/internal/deliverypolicy"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

type MailboxReader interface {
	MailboxKey() string
	FetchMailbox(context.Context, relayemail.MailboxCursor, func(relayemail.MailboxCursor) error, func(uint32, uint32, *relayemail.Received, string) error) (relayemail.MailboxCursor, error)
}

type MailboxPoller struct {
	DB       *pgxpool.Pool
	Registry *Registry
}

// NewMailboxPool isolates connections held during IMAP I/O from request traffic.
func NewMailboxPool(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	config = config.Copy()
	config.MaxConns = 8
	config.MinConns = 0
	return pgxpool.NewWithConfig(ctx, config)
}

func (p MailboxPoller) Run(ctx context.Context) {
	go p.run(ctx, time.Second, func() map[string]MailboxReader {
		readers := map[string]MailboxReader{}
		for account, client := range p.Registry.snapshot().email {
			if client.MailboxConfigured() {
				readers[account] = client
			}
		}
		return readers
	})
}
func (p MailboxPoller) run(ctx context.Context, interval time.Duration, readers func() map[string]MailboxReader) {
	callbackworker.Run(ctx, interval, 8, func() []callbackworker.Job {
		jobs := []callbackworker.Job{}
		for account, client := range readers() {
			jobs = append(jobs, callbackworker.Job{Key: account, Run: func(ctx context.Context) { _ = p.poll(ctx, account, client, true) }})
		}
		return jobs
	})
}
func (p MailboxPoller) Poll(ctx context.Context, account string, client MailboxReader) error {
	return p.poll(ctx, account, client, false)
}
func (p MailboxPoller) poll(ctx context.Context, account string, client MailboxReader, scheduled bool) error {
	conn, err := p.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	key := client.MailboxKey()
	lock := account + ":" + key
	var acquired bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,732))`, lock).Scan(&acquired); err != nil || !acquired {
		return err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, e := conn.Exec(release, `SELECT pg_advisory_unlock(hashtextextended($1,732))`, lock); e != nil {
			conn.Hijack().Close(release)
		}
	}()
	if _, err = conn.Exec(ctx, `INSERT INTO relay_mailbox_cursor(account,mailbox) VALUES($1,$2) ON CONFLICT DO NOTHING`, account, key); err != nil {
		return err
	}
	cursor := relayemail.MailboxCursor{}
	failures := 0
	var ready bool
	if err = conn.QueryRow(ctx, `SELECT uid_validity,last_uid,reset_uid_next,failures,next_attempt<=now() FROM relay_mailbox_cursor WHERE account=$1 AND mailbox=$2`, account, key).Scan(&cursor.Validity, &cursor.UID, &cursor.ResetUIDNext, &failures, &ready); err != nil {
		return err
	}
	if scheduled && !ready {
		return nil
	}
	next, err := client.FetchMailbox(ctx, cursor, func(reset relayemail.MailboxCursor) error {
		_, e := conn.Exec(ctx, `UPDATE relay_mailbox_cursor SET uid_validity=$3,last_uid=0,reset_uid_next=$4 WHERE account=$1 AND mailbox=$2`, account, key, reset.Validity, reset.ResetUIDNext)
		if e == nil {
			cursor = reset
		}
		return e
	}, func(validity, uid uint32, message *relayemail.Received, reason string) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if message == nil {
			_, e = tx.Exec(ctx, `INSERT INTO relay_mailbox_rejections(account,mailbox,uid_validity,uid,reason) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, account, key, validity, uid, reason)
		} else {
			e = saveMailboxMessage(ctx, tx, account, key, validity, uid, uid < cursor.ResetUIDNext, message)
		}
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE relay_mailbox_cursor SET uid_validity=$3,last_uid=$4,last_sync=now(),last_error='' WHERE account=$1 AND mailbox=$2`, account, key, validity, uid); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		write, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.Exec(write, `UPDATE relay_mailbox_cursor SET last_error='Mailbox synchronization failed; check credentials, TLS and mailbox access',failures=least(failures+1,100),next_attempt=now()+$3*interval '1 second' WHERE account=$1 AND mailbox=$2`, account, key, deliverypolicy.RetryDelay(failures+1, 0).Seconds())
		return err
	}
	delay := 30
	if next.Backlog {
		delay = 0
	}
	_, err = conn.Exec(ctx, `UPDATE relay_mailbox_cursor SET uid_validity=$3,last_uid=$4,last_sync=now(),last_error='',failures=0,next_attempt=now()+$5*interval '1 second' WHERE account=$1 AND mailbox=$2`, account, key, next.Validity, next.UID, delay)
	return err
}

func saveMailboxMessage(ctx context.Context, tx pgx.Tx, account, key string, validity, uid uint32, historical bool, message *relayemail.Received) error {
	providerID := fmt.Sprintf("imap:%s:%d:%d", key, validity, uid)
	if message.ContentHash != "" {
		// Match occurrences across epochs, preserving distinct identical copies in one epoch.
		var prior string
		if historical {
			var err error
			prior, err = priorMailboxIdentity(ctx, tx, account, key, validity, message)
			if err != nil {
				return err
			}
		}
		identity := providerID
		if prior != "" {
			identity = prior
		}
		inserted, err := tx.Exec(ctx, `INSERT INTO relay_mailbox_messages(account,mailbox,uid_validity,uid,content_hash,provider_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account,mailbox,uid_validity,uid) DO UPDATE
 SET content_hash=EXCLUDED.content_hash WHERE relay_mailbox_messages.content_hash=''`, account, key, validity, uid, message.ContentHash, identity)
		if err != nil || inserted.RowsAffected() == 0 || prior != "" {
			return err
		}
	}

	metadata, err := saveEmailAttachments(ctx, tx, account, providerID, message.Attachments)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"message_id": message.MessageID, "in_reply_to": message.InReplyTo, "references": message.References, "to": message.To, "cc": message.Cc, "html": message.HTML, "auto_submitted": message.AutoSubmitted, "attachments": metadata, "authentication": message.Authentication, "content_hash": message.ContentHash})
	if err != nil {
		return err
	}
	inserted, err := tx.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,sender,recipient,subject,body,payload) VALUES($1,'Email','inbound',$2,$3,$4,$5,$6,$7) ON CONFLICT(channel,account,provider_id) DO NOTHING`, account, providerID, message.From, strings.Join(message.To, ","), message.Subject, message.Body, payload)
	if err != nil {
		return err
	}
	if inserted.RowsAffected() > 0 {
		return saveEmailStatuses(ctx, tx, account, message.DeliveryStatuses)
	}
	return nil
}

// Legacy mail has no raw hash. Compare all retained content, including attachment
// bytes, rather than trusting Message-ID (which may be absent or reused).
func priorMailboxIdentity(ctx context.Context, tx pgx.Tx, account, key string, validity uint32, message *relayemail.Received) (string, error) {
	files := message.Attachments
	if files == nil {
		files = []relayemail.Attachment{}
	}
	payload, err := json.Marshal(map[string]any{"message_id": message.MessageID, "in_reply_to": message.InReplyTo, "references": message.References, "to": message.To, "cc": message.Cc, "html": message.HTML, "auto_submitted": message.AutoSubmitted, "attachments": files})
	if err != nil {
		return "", err
	}
	var prior string
	err = tx.QueryRow(ctx, `SELECT m.provider_id FROM relay_mailbox_messages m
 WHERE m.account=$1 AND m.mailbox=$2 AND m.uid_validity<>$4
 AND (m.content_hash=$3 OR (m.content_hash='' AND EXISTS (
 SELECT 1 FROM relay_channel_events e WHERE e.account=m.account AND e.channel='Email' AND e.provider_id=m.provider_id
 AND e.sender=$5 AND e.recipient=$6 AND e.subject=$7 AND e.body=$8
 AND e.payload-'attachments'-'authentication'-'content_hash'=$9::jsonb-'attachments'
 AND coalesce((SELECT jsonb_agg(jsonb_build_object('filename',a.filename,'content_type',a.content_type,
 'content_base64',replace(encode(a.content,'base64'),chr(10),'')) ORDER BY a.slot)
 FROM relay_email_attachments a WHERE a.account=e.account AND a.provider_id=e.provider_id),'[]'::jsonb)=$9::jsonb->'attachments')))
 AND NOT EXISTS(SELECT 1 FROM relay_mailbox_messages n WHERE n.account=m.account AND n.mailbox=m.mailbox AND n.uid_validity=$4 AND n.provider_id=m.provider_id)
 ORDER BY m.uid_validity DESC,m.uid LIMIT 1`, account, key, message.ContentHash, validity, message.From, strings.Join(message.To, ","), message.Subject, message.Body, payload).Scan(&prior)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return prior, err
}
