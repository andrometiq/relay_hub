package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/andrometiq/relay_hub/internal/containers"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/sitechannels"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

// simulationPublicURL is the HTTPS address the sim gives Telegram for
// webhooks. The fake Telegram delivers to it through the sim's own listener.
const simulationPublicURL = "https://hub.sim.invalid"

// simulationMailAllow lists the address ranges a site's mailbox may use in the
// sim, such as 127.0.0.1/32 for the fake mail servers. Unset, a loopback host
// is refused as it is in the Hub.
func simulationMailAllow() ([]netip.Prefix, error) {
	allow := []netip.Prefix{}
	for _, value := range strings.Split(os.Getenv("RELAY_SIM_MAIL_ALLOW"), ",") {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, errors.New("RELAY_SIM_MAIL_ALLOW must list address ranges")
		}
		allow = append(allow, prefix)
	}
	return allow, nil
}

// simulationClients builds the clients of a connect against the fake Telegram and mail servers.
type simulationClients struct {
	sim   *simulation
	hosts relayemail.PublicHosts
}

func (c simulationClients) Telegram(token string) (*telegram.Client, error) {
	return telegram.NewWithHTTPClient(telegram.Config{AccountID: "connect", BotToken: token, Enabled: true}, c.sim.channelHTTP())
}

// Email signs in with the settings the site gave, so a wrong login fails on the fake servers.
func (c simulationClients) Email(cfg relayemail.Config) (*relayemail.Client, error) {
	return relayemail.NewWithTransport(cfg, c.sim.email.roots, func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if _, err = c.hosts.Resolve(ctx, host); err != nil {
			return nil, err
		}
		return c.sim.email.dial(ctx, network, address)
	})
}

// siteChannels serves the site bot and mailbox tools against the fakes.
func (s *simulation) siteChannels() (*sitechannels.Service, error) {
	store, err := containers.New(s.db, s.credentialKey)
	if err != nil {
		return nil, err
	}
	allow, err := simulationMailAllow()
	if err != nil {
		return nil, err
	}
	hosts := relayemail.PublicHosts{Allow: allow}
	return &sitechannels.Service{Store: store, Registry: s.registry, Clients: simulationClients{sim: s, hosts: hosts}, Hosts: hosts,
		Reload: func(ctx context.Context) error { return s.activateSites(ctx, store) }, PublicURL: simulationPublicURL, TelegramMaster: s.token}, nil
}

// deliverTelegram posts an update to the webhook a bot registered, with the
// secret it registered, as Telegram does. invalid sends a wrong secret.
func (s *simulation) deliverTelegram(ctx context.Context, bot string, payload []byte, invalid bool) (int, error) {
	hook, ok := s.telegram.hook(bot)
	if !ok || !strings.HasPrefix(hook.URL, simulationPublicURL+"/") {
		return 0, errors.New("the bot has no webhook")
	}
	target, err := url.Parse(s.hub.String() + strings.TrimPrefix(hook.URL, simulationPublicURL))
	if err != nil {
		return 0, err
	}
	secret := hook.secret
	if invalid {
		secret = "invalid"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	client := &http.Client{Transport: s.transport.base, Timeout: 10 * time.Second, CheckRedirect: denyRedirect}
	res, err := client.Do(req)
	if err != nil {
		return 0, errors.New("telegram webhook failed")
	}
	defer res.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(res.Body, 65536))
	s.meta.records.record(req, "telegram_webhook", payload, res.StatusCode, reply, started, "")
	return res.StatusCode, nil
}

// clearTelegramWebhook forgets a bot's webhook, as when it was set elsewhere.
func (t *fakeTelegram) clearWebhook(bot string) {
	t.webhookMethod(bot, "deleteWebhook", map[string]any{})
}
