package containers

import (
	"context"
	"errors"
	"testing"
)

func holdEvents(t *testing.T, ctx context.Context, s *Store, digest string) {
	t.Helper()
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane) VALUES('sales',$1,'{"object":"whatsapp_business_account"}','realtime')`, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_channel_events(account,channel,direction,provider_id) VALUES('bot','Telegram','inbound',$1)`, digest); err != nil {
		t.Fatal(err)
	}
}

func heldCount(t *testing.T, ctx context.Context, s *Store, ref ChannelRef) int {
	t.Helper()
	count, err := s.HeldEvents(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestLinkingANumberReplaysItsHeldEventsOnce(t *testing.T) {
	ctx, s := directoryStore(t)
	featureNumber(t, ctx, s, "sales", map[string]any{"phone_id": "11", "waba_id": "22"})
	if err := s.SaveChannel(ctx, Channel{Key: "bot", Channel: "Telegram", Container: "box", Enabled: true, Config: map[string]any{}}, "", true); err != nil {
		t.Fatal(err)
	}
	sales, bot := ChannelRef{Channel: "WhatsApp", Key: "sales"}, ChannelRef{Channel: "Telegram", Key: "bot"}
	holdEvents(t, ctx, s, "first")
	if heldCount(t, ctx, s, sales) != 1 || heldCount(t, ctx, s, bot) != 1 {
		t.Fatal("held events not counted")
	}
	if _, err := s.ReplayHeld(ctx, sales); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("replay without a site: %v", err)
	}
	if err := s.AssignChannels(ctx, "shop", []ChannelRef{sales, bot}); err != nil {
		t.Fatal(err)
	}
	var moved int
	if err := s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM relay_whatsapp_events WHERE account='sales' AND site_id='shop' AND replayed_at IS NOT NULL)
 + (SELECT count(*) FROM relay_channel_events WHERE account='bot' AND site_id='shop' AND replayed_at IS NOT NULL)`).Scan(&moved); err != nil || moved != 2 {
		t.Fatalf("linking moved %d held events: %v", moved, err)
	}
	if heldCount(t, ctx, s, sales) != 0 || heldCount(t, ctx, s, bot) != 0 {
		t.Fatal("events still held after linking")
	}
	if err := s.AssignChannels(ctx, "shop", []ChannelRef{sales, bot}); err != nil {
		t.Fatal(err)
	}
	if replayed, err := s.ReplayHeld(ctx, sales); err != nil || replayed != 0 {
		t.Fatalf("events replayed again: %d %v", replayed, err)
	}
}

func TestReplayHeldGivesOldHeldEventsToTheLinkedSite(t *testing.T) {
	ctx, s := directoryStore(t)
	featureNumber(t, ctx, s, "sales", map[string]any{"phone_id": "11", "waba_id": "22"})
	sales := ChannelRef{Channel: "WhatsApp", Key: "sales"}
	if err := s.AssignChannels(ctx, "shop", []ChannelRef{sales}); err != nil {
		t.Fatal(err)
	}
	// Events held before this Hub version replayed them on link.
	holdEvents(t, ctx, s, "older")
	if heldCount(t, ctx, s, sales) != 1 {
		t.Fatal("held event not counted")
	}
	if replayed, err := s.ReplayHeld(ctx, sales); err != nil || replayed != 1 {
		t.Fatalf("first replay: %d %v", replayed, err)
	}
	if replayed, err := s.ReplayHeld(ctx, sales); err != nil || replayed != 0 {
		t.Fatalf("second replay: %d %v", replayed, err)
	}
	if _, err := s.HeldEvents(ctx, ChannelRef{Channel: "WhatsApp", Key: "missing"}); err == nil {
		t.Fatal("unknown number accepted")
	}
}
