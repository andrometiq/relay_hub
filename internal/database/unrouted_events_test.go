package database

import "testing"

func TestUnroutedWhatsAppEventsAreNotQueued(t *testing.T) {
	c, ctx := testConnection(t)
	if err := Migrate(ctx, c); err != nil {
		t.Fatal(err)
	}
	insert := func(account, digest string, site *string) int64 {
		t.Helper()
		var id int64
		err := c.QueryRow(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id)
 VALUES($1,$2,'{"object":"whatsapp_business_account"}','realtime',$3) RETURNING id`, account, digest, site).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	queued := func(id int64) int {
		t.Helper()
		var count int
		if err := c.QueryRow(ctx, "SELECT count(*) FROM relay_whatsapp_callback_delivery WHERE event_id=$1", id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	site := "distributor"
	unrouted := insert("", "unrouted", nil)
	notice := insert("", "notice", &site)
	held := insert("number", "held", nil)
	if queued(unrouted) != 0 || queued(notice) != 1 || queued(held) != 1 {
		t.Fatal("only events with a channel key or a site queue a callback", queued(unrouted), queued(notice), queued(held))
	}

	if _, err := c.Exec(ctx, "INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES($1,'realtime')", unrouted); err != nil {
		t.Fatal(err)
	}
	cleanup, err := Files.ReadFile("migrations/0047_unrouted_events_not_queued.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, string(cleanup)); err != nil {
		t.Fatal(err)
	}
	if queued(unrouted) != 0 || queued(notice) != 1 || queued(held) != 1 {
		t.Fatal("cleanup removed the wrong callbacks")
	}
}
