package whatsapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeGraph(t *testing.T, handle func(r *http.Request) string) *Service {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Error("missing provider credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(handle(r)))
	}))
	t.Cleanup(server.Close)
	s, err := New([]Account{testAccount()})
	if err != nil {
		t.Fatal(err)
	}
	s.base = server.URL
	return s
}

func TestMessagesReadPayload(t *testing.T) {
	for _, typing := range []bool{false, true} {
		var body map[string]any
		s := fakeGraph(t, func(r *http.Request) string {
			if r.Method != "POST" || r.URL.Path != "/v25.0/111/messages" {
				t.Errorf("wrong provider request: %s %s", r.Method, r.URL.Path)
			}
			json.NewDecoder(r.Body).Decode(&body)
			return `{"success":true}`
		})
		result, err := s.Execute(context.Background(), testAccount(), Input{Operation: "messages.read", MessageID: "wamid.HBgM=", Typing: typing})
		if err != nil || result["success"] != true {
			t.Fatal(result, err)
		}
		if body["messaging_product"] != "whatsapp" || body["status"] != "read" || body["message_id"] != "wamid.HBgM=" || body["to"] != nil {
			t.Fatal("read payload", body)
		}
		indicator, _ := body["typing_indicator"].(map[string]any)
		if typing != (indicator != nil) || typing && indicator["type"] != "text" {
			t.Fatal("typing indicator", typing, body)
		}
	}
}

func TestMessagesReadValidationAndNotQueueable(t *testing.T) {
	s := fakeGraph(t, func(r *http.Request) string {
		t.Error("invalid read reached the provider")
		return `{}`
	})
	for _, id := range []string{"", "wamid with space", "wamid/../x"} {
		if _, err := s.Execute(context.Background(), testAccount(), Input{Operation: "messages.read", MessageID: id}); err == nil {
			t.Fatal("invalid message ID accepted", id)
		}
	}
	if queueable(Input{Operation: "messages.read", MessageID: "wamid.x"}, false) == nil {
		t.Fatal("read receipt must not be queueable")
	}
}

func TestBillingPricingQueryAndDataPoints(t *testing.T) {
	s := fakeGraph(t, func(r *http.Request) string {
		want := `currency,pricing_analytics.start(1767225600).end(1769904000).granularity(MONTHLY).dimensions(["PRICING_CATEGORY","COUNTRY"])`
		if r.Method != "GET" || r.URL.Path != "/v25.0/222" || r.URL.Query().Get("fields") != want {
			t.Errorf("wrong pricing request: %s %s %s", r.Method, r.URL.Path, r.URL.Query().Get("fields"))
		}
		return `{"id":"222","currency":"INR","pricing_analytics":{"data":[{"data_points":[{"start":1767225600,"end":1769904000,"volume":12,"cost":1.5,"pricing_category":"MARKETING","country":"IN"}]}]}}`
	})
	result, err := s.Execute(context.Background(), testAccount(), Input{Operation: "billing.pricing", Start: 1767225600, End: 1769904000, Granularity: "MONTHLY", Dimensions: []string{"PRICING_CATEGORY", "COUNTRY"}})
	if err != nil {
		t.Fatal(err)
	}
	points, _ := result["data_points"].([]any)
	point, _ := points[0].(map[string]any)
	if len(points) != 1 || point["volume"] != 12.0 || point["pricing_category"] != "MARKETING" || result["currency"] != "INR" {
		t.Fatal("data points", result)
	}
}

func TestBillingPricingWithoutDimensionsAndEmpty(t *testing.T) {
	s := fakeGraph(t, func(r *http.Request) string {
		if r.URL.Query().Get("fields") != "currency,pricing_analytics.start(1).end(2).granularity(DAILY)" {
			t.Error("wrong fields", r.URL.Query().Get("fields"))
		}
		return `{"id":"222"}`
	})
	result, err := s.Execute(context.Background(), testAccount(), Input{Operation: "billing.pricing", Start: 1, End: 2, Granularity: "DAILY"})
	if points, _ := result["data_points"].([]any); err != nil || points == nil || len(points) != 0 || result["currency"] != "" {
		t.Fatal("empty pricing", result, err)
	}
}

func TestBillingPricingValidation(t *testing.T) {
	s := fakeGraph(t, func(r *http.Request) string {
		t.Error("invalid pricing request reached the provider")
		return `{}`
	})
	for name, in := range map[string]Input{
		"no start":      {End: 2, Granularity: "DAILY"},
		"end first":     {Start: 5, End: 5, Granularity: "DAILY"},
		"granularity":   {Start: 1, End: 2, Granularity: "HOURLY"},
		"dimension":     {Start: 1, End: 2, Granularity: "DAILY", Dimensions: []string{"TIER"}},
		"duplicate":     {Start: 1, End: 2, Granularity: "DAILY", Dimensions: []string{"PHONE", "PHONE"}},
		"injected text": {Start: 1, End: 2, Granularity: "DAILY", Dimensions: []string{`PHONE"]),id`}},
	} {
		in.Operation = "billing.pricing"
		if _, err := s.Execute(context.Background(), testAccount(), in); err == nil {
			t.Fatal("accepted", name)
		}
	}
}

func TestSharedWABAPricingIsSplitBySite(t *testing.T) {
	s := fakeGraph(t, func(r *http.Request) string {
		return `{"currency":"INR","pricing_analytics":{"data":[{"data_points":[{"phone_number":"+91 98400 00001","cost":1},{"phone_number":"919840000002","cost":2},{"cost":3}]}]}}`
	})
	own, other := testAccount(), testAccount()
	own.SiteID, own.DisplayNumber = "site-one", "+91 98400 00001"
	other.ID, other.SiteID, other.PhoneID, other.DisplayNumber, other.CoreToken = "site-b", "site-two", "333", "919840000002", strings.Repeat("b", 64)
	if err := s.ReplaceAccounts([]Account{own, other}); err != nil {
		t.Fatal(err)
	}
	in := Input{Operation: "billing.pricing", Start: 1, End: 2, Granularity: "DAILY", Dimensions: []string{"PRICING_CATEGORY"}}
	if _, err := s.Execute(context.Background(), own, in); err == nil || !strings.Contains(err.Error(), "PHONE") {
		t.Fatalf("shared WABA without PHONE: %v", err)
	}
	in.Dimensions = append(in.Dimensions, "PHONE")
	result, err := s.Execute(context.Background(), own, in)
	if err != nil {
		t.Fatal(err)
	}
	points, _ := result["data_points"].([]any)
	if len(points) != 1 || points[0].(map[string]any)["cost"] != 1.0 || points[0].(map[string]any)["channel_key"] != "site-a" {
		t.Fatalf("site pricing rows: %v", points)
	}
}
