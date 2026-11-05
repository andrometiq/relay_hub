package channels

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

// These checks are shared by the Hub admin pages and the site tools, so a bot
// or mailbox is checked the same way whoever sets it up.

var mailHostname = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// ValidateEmailSettings checks the shape of mailbox settings before they are tried or stored.
func ValidateEmailSettings(config map[string]any, password string) error {
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

// EmailConnection is the result of an SMTP and IMAP sign-in test.
type EmailConnection struct {
	SMTP string `json:"smtp"`
	IMAP string `json:"imap"`
}

// Status is "connected" when SMTP and a configured IMAP mailbox both signed in, else "failed".
func (m EmailConnection) Status() string {
	if m.SMTP != "connected" || m.IMAP == "failed" {
		return "failed"
	}
	return "connected"
}

// Failure is the safe text of a failed test.
func (m EmailConnection) Failure() string {
	if m.SMTP != "connected" {
		return "The Hub could not sign in to the SMTP server. Check the host, port, username and password."
	}
	return "The Hub could not sign in to the IMAP server. Check the IMAP host, port, folder, username and password."
}

// TestEmail signs in to SMTP and, when it is configured, to the IMAP folder. No mail is sent or read.
func TestEmail(ctx context.Context, client *relayemail.Client) EmailConnection {
	result := EmailConnection{SMTP: "connected", IMAP: "not_configured"}
	if client.TestSMTP(ctx) != nil {
		result.SMTP = "failed"
	}
	if client.MailboxConfigured() {
		result.IMAP = "connected"
		if client.TestMailbox(ctx) != nil {
			result.IMAP = "failed"
		}
	}
	return result
}

// TelegramChecker reads a bot that is active in the runtime.
type TelegramChecker interface {
	TelegramUsername(context.Context, string) (string, error)
	TelegramConnection(context.Context, string, string, string, bool) (telegram.WebhookHealth, error)
	WebhookPath(provider, key string) string
}

// TelegramCheck is what a Telegram check shows: the bot name and the webhook state.
type TelegramCheck struct {
	Username string `json:"username"`
	telegram.WebhookHealth
	// WebhookStatus is registered, not_registered or failed.
	WebhookStatus string `json:"webhook_status"`
	// WebhookMissing says why the webhook was not checked, for a Hub without a public HTTPS URL.
	WebhookMissing string `json:"webhook_missing,omitempty"`
}

// ErrTelegramToken means Telegram refused the bot token in getMe.
var ErrTelegramToken = errors.New("Telegram did not accept the bot token")

// CheckTelegram reads the bot name with getMe, which also checks the token, then checks or registers the
// webhook. Telegram sends webhooks only to HTTPS, so a Hub without a public HTTPS URL stops after the token.
func CheckTelegram(ctx context.Context, bots TelegramChecker, key, publicURL, master string, repair bool) (TelegramCheck, error) {
	username, err := bots.TelegramUsername(ctx, key)
	if err != nil {
		return TelegramCheck{WebhookStatus: "failed"}, ErrTelegramToken
	}
	result := TelegramCheck{Username: username}
	address, ok := TelegramWebhookURL(publicURL, bots.WebhookPath("Telegram", key))
	if !ok {
		result.WebhookStatus = "not_registered"
		result.WebhookMissing = "The Hub has no public HTTPS URL, so Telegram cannot send messages to it yet."
		return result, nil
	}
	result.WebhookHealth, err = bots.TelegramConnection(ctx, key, address, master, repair)
	result.WebhookStatus = telegramStatus(result.WebhookHealth, err)
	return result, err
}

// TelegramWebhookURL is the public webhook address of a bot, when the Hub has a public HTTPS URL.
func TelegramWebhookURL(publicURL, path string) (string, bool) {
	address := strings.TrimRight(publicURL, "/") + "/webhooks/telegram/" + path
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return address, true
}

func telegramStatus(health telegram.WebhookHealth, err error) string {
	switch {
	case err != nil:
		return "failed"
	case health.Registered && health.Matches:
		return "registered"
	default:
		return "not_registered"
	}
}
