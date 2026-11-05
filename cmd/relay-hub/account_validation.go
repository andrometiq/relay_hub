package main

import (
	"context"
	"errors"
	"strings"

	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	relayruntime "github.com/andrometiq/relay_hub/internal/runtime"
	"github.com/jackc/pgx/v5"
)

type credentialReader interface {
	Credential(context.Context, string, string) (containers.RuntimeCredential, error)
}

// validateChannelAccount checks the Telegram and email settings of an account
// save. The store checks that the site's Core credentials are complete.
func validateChannelAccount(ctx context.Context, store credentialReader, account containers.Account, secrets map[string]string) error {
	if !account.Enabled {
		return nil
	}
	if err := sharedCoreCredentials(account); err != nil {
		return err
	}
	for _, name := range []string{"Telegram", "Email"} {
		saved, err := store.Credential(ctx, name, account.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return errors.New("saved channel credentials unavailable")
		}
		// Save leaves omitted channels intact, so include them in validation.
		channel := containers.AccountChannel{Channel: name, Enabled: saved.Enabled, Config: saved.Config}
		for _, incoming := range account.Channels {
			if incoming.Channel == name {
				channel = incoming
				break
			}
		}
		if !channel.Enabled {
			continue
		}
		candidate := containers.RuntimeCredential{Key: account.ID, Channel: channel.Channel, Enabled: true, Config: map[string]any{}, Secret: saved.Secret}
		if secrets[channel.Channel] != "" {
			candidate.Secret = secrets[channel.Channel]
		}
		for key, value := range channel.Config {
			candidate.Config[key] = value
		}
		if candidate.Config["imap_password"] == nil || candidate.Config["imap_password"] == "" {
			candidate.Config["imap_password"] = saved.Config["imap_password"]
		}
		if value, _ := candidate.Config["callback_secret"].(string); value != "" && len(value) < 32 {
			return errors.New("callback signing secret must have at least 32 characters")
		}
		if channel.Channel == "Email" {
			if err := channels.ValidateEmailSettings(candidate.Config, candidate.Secret); err != nil {
				return err
			}
		}
		if _, err := channels.Load([]containers.RuntimeCredential{candidate}); err != nil {
			return errors.New(channel.Channel + " settings are invalid; check required fields and credentials")
		}
	}
	return nil
}

// sharedCoreCredentials refuses two channels that carry different Core credentials,
// because every channel of a site uses the site's one set.
func sharedCoreCredentials(account containers.Account) error {
	shared := map[string]string{}
	for _, channel := range account.Channels {
		for _, key := range []string{"core_api_key", "core_api_secret", "callback_secret"} {
			value, _ := channel.Config[key].(string)
			if value == "" {
				continue
			}
			if shared[key] != "" && shared[key] != value {
				return errors.New("Telegram and email share Core delivery; use the same three Core credentials for both channels")
			}
			shared[key] = value
		}
	}
	return nil
}

func validateWhatsAppConfig(config map[string]any) error {
	for _, key := range []string{"phone_id", "waba_id", "app_id"} {
		value, ok := config[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return errors.New(key + " is required")
		}
	}
	_, err := relayruntime.Rate(config["messages_per_second"])
	return err
}
