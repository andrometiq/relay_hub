package whatsapp

import (
	"strings"
	"testing"
)

func TestBulkSendsLeaveHalfTheAccountQueueForConversations(t *testing.T) {
	if BulkQueueLimit*2 != QueueLimit {
		t.Fatalf("bulk share = %d of %d", BulkQueueLimit, QueueLimit)
	}
	cases := []struct {
		count, bulkCount int
		bulk             bool
		refused          string
	}{
		{BulkQueueLimit - 1, BulkQueueLimit - 1, true, ""},
		{BulkQueueLimit, BulkQueueLimit, true, "bulk queue is full"},
		{BulkQueueLimit, BulkQueueLimit, false, ""},
		{QueueLimit - 1, BulkQueueLimit, false, ""},
		{QueueLimit, BulkQueueLimit, false, "account queue is full"},
		{QueueLimit, 0, true, "account queue is full"},
	}
	for _, c := range cases {
		err := queueRoom(c.count, c.bulkCount, c.bulk)
		if c.refused == "" && err != nil || c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)) {
			t.Fatalf("queueRoom(%d, %d, %v) = %v, want %q", c.count, c.bulkCount, c.bulk, err, c.refused)
		}
	}
}
