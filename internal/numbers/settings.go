package numbers

import "context"

// SettingsInput changes the switches of one number. A nil field keeps its value.
type SettingsInput struct {
	Channel      string `json:"channel,omitempty"`
	Enabled      *bool  `json:"enabled,omitempty"`
	Templates    *bool  `json:"templates,omitempty"`
	SyncMetadata *bool  `json:"sync_metadata,omitempty"`
}

var errNoChange = &Failure{"invalid_request", "Choose a setting to change."}

// SaveSettings turns a number on or off and changes its template and hourly
// Meta sync switches. Only public settings change; a number Meta removed must
// be connected again before it can be turned on.
func (s *Service) SaveSettings(ctx context.Context, site string, in SettingsInput) (Health, error) {
	if in.Enabled == nil && in.Templates == nil && in.SyncMetadata == nil {
		return Health{}, errNoChange
	}
	c, err := s.channel(ctx, site, in.Channel)
	if err != nil {
		return Health{}, err
	}
	values := map[string]any{}
	if in.Templates != nil {
		values["templates"] = *in.Templates
	}
	if in.SyncMetadata != nil {
		values["sync_metadata"] = *in.SyncMetadata
	}
	turnedOn := in.Enabled != nil && *in.Enabled && !c.Enabled
	if turnedOn {
		if _, err = s.account(ctx, c); err != nil {
			return Health{}, err
		}
		values["connection_status"] = "unchecked"
	} else if in.Enabled != nil && !*in.Enabled {
		values["connection_status"] = "disabled"
	}
	if err = s.Store.UpdateSiteNumber(ctx, site, c.Key, in.Enabled, values); err != nil {
		return Health{}, errUnavailable
	}
	if err = s.Reload(ctx); err != nil {
		return Health{}, errUnavailable
	}
	if waba, _ := c.Config["waba_id"].(string); in.Templates != nil && *in.Templates {
		_ = s.WhatsApp.Metadata.MarkTemplatesStale(ctx, waba)
	}
	if turnedOn {
		if checked, err := s.Check(ctx, site, c.Key, false); err == nil {
			return checked.Health, nil
		}
	}
	return s.Health(ctx, site, c.Key)
}
