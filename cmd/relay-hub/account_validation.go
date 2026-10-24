package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/andrometiq/relay_hub/internal/accounts"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/jackc/pgx/v5"
)

type credentialReader interface {
	Credential(context.Context, string, string) (accounts.RuntimeCredential, error)
}

func validateChannelAccount(ctx context.Context, store credentialReader, account accounts.Account, secrets map[string]string) error {
	if !account.Enabled {
		return nil
	}
	var sharedCallbacks [3]string
	haveCallbacks, hadCallbacks, active := false, false, false
	for _, name := range []string{"Telegram", "Email"} {
		saved, err := store.Credential(ctx, account.ID, name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return errors.New("saved channel credentials unavailable")
		}
		if value, _ := saved.Config["callback_secret"].(string); value != "" {
			hadCallbacks = true
		}
		// Save leaves omitted channels intact, so include them in validation.
		channel := accounts.Channel{Channel: name, Enabled: saved.Enabled, Config: saved.Config}
		for _, incoming := range account.Channels {
			if incoming.Channel == name {
				channel = incoming
				break
			}
		}
		if !channel.Enabled {
			continue
		}
		active = true
		candidate := accounts.RuntimeCredential{AccountID: account.ID, Channel: channel.Channel, Enabled: true, Config: map[string]any{}, Secret: saved.Secret}
		if secrets[channel.Channel] != "" {
			candidate.Secret = secrets[channel.Channel]
		}
		for key, value := range channel.Config {
			candidate.Config[key] = value
		}
		for _, key := range []string{"imap_password", "core_api_key", "core_api_secret", "callback_secret", "core_token"} {
			if candidate.Config[key] == nil || candidate.Config[key] == "" {
				candidate.Config[key] = saved.Config[key]
			}
		}
		present := func(key string) bool { v, _ := candidate.Config[key].(string); return strings.TrimSpace(v) != "" }
		count := 0
		for _, key := range []string{"core_api_key", "core_api_secret", "callback_secret"} {
			if present(key) {
				count++
			}
		}
		if count != 0 && count != 3 {
			return errors.New("complete all three Core delivery credentials")
		}
		if count == 3 {
			var current [3]string
			for i, key := range []string{"core_api_key", "core_api_secret", "callback_secret"} {
				current[i], _ = candidate.Config[key].(string)
			}
			if haveCallbacks && current != sharedCallbacks {
				return errors.New("Telegram and email share Core delivery; use the same three Core credentials for both channels")
			}
			sharedCallbacks, haveCallbacks = current, true
		}
		if value, _ := candidate.Config["callback_secret"].(string); value != "" && len(value) < 32 {
			return errors.New("callback signing secret must have at least 32 characters")
		}
		if channel.Channel == "Email" {
			if err := validateEmailSettings(candidate.Config, candidate.Secret); err != nil {
				return err
			}
		}
		if _, err := channels.Load([]accounts.RuntimeCredential{candidate}); err != nil {
			return errors.New(channel.Channel + " settings are invalid; check required fields and credentials")
		}
	}
	if active && hadCallbacks && !haveCallbacks {
		return errors.New("Core delivery credentials are on a disabled channel; copy all three Core delivery credentials to an enabled Telegram or email channel before disabling it")
	}
	return nil
}

var mailHostname = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)

func validateEmailSettings(config map[string]any, password string) error {
	for _, key := range []string{"host", "username", "from", "imap_host", "imap_username", "imap_folder", "trusted_authserv_id", "trusted_dsn_domain"} {
		if value, exists := config[key]; exists {
			if _, ok := value.(string); !ok {
				return errors.New("email settings must use text values")
			}
		}
	}
	text := func(key string) string { value, _ := config[key].(string); return value }
	for _, key := range []string{"host", "imap_host", "trusted_authserv_id", "trusted_dsn_domain"} {
		value := text(key)
		if (key == "host" && value == "") || (value != "" && !mailHostname.MatchString(value) && net.ParseIP(value) == nil) {
			return errors.New("invalid email hostname or trusted domain")
		}
	}
	for _, key := range []string{"port", "imap_port"} {
		if key == "imap_port" && (text("imap_host") == "" || config[key] == nil || config[key] == "") {
			continue
		}
		port, err := strconv.Atoi(fmt.Sprint(config[key]))
		if err != nil || port < 1 || port > 65535 {
			return errors.New("email ports must be integers from 1 to 65535")
		}
	}
	if text("username") != "" && password == "" {
		return errors.New("SMTP password is required for authentication")
	}
	if text("imap_host") != "" && ((text("imap_username") == "" && text("username") == "") || (text("imap_password") == "" && password == "")) {
		return errors.New("IMAP username and password are required; SMTP credentials may be reused")
	}
	return nil
}

func whatsappRate(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	switch value.(type) {
	case int, float64:
	default:
		return 0, errors.New("messages_per_second must be an integer from 0 to 1000")
	}
	rate, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil || rate < 0 || rate > 1000 {
		return 0, errors.New("messages_per_second must be an integer from 0 to 1000")
	}
	return rate, nil
}

func validateWhatsAppConfig(config map[string]any) error {
	for _, key := range []string{"phone_id", "waba_id", "app_id"} {
		value, ok := config[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return errors.New(key + " is required")
		}
	}
	_, err := whatsappRate(config["messages_per_second"])
	return err
}
