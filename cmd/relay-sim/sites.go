package main

import (
	"context"
	"errors"
	"net/url"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5"
)

// coreBinding is the Core delivery identity of one simulated site.
type coreBinding struct {
	Site           string `json:"site"`
	APIKey         string `json:"api_key"`
	APISecret      string `json:"api_secret"`
	CallbackSecret string `json:"callback_secret"`
}

func (b coreBinding) valid() bool {
	u, err := url.Parse(b.Site)
	return err == nil && u.Scheme == "http" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == "" &&
		len(b.CallbackSecret) >= 32 && b.APIKey != "" && b.APISecret != ""
}

// simulationSite is one simulated site with one number in its own WABA, one
// bot and one mailbox. Only the first mailbox reads the shared fake IMAP inbox.
type simulationSite struct {
	ID, Suffix, Phone, BotToken, From string
	IMAP                              bool
}

var simulationSites = []simulationSite{
	{"sim-site-1", "-1", "110", simulationBotToken, "inbox@sim.invalid", true},
	{"sim-site-2", "-2", "111", simulationBotToken2, "inbox2@sim.invalid", false},
}

type simulationChannel struct {
	channel containers.Channel
	secret  string
}

func (site simulationSite) channels() []simulationChannel {
	number := simulationNumbers[site.Phone]
	mail := map[string]any{"host": "127.0.0.1", "port": float64(587), "username": simulationMailUser, "from": site.From}
	if site.IMAP {
		mail["imap_host"], mail["imap_port"] = "127.0.0.1", float64(993)
	}
	wa := map[string]any{"phone_id": site.Phone, "waba_id": number.WABA, "display_number": number.Display, "templates": true}
	return []simulationChannel{
		{containers.Channel{Key: "sim-wa" + site.Suffix, Channel: "WhatsApp", Container: "sim-app", Enabled: true, Config: wa}, ""},
		{containers.Channel{Key: "sim-bot" + site.Suffix, Channel: "Telegram", Container: "sim-app", Enabled: true, Config: map[string]any{"username": simulationBots[site.BotToken]}}, site.BotToken},
		{containers.Channel{Key: "sim-mail" + site.Suffix, Channel: "Email", Container: "sim-app", Enabled: true, Config: mail}, simulationMailPassword},
	}
}

// provisionSites stores one container (one Meta app), two sites and their
// channels, activates them and returns an MCP token per site.
func (s *simulation) provisionSites(ctx context.Context, bindings []coreBinding) (map[string]string, error) {
	store, err := containers.New(s.db, s.credentialKey)
	if err != nil {
		return nil, err
	}
	app := &containers.ProviderInput{AccessToken: "sim-provider-token", Config: map[string]any{"app_id": "sim-app", "graph_version": "v25.0", "app_secret": "sim-app-secret", "verify_token": "sim-verify-token"}}
	if err = store.SaveContainer(ctx, containers.Container{ID: "sim-app", Name: "Simulation app", Enabled: true}, app, false); err != nil {
		return nil, err
	}
	tokens := map[string]string{}
	for i, binding := range bindings {
		site := simulationSites[i].ID
		secrets := map[string]any{"core_api_key": binding.APIKey, "core_api_secret": binding.APISecret, "callback_secret": binding.CallbackSecret}
		if err = store.SaveSite(ctx, containers.Site{ID: site, Name: site, Origin: binding.Site, Enabled: true}, secrets, false); err != nil {
			return nil, err
		}
		refs := []containers.ChannelRef{}
		for _, row := range simulationSites[i].channels() {
			if err = store.SaveChannel(ctx, row.channel, row.secret, false); err != nil {
				return nil, err
			}
			refs = append(refs, containers.ChannelRef{Channel: row.channel.Channel, Key: row.channel.Key})
		}
		if err = store.AssignChannels(ctx, site, refs); err != nil {
			return nil, err
		}
		if tokens[site], err = s.siteToken(ctx, site); err != nil {
			return nil, err
		}
	}
	return tokens, s.activateSites(ctx, store)
}

// activateSites loads the stored channels as the Hub does, with the fake
// Telegram and email clients in place of the real ones.
func (s *simulation) activateSites(ctx context.Context, store *containers.Store) error {
	rows, err := store.Runtime(ctx)
	if err != nil {
		return err
	}
	next, err := channels.Load(rows)
	if err != nil {
		return err
	}
	numbers, callbacks, origins := []whatsapp.Account{}, []channels.RuntimeCallback{}, []string{}
	for _, row := range rows {
		if !row.Enabled || row.Site == "" {
			continue
		}
		origins = append(origins, row.Origin)
		switch row.Channel {
		case "WhatsApp":
			numbers = append(numbers, simulationNumber(row))
			s.webhookPath.Store(&row.WebhookID)
			continue
		case "Telegram":
			next.Telegram[row.Key], err = telegram.NewWithHTTPClient(telegram.Config{AccountID: row.Key, BotToken: row.Secret, Enabled: true}, s.channelHTTP())
		case "Email":
			from, _ := row.Config["from"].(string)
			next.Email[row.Key], err = s.email.client(row.Key, from, row.Config["imap_host"] != nil)
		}
		if err != nil {
			return err
		}
		callbacks = append(callbacks, channels.RuntimeCallback{Account: row.Key, Channel: row.Channel, SiteID: row.Site, Site: row.Origin,
			To: map[string]string{row.Channel: channels.DisplayAddress(row)}, APIKey: row.Core.APIKey, APISecret: row.Core.APISecret,
			CallbackSecret: row.Core.CallbackSecret, HTTPClient: s.channelHTTP()})
	}
	s.transport.allowSites(origins...)
	if err = s.registry.Replace(next); err != nil {
		return err
	}
	s.callbacks.Update(callbacks)
	return s.wa.ReplaceAccounts(numbers)
}

// simulationNumber is one stored number with its container app and site.
func simulationNumber(row containers.RuntimeCredential) whatsapp.Account {
	get := func(key string) string { value, _ := row.Config[key].(string); return value }
	return whatsapp.Account{ID: row.Key, ContainerID: row.Container, WebhookID: row.WebhookID, AccessToken: row.Secret,
		DisplayNumber: get("display_number"), PhoneID: get("phone_id"), WABAID: get("waba_id"), Version: get("graph_version"),
		AppSecret: get("app_secret"), VerifyToken: get("verify_token"), Templates: true,
		SiteID: row.Site, Site: row.Origin, CoreAPIKey: row.Core.APIKey, CoreAPISecret: row.Core.APISecret, CallbackSecret: row.Core.CallbackSecret}
}

// siteToken replaces the simulation admin's MCP token for one site.
func (s *simulation) siteToken(ctx context.Context, site string) (string, error) {
	authStore := &auth.Store{DB: s.db}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM relay_users").Scan(&count); err != nil {
		return "", errors.New("auth unavailable")
	}
	if count == 0 {
		if err := authStore.Bootstrap(ctx, "sim-admin", auth.Secret()); err != nil {
			return "", errors.New("bootstrap failed")
		}
	}
	var admin auth.User
	if err := s.db.QueryRow(ctx, "SELECT id,username,role,auth_version FROM relay_users WHERE username='sim-admin' AND enabled").Scan(&admin.ID, &admin.Username, &admin.Role, &admin.Version); err != nil {
		return "", errors.New("simulation admin unavailable")
	}
	if err := authStore.Grant(ctx, admin, admin.ID, site, true); err != nil {
		return "", errors.New("grant failed")
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM relay_api_tokens WHERE user_id=$1 AND site_id=$2 AND name='simulation' AND revoked_at IS NULL", admin.ID, site)
	if err != nil {
		return "", errors.New("token rotation unavailable")
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return "", errors.New("token rotation unavailable")
	}
	for _, id := range ids {
		if err = authStore.Revoke(ctx, admin, id); err != nil {
			return "", errors.New("token rotation failed")
		}
	}
	token, err := authStore.Issue(ctx, admin, "simulation", site, 1)
	if err != nil {
		return "", errors.New("token issue failed")
	}
	return token, nil
}
