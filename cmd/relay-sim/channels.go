package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func (s *simulation) activateChannels(a whatsapp.Account) error {
	bot, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: a.ID, BotToken: simulationBotToken, Enabled: true}, s.channelHTTP())
	if err != nil {
		return err
	}
	mail, err := s.email.client(a.ID, "inbox@sim.invalid", true)
	if err != nil {
		return err
	}
	if err = s.registry.Replace(&channels.Registry{Telegram: map[string]*telegram.Client{a.ID: bot}, Email: map[string]*relayemail.Client{a.ID: mail}}); err != nil {
		return err
	}
	s.callbacks.Update([]channels.RuntimeCallback{{Account: a.ID, Site: a.Site, APIKey: a.CoreAPIKey, APISecret: a.CoreAPISecret, CallbackSecret: a.CallbackSecret, HTTPClient: s.channelHTTP()}})
	return nil
}
func (s *simulation) channelHTTP() *http.Client {
	return &http.Client{Transport: s.transport, Timeout: 12 * time.Second, CheckRedirect: denyRedirect}
}
func simulationChannels(a whatsapp.Account) []containers.AccountChannel {
	callback := func() map[string]any {
		return map[string]any{"core_api_key": a.CoreAPIKey, "core_api_secret": a.CoreAPISecret, "callback_secret": a.CallbackSecret}
	}
	wa := callback()
	wa["phone_id"] = a.PhoneID
	wa["waba_id"] = a.WABAID
	wa["graph_version"] = a.Version
	wa["templates"] = true
	wa["app_secret"] = a.AppSecret
	mail := callback()
	mail["host"] = "127.0.0.1"
	mail["port"] = 587
	mail["username"] = simulationMailUser
	mail["from"] = "inbox@sim.invalid"
	mail["imap_host"] = "127.0.0.1"
	mail["imap_port"] = 993
	return []containers.AccountChannel{{Channel: "WhatsApp", Enabled: true, Config: wa}, {Channel: "Telegram", Enabled: true, Config: callback()}, {Channel: "Email", Enabled: true, Config: mail}}
}
func (s *simulation) channelWebhook(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		store := channels.Store{DB: s.db, Wake: s.channelWake}
		provider := map[string]string{"telegram": "Telegram", "email": "Email"}[channel]
		key, known := s.registry.WebhookKey(provider, r.PathValue("account"))
		if !known {
			http.NotFound(w, r)
			return
		}
		if channel == "telegram" {
			err = s.registry.TelegramWebhook(r.Context(), store, key, s.token, []byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), raw)
		} else {
			err = s.registry.EmailWebhook(r.Context(), store, key, s.token, []byte(r.Header.Get("X-Relay-Email-Secret")), raw)
		}
		if err != nil {
			http.Error(w, "ingress rejected", 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

// emitChannel posts a provider webhook for one bot or mailbox (key) to its public path.
func (s *simulation) emitChannel(ctx context.Context, channel, key string, payload []byte, invalid bool) (int, error) {
	if key == "" {
		key = "sim-v1"
	}
	secret, err := s.registry.TelegramWebhookSecret(key, s.token)
	header, provider := "X-Telegram-Bot-Api-Secret-Token", "Telegram"
	if channel == "email" {
		secret, err = s.registry.EmailWebhookSecret(key, s.token)
		header, provider = "X-Relay-Email-Secret", "Email"
	}
	if err != nil {
		return 0, err
	}
	if invalid {
		secret = "invalid"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.hub.String()+"/webhooks/"+channel+"/"+s.registry.WebhookPath(provider, key), bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set(header, secret)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	client := &http.Client{Transport: s.transport.base, Timeout: 10 * time.Second, CheckRedirect: denyRedirect}
	res, err := client.Do(req)
	if err != nil {
		return 0, errors.New("channel webhook failed")
	}
	defer res.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(res.Body, 65536))
	s.meta.records.record(req, channel+"_webhook", payload, res.StatusCode, reply, started, "")
	return res.StatusCode, nil
}
func (s *simulation) providerControl(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/_sim/providers" {
		writeJSON(w, map[string]any{"telegram": s.telegram.state.snapshot(), "email": s.email.state.snapshot(), "records": s.meta.records.snapshot()})
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Payload        json.RawMessage `json:"payload"`
		Faults         []string        `json:"faults"`
		Stage          string          `json:"stage"`
		Raw            string          `json:"raw"`
		UID            uint32          `json:"uid"`
		AuthservID     string          `json:"authserv_id"`
		Authentication string          `json:"authentication"`
		MessageID      string          `json:"message_id"`
		Recipient      string          `json:"recipient"`
		ID             string          `json:"id"`
		Webhook        bool            `json:"webhook"`
		Invalid        bool            `json:"invalid_signature"`
		Key            string          `json:"key"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 9<<20)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	var err error
	result := map[string]any{"ok": true}
	switch r.URL.Path {
	case "/_sim/telegram/faults":
		err = s.telegram.faults(in.Faults)
	case "/_sim/telegram/emit":
		err = s.telegram.queue(in.Payload)
		if err == nil {
			result["status"], err = s.emitChannel(r.Context(), "telegram", in.Key, in.Payload, in.Invalid)
		}
	case "/_sim/telegram/queue":
		err = s.telegram.queue(in.Payload)
	case "/_sim/email/faults":
		err = s.email.faults(in.Stage, in.Faults)
	case "/_sim/email/emit", "/_sim/email/bounce":
		raw := in.Raw
		if strings.HasSuffix(r.URL.Path, "/bounce") {
			raw, err = bounceMail(in.MessageID, in.Recipient)
		} else if in.Authentication != "" {
			server := in.AuthservID
			if server == "" {
				server = simulationAuthserv
			}
			raw, err = authenticatedMail(raw, server, in.Authentication)
		}
		if err != nil {
			break
		}
		if in.Webhook {
			if in.ID == "" {
				err = errors.New("webhook event id required")
				break
			}
			payload, _ := json.Marshal(map[string]any{"id": in.ID, "raw": raw})
			result["status"], err = s.emitChannel(r.Context(), "email", in.Key, payload, in.Invalid)
		} else {
			result["uid"], err = s.email.appendMail(raw, in.UID)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "provider control rejected", 400)
		return
	}
	writeJSON(w, result)
}
