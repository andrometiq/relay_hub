package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

var pricingDimensions = map[string]bool{"PRICING_CATEGORY": true, "PRICING_TYPE": true, "COUNTRY": true, "PHONE": true}

// pricing reads Meta's per-message pricing analytics for the account's WABA
// and returns the provider data points unchanged, with the WABA currency
// because the data points carry none.
func (s *Service) pricing(ctx context.Context, a Account, in Input, validateOnly bool) (map[string]any, error) {
	field, err := pricingField(in)
	if err != nil {
		return nil, err
	}
	byPhone := slices.Contains(in.Dimensions, "PHONE")
	numbers, shared := s.wabaNumbers(a)
	if shared && !byPhone {
		return nil, errors.New("this WhatsApp Business Account is shared with another site; add the PHONE dimension")
	}
	if validateOnly {
		return nil, nil
	}
	result, err := s.graph(ctx, a, http.MethodGet, a.WABAID, url.Values{"fields": {"currency," + field}}, nil)
	if err != nil {
		return nil, err
	}
	points := []any{}
	analytics, _ := result["pricing_analytics"].(map[string]any)
	rows, _ := analytics["data"].([]any)
	for _, row := range rows {
		item, _ := row.(map[string]any)
		values, _ := item["data_points"].([]any)
		for _, value := range values {
			if point, ok := value.(map[string]any); ok && byPhone {
				if !ownPoint(point, a, numbers, shared) {
					continue
				}
			}
			points = append(points, value)
		}
	}
	currency, _ := result["currency"].(string)
	return map[string]any{"data_points": points, "currency": currency}, nil
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

// wabaNumbers lists the numbers that share the WABA of a, and whether a number
// of another site is among them.
func (s *Service) wabaNumbers(a Account) ([]Account, bool) {
	normalizeAccount(&a)
	numbers, shared := []Account{}, false
	for _, n := range s.AccountsSnapshot() {
		if n.WABAID != a.WABAID {
			continue
		}
		numbers = append(numbers, n)
		shared = shared || n.SiteID != a.SiteID
	}
	return numbers, shared
}

// ownPoint keeps a data point of a number of a's site and names its channel
// key. A point Meta did not split by number stays only when no other site
// shares the WABA; those charges are for the Hub admin.
func ownPoint(point map[string]any, a Account, numbers []Account, shared bool) bool {
	phone := phoneDigits(fmtValue(point["phone_number"]))
	if phone == "" {
		return !shared
	}
	for _, n := range numbers {
		if phoneDigits(n.DisplayNumber) == phone || n.PhoneID == phone {
			if n.SiteID != a.SiteID {
				return false
			}
			point["channel_key"] = n.ID
			return true
		}
	}
	return !shared
}

func phoneDigits(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, value)
}

func fmtValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return ""
}
