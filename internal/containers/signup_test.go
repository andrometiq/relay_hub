package containers

import (
	"errors"
	"testing"
)

func TestOnlyOneContainerHostsSignup(t *testing.T) {
	ctx, s := directoryStore(t)
	app := &ProviderInput{Config: map[string]any{"app_id": "333", "graph_version": "v25.0", "es_config_id": "es-1", "app_secret": "app-secret", "verify_token": "verify"}}
	if err := s.SaveContainer(ctx, Container{ID: "first", Enabled: true, Signup: true}, app, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContainer(ctx, Container{ID: "second", Enabled: true, Signup: true}, nil, true); !errors.Is(err, ErrSignupTaken) {
		t.Fatalf("second signup container: %v", err)
	}
	signup, err := s.SignupApp(ctx)
	if err != nil || signup.Container != "first" || signup.AppSecret != "app-secret" || signup.ConfigID != "es-1" {
		t.Fatalf("signup app %+v %v", signup, err)
	}
	stored, err := s.GetContainer(ctx, "first")
	if err != nil || !stored.Signup || stored.WhatsApp.Config["es_config_id"] != "es-1" {
		t.Fatalf("stored container %+v %v", stored, err)
	}
	apps, err := s.WhatsAppApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].Container != "first" || apps[0].VerifyToken != "verify" || apps[0].WebhookID == "" {
		t.Fatalf("container apps %+v %v", apps, err)
	}
	if err = s.SaveContainer(ctx, Container{ID: "first", Enabled: true}, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SignupApp(ctx); !errors.Is(err, ErrNoSignupContainer) {
		t.Fatalf("signup switched off: %v", err)
	}
}

func TestSignupChannelIsLinkedOnceAndKeepsOtherChannels(t *testing.T) {
	ctx, s := directoryStore(t)
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_containers(container_id,name,signup) VALUES('signup','signup',true),('manual','manual',false);
 INSERT INTO relay_sites(site_id,origin) VALUES('shop','https://shop.invalid'),('other','https://other.invalid')`); err != nil {
		t.Fatal(err)
	}
	existing := Channel{Key: "old", Channel: "WhatsApp", Container: "manual", Enabled: true, Config: map[string]any{"phone_id": "50", "waba_id": "60"}}
	if err := s.SaveChannel(ctx, existing, "token-old", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "shop", []ChannelRef{{Channel: "WhatsApp", Key: "old"}}); err != nil {
		t.Fatal(err)
	}
	number := Channel{Key: "wa-120", Channel: "WhatsApp", Container: "signup", Enabled: true, Config: map[string]any{
		"phone_id": "120", "waba_id": "230", "business_id": "330", "onboarding": "new", "coexistence": false, "pin": "123456"}}
	for range 2 {
		if key, err := s.SaveSignupChannel(ctx, "shop", number, "biz-token"); err != nil || key != "wa-120" {
			t.Fatal(key, err)
		}
	}
	site, err := s.GetSite(ctx, "shop")
	if err != nil || len(site.Channels) != 2 {
		t.Fatalf("site channels %+v %v", site.Channels, err)
	}
	credential, err := s.Credential(ctx, "WhatsApp", "wa-120")
	if err != nil || credential.Secret != "biz-token" || credential.Config["pin"] != "123456" || credential.Config["onboarding"] != "new" {
		t.Fatalf("signup credential %+v %v", credential.Config, err)
	}
	if _, err = s.SaveSignupChannel(ctx, "other", number, "biz-token"); !errors.Is(err, ErrNumberInUse) {
		t.Fatalf("number of another site: %v", err)
	}
	reused := number
	reused.Key, reused.Config = "wa-50", map[string]any{"phone_id": "50", "waba_id": "60"}
	if _, err = s.SaveSignupChannel(ctx, "other", reused, "biz-token"); !errors.Is(err, ErrNumberInUse) {
		t.Fatalf("manual number of another site: %v", err)
	}
	if err = s.LinkChannel(ctx, "other", ChannelRef{Channel: "WhatsApp", Key: "wa-120"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("link a foreign channel: %v", err)
	}

	sites, err := s.DisableWABA(ctx, "230")
	if err != nil || len(sites) != 1 || sites[0] != "shop" {
		t.Fatalf("disable WABA %v %v", sites, err)
	}
	removed, _ := s.GetChannel(ctx, ChannelRef{Channel: "WhatsApp", Key: "wa-120"})
	kept, _ := s.GetChannel(ctx, ChannelRef{Channel: "WhatsApp", Key: "old"})
	if removed.Enabled || removed.Config["partner_status"] != "removed" || !kept.Enabled {
		t.Fatalf("partner removal %+v %+v", removed, kept)
	}
	if sites, err = s.DisableWABA(ctx, "60"); err != nil || len(sites) != 0 {
		t.Fatalf("manual container number disabled: %v %v", sites, err)
	}
}

func TestSignupAdoptsTheSiteNumberFromAnotherContainer(t *testing.T) {
	ctx, s := directoryStore(t)
	if _, err := s.DB.Exec(ctx, `INSERT INTO relay_containers(container_id,name,signup) VALUES('signup','signup',true),('manual','manual',false);
 INSERT INTO relay_sites(site_id,origin) VALUES('shop','https://shop.invalid')`); err != nil {
		t.Fatal(err)
	}
	manual := Channel{Key: "old", Channel: "WhatsApp", Container: "manual", Enabled: true, Config: map[string]any{
		"phone_id": "50", "waba_id": "60", "app_secret": "manual-app-secret", "verify_token": "manual-verify"}}
	if err := s.SaveChannel(ctx, manual, "token-old", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignChannels(ctx, "shop", []ChannelRef{{Channel: "WhatsApp", Key: "old"}}); err != nil {
		t.Fatal(err)
	}
	number := Channel{Key: "wa-50", Channel: "WhatsApp", Container: "signup", Enabled: true, Config: map[string]any{"phone_id": "50", "waba_id": "60", "pin": "123456"}}
	key, err := s.SaveSignupChannel(ctx, "shop", number, "biz-token")
	if err != nil || key != "old" {
		t.Fatalf("adopted key %q %v", key, err)
	}
	adopted, err := s.Credential(ctx, "WhatsApp", "old")
	if err != nil || adopted.Container != "signup" || adopted.Secret != "biz-token" || adopted.Site != "shop" || adopted.Config["pin"] != "123456" {
		t.Fatalf("adopted number %+v %v", adopted, err)
	}
	if adopted.Config["app_secret"] == "manual-app-secret" || adopted.Config["verify_token"] == "manual-verify" {
		t.Fatal("the old app's webhook overrides were kept")
	}
	if _, err = s.GetChannel(ctx, ChannelRef{Channel: "WhatsApp", Key: "wa-50"}); err == nil {
		t.Fatal("a second channel was made for the adopted number")
	}
}
