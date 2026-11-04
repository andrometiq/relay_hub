package whatsapp

import (
	"context"
	"net/http"
	"net/url"
)

const numberStatusFields = "id,display_phone_number,verified_name,name_status,status,quality_rating"

// NumberStatus reads a number from Meta with its own token. It changes nothing.
func (s *Service) NumberStatus(ctx context.Context, a Account) (map[string]any, error) {
	return s.graph(ctx, a, http.MethodGet, a.PhoneID, url.Values{"fields": {numberStatusFields}}, nil)
}

// WebhookSubscription reports whether the number's Meta app gets the webhooks
// of its WhatsApp Business Account. With repair it subscribes the app again.
func (s *Service) WebhookSubscription(ctx context.Context, a Account, repair bool) (bool, error) {
	result, err := s.graph(ctx, a, http.MethodGet, a.WABAID+"/subscribed_apps", nil, nil)
	if err != nil {
		return false, err
	}
	rows, _ := result["data"].([]any)
	for _, row := range rows {
		app, _ := row.(map[string]any)
		data, _ := app["whatsapp_business_api_data"].(map[string]any)
		if a.AppID != "" && stringValue(data["id"]) == a.AppID {
			return true, nil
		}
	}
	if !repair {
		return false, nil
	}
	if _, err = s.graph(ctx, a, http.MethodPost, a.WABAID+"/subscribed_apps", nil, nil); err != nil {
		return false, err
	}
	return true, nil
}

// SetTwoStepPIN sets the number's two-step verification PIN at Meta.
func (s *Service) SetTwoStepPIN(ctx context.Context, a Account, pin string) error {
	_, err := s.graph(ctx, a, http.MethodPost, a.PhoneID, nil, map[string]string{"pin": pin})
	return err
}
