package channels

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Event struct {
	ID                                                                        int64 `json:"id"`
	Account, Channel, Direction, ProviderID, Sender, Recipient, Subject, Body string
	OccurredAt                                                                time.Time       `json:"occurred_at"`
	Payload                                                                   json.RawMessage `json:"payload,omitempty"`
}
type Store struct{ DB *pgxpool.Pool }

// Only serve sticker IDs journaled for this account. Never turn the Hub into an
// arbitrary bot file downloader for a caller-supplied provider identifier.
func (s Store) TelegramStickerKnown(ctx context.Context, account, fileID string) (bool, error) {
	if s.DB == nil || account == "" || len(fileID) == 0 || len(fileID) > 512 {
		return false, errors.New("sticker lookup unavailable")
	}
	var known bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_channel_events
		WHERE account=$1 AND channel='Telegram' AND direction='inbound'
		AND coalesce(payload#>>'{message,sticker,file_id}',payload#>>'{edited_message,sticker,file_id}')=$2)`, account, fileID).Scan(&known)
	return known, err
}

func (s Store) Save(ctx context.Context, e Event) error {
	if s.DB == nil || e.Account == "" || e.Channel == "" || e.Direction == "" {
		return errors.New("invalid channel event")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id,sender,recipient,subject,body,occurred_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(channel,account,provider_id) DO NOTHING`, e.Account, e.Channel, e.Direction, e.ProviderID, e.Sender, e.Recipient, e.Subject, e.Body, e.OccurredAt, e.Payload)
	return err
}

func (s Store) TelegramMediaKnown(ctx context.Context, account, fileID string) (bool, error) {
	if s.DB == nil || account == "" || len(fileID) == 0 || len(fileID) > 256 {
		return false, errors.New("media lookup unavailable")
	}
	var known bool
	// The same bounded field paths cover new and edited messages and photo sizes.
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_channel_events WHERE account=$1 AND channel='Telegram' AND direction='inbound' AND jsonb_path_exists(payload,'$.*.* ? (@.file_id == $file || @[*].file_id == $file)',jsonb_build_object('file',$2::text)))`, account, fileID).Scan(&known)
	return known, err
}
