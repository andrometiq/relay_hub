package channels

import (
	"context"
	"errors"

	"github.com/andrometiq/relay_hub/internal/telegram"
)

func (r *Registry) TelegramConnection(ctx context.Context, account, address, master string, repair bool) (telegram.WebhookHealth, error) {
	client := r.snapshot().telegram[account]
	if client == nil {
		return telegram.WebhookHealth{}, errors.New("Telegram account is not active")
	}
	if repair {
		secret, err := r.TelegramWebhookSecret(account, master)
		if err != nil {
			return telegram.WebhookHealth{}, err
		}
		if err = client.RegisterWebhook(ctx, address, secret); err != nil {
			return telegram.WebhookHealth{}, err
		}
	}
	return client.WebhookHealth(ctx, address)
}

type EmailConnection struct {
	SMTP string `json:"smtp"`
	IMAP string `json:"imap"`
}

func (r *Registry) TestEmailConnection(ctx context.Context, account string) (EmailConnection, error) {
	client := r.snapshot().email[account]
	if client == nil {
		return EmailConnection{}, errors.New("Email account is not active")
	}
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
	return result, nil
}
