package containers

import (
	"context"
	"testing"

	"github.com/andrometiq/relay_hub/internal/database"
)

func featureNumber(t *testing.T, ctx context.Context, s *Store, key string, config map[string]any) Channel {
	t.Helper()
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_containers(container_id,name) VALUES('box','box') ON CONFLICT DO NOTHING;
 INSERT INTO relay_sites(site_id,origin) VALUES('shop','https://shop.invalid') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	c := Channel{Key: key, Channel: "WhatsApp", Container: "box", Enabled: true, Config: config}
	if err := s.SaveChannel(ctx, c, "", true); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetChannel(ctx, ChannelRef{Channel: "WhatsApp", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func channelNotices(t *testing.T, ctx context.Context, s *Store) int {
	t.Helper()
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM relay_whatsapp_events WHERE site_id='shop' AND payload->>'object'='relay_channels'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNewWhatsAppNumberStartsWithTemplatesAndSync(t *testing.T) {
	ctx, s := directoryStore(t)
	added := featureNumber(t, ctx, s, "sales", map[string]any{"phone_id": "11", "waba_id": "22"})
	if added.Config["templates"] != true || added.Config["sync_metadata"] != true || added.Config["groups"] != nil || added.Config["calls"] != nil {
		t.Fatalf("new number features: %#v", added.Config)
	}
	chosen := featureNumber(t, ctx, s, "support", map[string]any{"phone_id": "33", "waba_id": "22", "templates": false, "sync_metadata": false})
	if chosen.Config["templates"] != false || chosen.Config["sync_metadata"] != false {
		t.Fatalf("explicit choice replaced: %#v", chosen.Config)
	}
}

func TestFeatureSwitchTellsTheSite(t *testing.T) {
	ctx, s := directoryStore(t)
	number := featureNumber(t, ctx, s, "sales", map[string]any{"phone_id": "11", "waba_id": "22"})
	if err := s.AssignChannels(ctx, "shop", []ChannelRef{{Channel: "WhatsApp", Key: "sales"}}); err != nil {
		t.Fatal(err)
	}
	before := channelNotices(t, ctx, s)
	number.Config["sync_metadata"] = false
	if err := s.SaveChannel(ctx, number, "", false); err != nil {
		t.Fatal(err)
	}
	if channelNotices(t, ctx, s) != before {
		t.Fatal("hourly sync is not in Core's channel list, but the site was told")
	}
	number.Config["groups"] = true
	if err := s.SaveChannel(ctx, number, "", false); err != nil {
		t.Fatal(err)
	}
	if channelNotices(t, ctx, s) != before+1 {
		t.Fatal("site was not told that groups changed")
	}
}

func TestTemplatesMigrationTurnsTemplatesOnOnly(t *testing.T) {
	ctx, s := directoryStore(t)
	featureNumber(t, ctx, s, "sales", map[string]any{"phone_id": "11", "waba_id": "22", "templates": false, "sync_metadata": false})
	featureNumber(t, ctx, s, "spare", map[string]any{"phone_id": "33", "waba_id": "22", "templates": false, "sync_metadata": false})
	if _, err := s.DB.Exec(ctx, `UPDATE relay_channel_credentials SET config=config-'templates' WHERE channel_key='spare'`); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "shop", []ChannelRef{{Channel: "WhatsApp", Key: "sales"}}); err != nil {
		t.Fatal(err)
	}
	before := channelNotices(t, ctx, s)
	migration, err := database.Files.ReadFile("migrations/0045_whatsapp_templates_on.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sales", "spare"} {
		number, err := s.GetChannel(ctx, ChannelRef{Channel: "WhatsApp", Key: key})
		if err != nil || number.Config["templates"] != true || number.Config["sync_metadata"] != false {
			t.Fatalf("%s after migration: %#v %v", key, number.Config, err)
		}
	}
	if channelNotices(t, ctx, s) != before+1 {
		t.Fatal("linked site was not told that templates changed")
	}
}
