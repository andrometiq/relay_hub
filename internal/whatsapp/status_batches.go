package whatsapp

import (
	"encoding/json"
	"errors"
	"strconv"
)

const statusBatchLimit = 500
const statusBatchBytes = 512 * 1024

var errStatusBatchFull = errors.New("receipt exceeds status batch byte limit")

// StatusCursor records the last fully consumed event, or the number of raw
// statuses consumed inside EventID when Offset is nonzero. It is committed
// only after Core acknowledges the complete batch.
type StatusCursor struct {
	EventID int64 `json:"event_id"`
	Offset  int   `json:"offset"`
}

type CompactStatus struct {
	EventID       int64  `json:"event_id"`
	Position      int    `json:"position"`
	MessageID     string `json:"message_id"`
	Recipient     string `json:"recipient"`
	Participant   string `json:"participant,omitempty"`
	RecipientType string `json:"recipient_type,omitempty"`
	Status        string `json:"status"`
	Timestamp     int64  `json:"timestamp"`
	ChannelKey    string `json:"channel_key,omitempty"`
	To            string `json:"to,omitempty"`
}

// buildStatusBatch works only on immutable, ascending status-lane events.
// Unknown status types consume their raw position but never become a receipt.
// Recognized malformed receipts fail closed so the caller cannot acknowledge
// data it could not faithfully transport.
func buildStatusBatch(events []Event, start StatusCursor, byteLimit int) ([]CompactStatus, StatusCursor, error) {
	return buildStatusBatchLimit(events, start, byteLimit, statusBatchLimit)
}

type receiptError struct{ Position int }

func (e *receiptError) Error() string { return "invalid delivery receipt" }
func buildStatusBatchLimit(events []Event, start StatusCursor, byteLimit, limit int) ([]CompactStatus, StatusCursor, error) {
	if byteLimit <= 0 || byteLimit > statusBatchBytes || start.EventID < 0 || start.Offset < 0 {
		return nil, start, errors.New("invalid status batch boundary")
	}
	if start.Offset > 0 && len(events) > 0 && events[0].ID != start.EventID {
		return nil, start, errors.New("partial status event is missing")
	}
	out := []CompactStatus{}
	cursor := start
	size := 2
	previous := int64(0)
	for _, event := range events {
		if event.ID <= previous || event.ID < start.EventID || (event.ID == start.EventID && start.Offset == 0) {
			return nil, start, errors.New("status events are not ordered after cursor")
		}
		previous = event.ID
		var envelope struct {
			Entry []struct {
				Changes []struct {
					Value struct {
						Statuses []json.RawMessage `json:"statuses"`
					} `json:"value"`
				} `json:"changes"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			return nil, start, err
		}
		statuses := []json.RawMessage{}
		for _, entry := range envelope.Entry {
			for _, change := range entry.Changes {
				statuses = append(statuses, change.Value.Statuses...)
			}
		}
		offset := 0
		if event.ID == start.EventID {
			offset = start.Offset
		}
		if offset > len(statuses) {
			return nil, start, errors.New("status cursor exceeds immutable event")
		}
		for i := offset; i < len(statuses); i++ {
			var raw struct {
				ID            string `json:"id"`
				Recipient     string `json:"recipient_id"`
				Participant   string `json:"recipient_participant_id"`
				RecipientType string `json:"recipient_type"`
				Status        string `json:"status"`
				Timestamp     string `json:"timestamp"`
			}
			if err := json.Unmarshal(statuses[i], &raw); err != nil {
				return nil, start, &receiptError{Position: i}
			}
			switch raw.Status {
			case "sent", "delivered", "read", "failed":
			default:
				cursor = StatusCursor{EventID: event.ID, Offset: i + 1}
				continue
			}
			stamp, err := strconv.ParseInt(raw.Timestamp, 10, 64)
			if err != nil || stamp < 0 || len(raw.ID) == 0 || len(raw.ID) > 300 || len(raw.Recipient) == 0 || len(raw.Recipient) > 200 || len(raw.Participant) > 200 || (raw.RecipientType != "" && raw.RecipientType != "group" && raw.RecipientType != "individual") || (raw.Participant != "" && raw.RecipientType != "group") {
				return nil, start, &receiptError{Position: i}
			}
			row := CompactStatus{EventID: event.ID, Position: i, MessageID: raw.ID, Recipient: raw.Recipient, Participant: raw.Participant, RecipientType: raw.RecipientType, Status: raw.Status, Timestamp: stamp, ChannelKey: event.ChannelKey, To: event.To}
			encoded, err := json.Marshal(row)
			if err != nil {
				return nil, start, err
			}
			additional := len(encoded)
			if len(out) > 0 {
				additional++
			}
			if len(out) == limit || size+additional > byteLimit {
				if len(out) == 0 {
					return nil, start, errStatusBatchFull
				}
				return out, cursor, nil
			}
			out = append(out, row)
			size += additional
			cursor = StatusCursor{EventID: event.ID, Offset: i + 1}
		}
		cursor = StatusCursor{EventID: event.ID}
	}
	return out, cursor, nil
}
