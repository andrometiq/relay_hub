package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/mail"

	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/jackc/pgx/v5"
)

// SaveEmail commits the inbound event and trusted DSN evidence together.
func (s Store) SaveEmail(ctx context.Context, event Event, statuses []relayemail.DeliveryStatus, attachments []relayemail.Attachment) error {
	if s.DB == nil || event.Account == "" {
		return errors.New("invalid email store")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	inserted, err := tx.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,sender,recipient,subject,body,occurred_at,payload)
 VALUES($1,'Email','inbound',$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(channel,account,provider_id) DO NOTHING`, event.Account, event.ProviderID, event.Sender, event.Recipient, event.Subject, event.Body, event.OccurredAt, event.Payload)
	if err != nil {
		return err
	}
	if inserted.RowsAffected() > 0 {
		metadata, err := saveEmailAttachments(ctx, tx, event.Account, event.ProviderID, attachments)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE relay_channel_events SET payload=jsonb_set(payload,'{attachments}',$3::jsonb) WHERE account=$1 AND channel='Email' AND provider_id=$2`, event.Account, event.ProviderID, raw); err != nil {
			return err
		}
		if err = saveEmailStatuses(ctx, tx, event.Account, statuses); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err == nil {
		WakeCallbacks(s.Wake)
	}
	return err
}

func saveEmailAttachments(ctx context.Context, tx pgx.Tx, account, providerID string, files []relayemail.Attachment) ([]map[string]any, error) {
	metadata := []map[string]any{}
	for slot, file := range files {
		raw, err := base64.StdEncoding.DecodeString(file.ContentBase64)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO relay_email_attachments(account,provider_id,slot,filename,content_type,content) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, account, providerID, slot, file.Filename, file.ContentType, raw); err != nil {
			return nil, err
		}
		metadata = append(metadata, map[string]any{"index": slot, "filename": file.Filename, "content_type": file.ContentType, "size": len(raw)})
	}
	return metadata, nil
}

func unverifiedEmailPayload(payload json.RawMessage) (relayemail.Received, error) {
	received := relayemail.Received{}
	if len(payload) > 0 {
		if json.Unmarshal(payload, &received) != nil {
			return received, errors.New("invalid email payload")
		}
	}
	received.Authentication = relayemail.Authentication{Status: "unverified"}
	received.DeliveryStatuses = nil
	received.Attachments = nil
	received.ContentHash = ""
	if len(received.HTML) > 200000 || len(received.References) > 20 || len(received.To)+len(received.Cc) > 50 {
		return received, errors.New("invalid email metadata")
	}
	if received.MessageID != "" && !relayemail.ValidMessageID(received.MessageID) {
		return received, errors.New("invalid email message ID")
	}
	if received.InReplyTo != "" && !relayemail.ValidMessageID(received.InReplyTo) {
		return received, errors.New("invalid email reply ID")
	}
	for _, id := range received.References {
		if !relayemail.ValidMessageID(id) {
			return received, errors.New("invalid email reference")
		}
	}
	for _, list := range [][]string{received.To, received.Cc} {
		for _, value := range list {
			if _, err := mail.ParseAddress(value); err != nil {
				return received, errors.New("invalid email recipient")
			}
		}
	}
	return received, nil
}
