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

// TelegramUsername checks the stored bot token and returns the bot's username.
func (r *Registry) TelegramUsername(ctx context.Context, account string) (string, error) {
	client := r.snapshot().telegram[account]
	if client == nil {
		return "", errors.New("Telegram account is not active")
	}
	return client.Username(ctx)
}

func (r *Registry) TestEmailConnection(ctx context.Context, account string) (EmailConnection, error) {
	client := r.snapshot().email[account]
	if client == nil {
		return EmailConnection{}, errors.New("Email account is not active")
	}
	return TestEmail(ctx, client), nil
}

// DeleteTelegramWebhook removes the webhook of an active bot, before a reload drops its token.
func (r *Registry) DeleteTelegramWebhook(ctx context.Context, account string) error {
	client := r.snapshot().telegram[account]
	if client == nil {
		return errors.New("Telegram account is not active")
	}
	return client.DeleteWebhook(ctx)
}
