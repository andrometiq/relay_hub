package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
)

// WebhookHealth deliberately excludes the provider URL and error description:
// both can contain credentials or other untrusted provider text.
type WebhookHealth struct {
	Registered  bool  `json:"registered"`
	Matches     bool  `json:"matches"`
	Pending     int   `json:"pending_updates"`
	LastErrorAt int64 `json:"last_error_at"`
}

func (c *Client) WebhookHealth(ctx context.Context, expected string) (WebhookHealth, error) {
	raw, err := c.call(ctx, "getWebhookInfo", map[string]any{})
	if err != nil {
		return WebhookHealth{}, err
	}
	var info struct {
		URL         string `json:"url"`
		Pending     int    `json:"pending_update_count"`
		LastErrorAt int64  `json:"last_error_date"`
	}
	if json.Unmarshal(raw, &info) != nil {
		return WebhookHealth{}, errors.New("invalid webhook response")
	}
	return WebhookHealth{Registered: info.URL != "", Matches: info.URL == expected, Pending: info.Pending, LastErrorAt: info.LastErrorAt}, nil
}

// Username checks the bot token with getMe and returns the bot's username.
func (c *Client) Username(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "getMe", map[string]any{})
	if err != nil {
		return "", err
	}
	var bot struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(raw, &bot) != nil || !botUsername.MatchString(bot.Username) {
		return "", errors.New("invalid getMe response")
	}
	return bot.Username, nil
}

var botUsername = regexp.MustCompile(`^[A-Za-z0-9_]{5,64}$`)

func (c *Client) RegisterWebhook(ctx context.Context, address, secret string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || secret == "" {
		return errors.New("invalid webhook configuration")
	}
	raw, err := c.call(ctx, "setWebhook", map[string]any{"url": address, "secret_token": secret, "max_connections": 1, "drop_pending_updates": false, "allowed_updates": supportedUpdates})
	if err != nil {
		return err
	}
	if string(raw) != "true" {
		return errors.New("webhook registration was not confirmed")
	}
	return nil
}
