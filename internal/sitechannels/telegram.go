package sitechannels

import (
	"context"
	"errors"
	"strings"

	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

var (
	missingBot = Missing{
		NotFound: &Failure{"not_found", "This Telegram bot is not connected to this site."},
		None:     &Failure{"no_bot", "This site has no Telegram bot."},
		Choose:   &Failure{"channel_required", "This site has several Telegram bots; choose one."},
	}
	errInvalidToken        = &Failure{"invalid_token", "Telegram did not accept this bot token. Copy the token from @BotFather again."}
	errBotChanged          = &Failure{"bot_changed", "This token belongs to another bot. Connect that bot as a new bot instead."}
	errTelegramUnavailable = &Failure{"telegram_unavailable", "Telegram is not answering. Try again in a few minutes."}
)

// BotHealth is what a site may see about one of its bots. It never contains the token.
type BotHealth struct {
	Channel       string `json:"channel"`
	Label         string `json:"label"`
	Username      string `json:"username"`
	BotID         string `json:"bot_id"`
	Enabled       bool   `json:"enabled"`
	AdminOff      bool   `json:"admin_off"`
	WebhookStatus string `json:"webhook_status"`
	CheckedAt     string `json:"checked_at"`
	CheckError    string `json:"check_error"`
	TokenSaved    bool   `json:"token_saved"`
}

// BotCheck is a health with what the last check found.
type BotCheck struct {
	BotHealth
	WebhookMissing string `json:"webhook_missing,omitempty"`
	PendingUpdates int    `json:"pending_updates"`
}

// BotSettings changes one bot. Nil fields and a blank token keep their values.
type BotSettings struct {
	Channel  string  `json:"channel,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
	Label    *string `json:"label,omitempty"`
	BotToken string  `json:"bot_token,omitempty"`
}

// BotRemoval says what happened when a bot left the site.
type BotRemoval struct {
	Channel        string `json:"channel"`
	Removed        bool   `json:"removed"`
	WebhookDeleted bool   `json:"webhook_deleted"`
}

// botName checks a token with getMe and returns the bot's username.
func (s *Service) botName(ctx context.Context, token string) (string, error) {
	client, err := s.Clients.Telegram(strings.TrimSpace(token))
	if err != nil {
		return "", errInvalidToken
	}
	username, err := client.Username(ctx)
	var provider *telegram.APIError
	switch {
	case err == nil:
		return username, nil
	case errors.As(err, &provider) && !provider.OutcomeUnknown && (provider.ErrorCode == 401 || provider.ErrorCode == 404):
		return "", errInvalidToken
	}
	return "", errTelegramUnavailable
}

// ConnectBot checks the token with Telegram, stores the bot for site, links
// it, and registers its webhook. A token Telegram refuses stores nothing.
func (s *Service) ConnectBot(ctx context.Context, site, token, label string) (BotCheck, error) {
	ref := containers.ChannelRef{Channel: "Telegram"}
	check, err := s.connectBot(ctx, site, token, label, &ref)
	s.audit(ctx, site, ref, "telegram.bot.connect", err)
	return check, err
}

func (s *Service) connectBot(ctx context.Context, site, token, label string, ref *containers.ChannelRef) (BotCheck, error) {
	if err := s.allow(ctx, site, "Telegram", "telegram.bot.connect", connectLimit); err != nil {
		return BotCheck{}, err
	}
	username, err := s.botName(ctx, token)
	if err != nil {
		return BotCheck{}, err
	}
	if label = strings.TrimSpace(label); label == "" {
		label = "@" + username
	}
	bot := containers.Channel{Channel: "Telegram", Label: label, Config: map[string]any{"username": username, "webhook_status": "not_registered", "checked_at": s.now(), "check_error": ""}}
	if ref.Key, err = s.Store.SaveSiteChannel(ctx, site, bot, strings.TrimSpace(token)); err != nil {
		return BotCheck{}, stored(err)
	}
	s.reload(ctx, site, ref.Key)
	return s.checkBot(ctx, site, ref.Key, true)
}

// BotHealth reads the stored state of one bot. It never contacts Telegram.
func (s *Service) BotHealth(ctx context.Context, site, key string) (BotHealth, error) {
	c, err := Channel(ctx, s.Store, "Telegram", site, key, missingBot)
	if err != nil {
		return BotHealth{}, err
	}
	return botHealth(c), nil
}

func botHealth(c containers.Channel) BotHealth {
	return BotHealth{Channel: c.Key, Label: c.Label, Username: text(c.Config, "username"), BotID: c.Address, Enabled: c.Enabled, AdminOff: c.AdminOff,
		WebhookStatus: text(c.Config, "webhook_status"), CheckedAt: text(c.Config, "checked_at"), CheckError: text(c.Config, "check_error"),
		TokenSaved: saved(c, "secret")}
}

// CheckBot checks the token with getMe and reads the webhook; repair registers the webhook again.
func (s *Service) CheckBot(ctx context.Context, site, key string, repair bool) (BotCheck, error) {
	c, err := Channel(ctx, s.Store, "Telegram", site, key, missingBot)
	if err != nil {
		return BotCheck{}, err
	}
	if !c.Enabled {
		return BotCheck{}, errDisabled
	}
	if err = s.allow(ctx, site, "Telegram", "telegram.bot.check", checkLimit); err != nil {
		return BotCheck{}, err
	}
	return s.checkBot(ctx, site, c.Key, repair)
}

// checkBot runs the shared Telegram check on an active bot and keeps its result.
func (s *Service) checkBot(ctx context.Context, site, key string, repair bool) (BotCheck, error) {
	result, err := channels.CheckTelegram(ctx, s.Registry, key, s.PublicURL, s.TelegramMaster, repair)
	values := map[string]any{"webhook_status": result.WebhookStatus, "checked_at": s.now(), "check_error": result.WebhookMissing}
	switch {
	case errors.Is(err, channels.ErrTelegramToken):
		values["check_error"] = "Telegram did not accept the saved token. Save a new token from @BotFather."
	case err != nil:
		values["check_error"] = "Telegram did not confirm the webhook. Select Check connection to try again."
	}
	if result.Username != "" {
		values["username"] = result.Username
	}
	if err = s.Store.UpdateSiteValues(ctx, site, containers.ChannelRef{Channel: "Telegram", Key: key}, nil, values); err != nil {
		return BotCheck{}, ErrUnavailable
	}
	health, err := s.BotHealth(ctx, site, key)
	return BotCheck{BotHealth: health, WebhookMissing: result.WebhookMissing, PendingUpdates: result.Pending}, err
}

// SaveBotSettings renames a bot, turns it on or off, or stores a new token of the same bot.
func (s *Service) SaveBotSettings(ctx context.Context, site string, in BotSettings) (BotHealth, error) {
	if in.Enabled == nil && in.Label == nil && strings.TrimSpace(in.BotToken) == "" {
		return BotHealth{}, errNoChange
	}
	c, err := Channel(ctx, s.Store, "Telegram", site, in.Channel, missingBot)
	if err != nil {
		return BotHealth{}, err
	}
	ref := containers.ChannelRef{Channel: "Telegram", Key: c.Key}
	health, err := s.saveBotSettings(ctx, site, c, in)
	s.audit(ctx, site, ref, "telegram.bot.settings", err)
	return health, err
}

func (s *Service) saveBotSettings(ctx context.Context, site string, c containers.Channel, in BotSettings) (BotHealth, error) {
	token := strings.TrimSpace(in.BotToken)
	if token != "" {
		if err := s.allow(ctx, site, "Telegram", "telegram.bot.connect", connectLimit); err != nil {
			return BotHealth{}, err
		}
		username, err := s.botName(ctx, token)
		if err != nil {
			return BotHealth{}, err
		}
		if id, _, _ := strings.Cut(token, ":"); id != c.Address {
			return BotHealth{}, errBotChanged
		}
		c.Config["username"] = username
	}
	turnedOn := in.Enabled != nil && *in.Enabled && !c.Enabled
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Label != nil && strings.TrimSpace(*in.Label) != "" {
		c.Label = strings.TrimSpace(*in.Label)
	}
	if err := s.Store.UpdateSiteChannel(ctx, site, c, token); err != nil {
		return BotHealth{}, stored(err)
	}
	if err := s.Reload(ctx); err != nil {
		return BotHealth{}, ErrUnavailable
	}
	if c.Enabled && (turnedOn || token != "") {
		if checked, err := s.checkBot(ctx, site, c.Key, true); err == nil {
			return checked.BotHealth, nil
		}
	}
	return s.BotHealth(ctx, site, c.Key)
}

// RemoveBot takes a bot off the site after confirm. The Hub wipes its token
// and unlinks it first, then asks Telegram to stop sending its updates.
func (s *Service) RemoveBot(ctx context.Context, site, key string, confirm bool) (BotRemoval, error) {
	if !confirm {
		return BotRemoval{}, errConfirm
	}
	c, err := Channel(ctx, s.Store, "Telegram", site, key, missingBot)
	if err != nil {
		return BotRemoval{}, err
	}
	ref := containers.ChannelRef{Channel: "Telegram", Key: c.Key}
	err = stored(s.Store.RemoveSiteChannel(ctx, site, ref))
	s.audit(ctx, site, ref, "telegram.bot.remove", err)
	if err != nil {
		return BotRemoval{}, err
	}
	// The runtime still holds the token until the reload, which is the last chance to delete the webhook.
	removal := BotRemoval{Channel: c.Key, Removed: true, WebhookDeleted: s.Registry.DeleteTelegramWebhook(ctx, c.Key) == nil}
	s.reload(ctx, site, c.Key)
	return removal, nil
}
