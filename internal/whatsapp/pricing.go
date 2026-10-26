package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var pricingDimensions = map[string]bool{"PRICING_CATEGORY": true, "PRICING_TYPE": true, "COUNTRY": true, "PHONE": true}

// pricing reads Meta's per-message pricing analytics for the account's WABA
// and returns the provider data points unchanged.
func (s *Service) pricing(ctx context.Context, a Account, in Input, validateOnly bool) (map[string]any, error) {
	field, err := pricingField(in)
	if err != nil {
		return nil, err
	}
	if validateOnly {
		return nil, nil
	}
	result, err := s.graph(ctx, a, http.MethodGet, a.WABAID, url.Values{"fields": {field}}, nil)
	if err != nil {
		return nil, err
	}
	points := []any{}
	analytics, _ := result["pricing_analytics"].(map[string]any)
	rows, _ := analytics["data"].([]any)
	for _, row := range rows {
		item, _ := row.(map[string]any)
		values, _ := item["data_points"].([]any)
		points = append(points, values...)
	}
	return map[string]any{"data_points": points}, nil
}

func pricingField(in Input) (string, error) {
	if in.Start <= 0 || in.End <= in.Start {
		return "", errors.New("start and end must be UNIX seconds with end after start")
	}
	if in.Granularity != "DAILY" && in.Granularity != "MONTHLY" {
		return "", errors.New("granularity must be DAILY or MONTHLY")
	}
	field := "pricing_analytics.start(" + strconv.FormatInt(in.Start, 10) + ").end(" + strconv.FormatInt(in.End, 10) + ").granularity(" + in.Granularity + ")"
	if len(in.Dimensions) == 0 {
		return field, nil
	}
	seen := map[string]bool{}
	quoted := make([]string, 0, len(in.Dimensions))
	for _, dimension := range in.Dimensions {
		if !pricingDimensions[dimension] || seen[dimension] {
			return "", errors.New("dimensions must be distinct PRICING_CATEGORY, PRICING_TYPE, COUNTRY or PHONE")
		}
		seen[dimension] = true
		quoted = append(quoted, `"`+dimension+`"`)
	}
	return field + ".dimensions([" + strings.Join(quoted, ",") + "])", nil
}
