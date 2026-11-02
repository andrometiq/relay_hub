// Package runtime builds the in-memory WhatsApp runtime from stored containers,
// channels and sites. The Hub and the simulator publish it the same way.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

// Source lists every enabled channel with its container app and site.
type Source interface {
	Runtime(context.Context) ([]containers.RuntimeCredential, error)
}

// AppSource lists the Meta apps of enabled containers.
type AppSource interface {
	WhatsAppApps(context.Context) ([]containers.WhatsAppApp, error)
}

// WhatsAppAccounts returns the WhatsApp numbers of the stored runtime.
func WhatsAppAccounts(ctx context.Context, store Source) ([]whatsapp.Account, error) {
	rows, err := store.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	accounts := make([]whatsapp.Account, 0)
	for _, row := range rows {
		if row.Channel != "WhatsApp" {
			continue
		}
		account, err := WhatsAppAccount(row)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

// WhatsAppAccount joins one number with its container app and its site.
// A number of a disabled site is kept so its webhooks are still journaled.
func WhatsAppAccount(row containers.RuntimeCredential) (whatsapp.Account, error) {
	get := func(key string) string { value, _ := row.Config[key].(string); return value }
	rate, err := Rate(row.Config["messages_per_second"])
	if err != nil {
		return whatsapp.Account{}, err
	}
	account := whatsapp.Account{MessagesPerSecond: rate, ID: row.Key, ContainerID: row.Container, Label: row.Label,
		WebhookID:   row.WebhookID,
		AccessToken: row.Secret, DisplayNumber: get("display_number"),
		AppID: get("app_id"), PhoneID: get("phone_id"), WABAID: get("waba_id"), Version: get("graph_version"),
		AppSecret: get("app_secret"), VerifyToken: get("verify_token"), CoreToken: get("core_token"),
		SyncMetadata: boolValue(row.Config["sync_metadata"]), Templates: boolValue(row.Config["templates"]),
	}
	if row.Site != "" {
		account.SiteID, account.Site = row.Site, row.Origin
		if row.SiteEnabled {
			account.CoreAPIKey, account.CoreAPISecret, account.CallbackSecret = row.Core.APIKey, row.Core.APISecret, row.Core.CallbackSecret
		}
	}
	return account, nil
}

// WhatsAppApps returns the webhook identities of enabled containers, so a
// container without numbers can still verify and receive webhooks.
func WhatsAppApps(ctx context.Context, store AppSource) ([]whatsapp.App, error) {
	rows, err := store.WhatsAppApps(ctx)
	if err != nil {
		return nil, err
	}
	apps := make([]whatsapp.App, len(rows))
	for i, row := range rows {
		apps[i] = whatsapp.App{ContainerID: row.Container, WebhookID: row.WebhookID, AppSecret: row.AppSecret, VerifyToken: row.VerifyToken}
	}
	return apps, nil
}

// ActivateWhatsApp publishes the stored numbers and container apps to wa.
func ActivateWhatsApp(ctx context.Context, wa *whatsapp.Service, store interface {
	Source
	AppSource
}) error {
	accounts, err := WhatsAppAccounts(ctx, store)
	if err != nil {
		return err
	}
	apps, err := WhatsAppApps(ctx, store)
	if err != nil {
		return err
	}
	if err = wa.ReplaceAccounts(accounts); err != nil {
		return err
	}
	wa.ReplaceApps(apps)
	return nil
}

// Rate reads a number's messages_per_second: an integer from 0 to 1000, 0 when unset.
func Rate(value any) (int, error) {
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

func boolValue(value any) bool {
	v, ok := value.(bool)
	return ok && v
}
