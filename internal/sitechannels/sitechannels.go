// Package sitechannels holds what every site channel tool shares: the coded
// failure Core maps to its own text, the runner that scopes a call to the
// caller's site, and the lookup of a channel key that belongs to that site.
// The provider tools live in this package (Telegram bots, mailboxes) and in
// numbers (WhatsApp numbers).
package sitechannels

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SiteOf returns the site of the MCP caller's token.
type SiteOf func(context.Context) (string, bool)

// Failure is a coded tool error that Core maps to its own text.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

// ErrUnavailable is the failure of an unexpected error; its cause is never shown.
var ErrUnavailable = &Failure{"hub_unavailable", "Relay Hub could not finish this. Try again in a few minutes."}

// Missing are the failures of a key lookup, in the provider's own words.
type Missing struct {
	// NotFound is a key that is not a channel of the site.
	NotFound *Failure
	// None is a site without a channel of the provider.
	None *Failure
	// Choose is a call without a key on a site with several channels.
	Choose *Failure
}

// Key resolves the channel of one call: the named key when it is a channel of
// provider on site, else the site's only channel of provider. The database
// decides ownership, so a key of another site is never trusted.
func Key(ctx context.Context, db *pgxpool.Pool, provider, site, key string, missing Missing) (string, error) {
	rows, err := db.Query(ctx, `SELECT channel_key FROM relay_channel_credentials WHERE channel=$1 AND site_id=$2 ORDER BY channel_key LIMIT 500`, provider, site)
	if err != nil {
		return "", ErrUnavailable
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", ErrUnavailable
	}
	if key != "" {
		for _, own := range keys {
			if own == key {
				return key, nil
			}
		}
		return "", missing.NotFound
	}
	switch len(keys) {
	case 0:
		return "", missing.None
	case 1:
		return keys[0], nil
	}
	return "", missing.Choose
}

// Channel reads one channel of provider that belongs to site.
func Channel(ctx context.Context, store *containers.Store, provider, site, key string, missing Missing) (containers.Channel, error) {
	key, err := Key(ctx, store.DB, provider, site, key, missing)
	if err != nil {
		return containers.Channel{}, err
	}
	c, err := store.GetChannel(ctx, containers.ChannelRef{Channel: provider, Key: key})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.Site != site) {
		return containers.Channel{}, missing.NotFound
	}
	if err != nil {
		return containers.Channel{}, ErrUnavailable
	}
	return c, nil
}

// Run calls one tool for the caller's site and returns its result as an object or a coded failure.
func Run(ctx context.Context, siteOf SiteOf, call func(site string) (any, error)) (*mcp.CallToolResult, map[string]any, error) {
	site, ok := siteOf(ctx)
	if !ok {
		return nil, nil, errors.New("site credential required")
	}
	value, err := call(site)
	if err != nil {
		var failure *Failure
		if !errors.As(err, &failure) {
			failure = ErrUnavailable
		}
		result := &mcp.CallToolResult{}
		result.SetError(failure)
		return result, map[string]any{"error": failure}, nil
	}
	var out map[string]any
	raw, _ := json.Marshal(value)
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}
