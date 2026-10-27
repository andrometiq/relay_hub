package channels

import (
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"sync"
	"testing"
)

func TestEmailStatusConcurrentReplayAndBatching(t *testing.T) {
	pool, _ := channelTestDB(t)
	// More than one callback page with meaningful distinct recipients and message IDs.
	_, err := pool.Exec(t.Context(), `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash,destinations,result)
 SELECT 'load','key-'||n,'email.send','{}','test',ARRAY['email:r@example.invalid'],jsonb_build_object('message_id','<load-'||n||'@example.invalid>') FROM generate_series(1,1003) n;
 INSERT INTO relay_email_delivery_evidence(account,message_id,recipient,state,code,rank)
 SELECT 'load','<load-'||n||'@example.invalid>','r@example.invalid','delivered','2.0.0',3 FROM generate_series(1,1003) n`)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := pool.Acquire(t.Context())
			if e != nil {
				t.Error(e)
				return
			}
			defer c.Release()
			if e = projectEmailStatuses(t.Context(), c.Conn(), "load"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	c, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	total := 0
	for {
		batch, err := PendingStatusReceipts(t.Context(), c.Conn(), "load")
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		if len(batch) > 500 {
			t.Fatal("unbounded callback")
		}
		total += len(batch)
		ids := make([]int64, len(batch))
		for i := range batch {
			ids[i] = batch[i].ID
		}
		if err = AcknowledgeStatusReceipts(t.Context(), c.Conn(), "load", ids); err != nil {
			t.Fatal(err)
		}
	}
	if total != 1003 {
		t.Fatal("duplicate/lost evidence", total)
	}
}

func TestEmailStatusConcurrentRecipientOrder(t *testing.T) {
	pool, _ := channelTestDB(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states := []relayemail.DeliveryStatus{{MessageID: "<same@example.invalid>", Recipient: "a@example.invalid", State: "delivered", Code: "2.0.0"}, {MessageID: "<same@example.invalid>", Recipient: "b@example.invalid", State: "delivered", Code: "2.0.0"}}
			if i%2 == 0 {
				states[0], states[1] = states[1], states[0]
				for j := range states {
					states[j].State = "delayed"
					states[j].Code = "4.0.0"
				}
			}
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			defer tx.Rollback(t.Context())
			if err = saveEmailStatuses(t.Context(), tx, "concurrent", states); err != nil {
				t.Error(err)
				return
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM relay_email_delivery_evidence WHERE account='concurrent' AND state='delivered'`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
