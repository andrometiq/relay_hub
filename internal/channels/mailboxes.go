package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

type MailboxReader interface {
	MailboxKey() string
	FetchMailbox(context.Context, relayemail.MailboxCursor, func(uint32, uint32, *relayemail.Received, string) error) (relayemail.MailboxCursor, error)
}

type MailboxPoller struct {
	DB       *pgxpool.Pool
	Registry *Registry
}

func (p MailboxPoller) Run(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for account, client := range p.Registry.snapshot().email {
					if !client.MailboxConfigured() {
						continue
					}
					work, cancel := context.WithTimeout(ctx, 45*time.Second)
					p.Poll(work, account, client)
					cancel()
					if ctx.Err() != nil {
						return
					}
				}
			}
		}
	}()
}
func (p MailboxPoller) Poll(ctx context.Context, account string, client MailboxReader) error {
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
	if err = conn.QueryRow(ctx, `SELECT uid_validity,last_uid FROM relay_mailbox_cursor WHERE account=$1 AND mailbox=$2`, account, key).Scan(&cursor.Validity, &cursor.UID); err != nil {
		return err
	}
	next, err := client.FetchMailbox(ctx, cursor, func(validity, uid uint32, message *relayemail.Received, reason string) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if message == nil {
			_, e = tx.Exec(ctx, `INSERT INTO relay_mailbox_rejections(account,mailbox,uid_validity,uid,reason) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, account, key, validity, uid, reason)
		} else {
			providerID := fmt.Sprintf("imap:%s:%d:%d", key, validity, uid)
			metadata := []map[string]any{}
			for slot, file := range message.Attachments {
				raw, decodeErr := base64.StdEncoding.DecodeString(file.ContentBase64)
				if decodeErr != nil {
					return decodeErr
				}
				if _, e = tx.Exec(ctx, `INSERT INTO relay_email_attachments(account,provider_id,slot,filename,content_type,content) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, account, providerID, slot, file.Filename, file.ContentType, raw); e != nil {
					return e
				}
				metadata = append(metadata, map[string]any{"index": slot, "filename": file.Filename, "content_type": file.ContentType, "size": len(raw)})
			}
			payload, _ := json.Marshal(map[string]any{"message_id": message.MessageID, "in_reply_to": message.InReplyTo, "references": message.References, "to": message.To, "cc": message.Cc, "html": message.HTML, "auto_submitted": message.AutoSubmitted, "attachments": metadata})
			_, e = tx.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,sender,recipient,subject,body,payload) VALUES($1,'Email','inbound',$2,$3,$4,$5,$6,$7) ON CONFLICT(channel,account,provider_id) DO NOTHING`, account, providerID, message.From, strings.Join(message.To, ","), message.Subject, message.Body, payload)
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
		_, _ = conn.Exec(write, `UPDATE relay_mailbox_cursor SET last_error='Mailbox synchronization failed; check credentials, TLS and mailbox access' WHERE account=$1 AND mailbox=$2`, account, key)
		return err
	}
	_, err = conn.Exec(ctx, `UPDATE relay_mailbox_cursor SET uid_validity=$3,last_uid=$4,last_sync=now(),last_error='' WHERE account=$1 AND mailbox=$2`, account, key, next.Validity, next.UID)
	return err
}
