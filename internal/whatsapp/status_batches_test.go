package whatsapp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func statusFixture(id int64, count int) Event {
	statuses := make([]map[string]string, count)
	for i := range statuses {
		statuses[i] = map[string]string{"id": fmt.Sprintf("wamid.%d.%d", id, i), "recipient_id": "919000000055", "status": "delivered", "timestamp": "100"}
	}
	raw, _ := json.Marshal(map[string]any{"entry": []any{map[string]any{"changes": []any{map[string]any{"value": map[string]any{"statuses": statuses}}}}}})
	return Event{ID: id, Payload: raw}
}

func TestStatusBatchSplitsAnEventAndReplaysExactly(t *testing.T) {
	events := []Event{statusFixture(7, 1251), statusFixture(9, 2)}
	cursor := StatusCursor{}
	sizes := []int{}
	seen := map[string]bool{}
	for {
		pending := []Event{}
		for _, event := range events {
			if event.ID > cursor.EventID || event.ID == cursor.EventID && cursor.Offset > 0 {
				pending = append(pending, event)
			}
		}
		if len(pending) == 0 {
			break
		}
		rows, next, err := buildStatusBatch(pending, cursor, statusBatchBytes)
		if err != nil {
			t.Fatal(err)
		}
		replay, replayNext, err := buildStatusBatch(pending, cursor, statusBatchBytes)
		if err != nil || !reflect.DeepEqual(rows, replay) || next != replayNext {
			t.Fatal("retry changed immutable batch")
		}
		for _, row := range rows {
			if seen[row.MessageID] {
				t.Fatal("duplicate receipt")
			}
			seen[row.MessageID] = true
		}
		sizes = append(sizes, len(rows))
		cursor = next
	}
	if fmt.Sprint(sizes) != "[500 500 253]" || len(seen) != 1253 || cursor != (StatusCursor{EventID: 9}) {
		t.Fatalf("incorrect chunks: %v %v", sizes, cursor)
	}
}

func TestStatusBatchBytesAndMalformedReceipt(t *testing.T) {
	event := statusFixture(1, 20)
	rows, cursor, err := buildStatusBatch([]Event{event}, StatusCursor{}, 400)
	if err != nil || len(rows) == 0 || len(rows) >= 20 || cursor.Offset != len(rows) {
		t.Fatalf("byte cap not respected: %d %v %v", len(rows), cursor, err)
	}
	raw, _ := json.Marshal(rows)
	if len(raw) > 400 {
		t.Fatal("encoded batch exceeds cap")
	}
	if _, _, err = buildStatusBatch([]Event{event}, StatusCursor{}, 10); err == nil {
		t.Fatal("oversized first receipt accepted")
	}
	if _, _, err = buildStatusBatch([]Event{event}, StatusCursor{EventID: 1, Offset: 21}, 400); err == nil {
		t.Fatal("invalid partial cursor accepted")
	}
	if _, _, err = buildStatusBatch([]Event{statusFixture(2, 1)}, StatusCursor{EventID: 1, Offset: 1}, 400); err == nil {
		t.Fatal("missing partial event was skipped")
	}
	bad := Event{ID: 2, Payload: json.RawMessage(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"x","status":"read","timestamp":"100"}]}}]}]}`)}
	if _, next, err := buildStatusBatch([]Event{event, bad}, StatusCursor{}, statusBatchBytes); err == nil || next != (StatusCursor{}) {
		t.Fatal("malformed receipt advanced acknowledgement")
	}
}

func TestGroupStatusBatchPreservesParticipants(t *testing.T) {
	raw := []byte(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"wamid.group","recipient_id":"group-a","recipient_type":"group","recipient_participant_id":"919000000001","status":"read","timestamp":"100"},{"id":"wamid.group","recipient_id":"group-a","recipient_type":"group","recipient_participant_id":"919000000002","status":"delivered","timestamp":"101"}]}}]}]}`)
	rows, _, err := buildStatusBatch([]Event{{ID: 1, Payload: raw}}, StatusCursor{}, statusBatchBytes)
	if err != nil || len(rows) != 2 {
		t.Fatalf("group batch: %v %#v", err, rows)
	}
	for i, row := range rows {
		if row.Recipient != "group-a" || row.RecipientType != "group" || row.Participant != fmt.Sprintf("91900000000%d", i+1) {
			t.Fatalf("participant lost: %#v", row)
		}
	}
}
