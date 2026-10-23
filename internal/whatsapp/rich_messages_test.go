package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func coordinate(v float64) *float64 { return &v }
func richCard() ContactCard {
	return ContactCard{Name: ContactName{FormattedName: "தமிழ் Support", FirstName: "தமிழ்", LastName: "Support", Prefix: "Dr.", MiddleName: "Team", Suffix: "Desk"}, Phones: []ContactPhone{{Phone: "+91 99999 99999", WhatsAppID: "919999999999", Type: "Work"}}, Emails: []ContactEmail{{Email: "support@example.invalid", Type: "Work"}}, Addresses: []ContactAddress{{Street: "1 Main Road", City: "Chennai", State: "TN", Zip: "600001", Country: "India", CountryCode: "IN", Type: "Work"}}, URLs: []ContactURL{{URL: "https://example.invalid/help", Type: "Support"}}, Organization: &ContactOrganization{Company: "Relay", Department: "Support", Title: "Agent"}, Birthday: "2000-02-29"}
}
func TestRichMessagePayloadSimulation(t *testing.T) {
	contact := richCard()
	contactJSON, _ := json.Marshal([]ContactCard{contact})
	var contactPayload any
	json.Unmarshal(contactJSON, &contactPayload)
	cases := []struct {
		name string
		in   Input
		body map[string]any
	}{
		{"location-zero", Input{Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(0), Longitude: coordinate(0)}, ReplyTo: "wamid.quote"}, map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual", "to": "919999999999", "type": "location", "location": map[string]any{"latitude": float64(0), "longitude": float64(0)}, "context": map[string]any{"message_id": "wamid.quote"}}},
		{"location-labelled", Input{Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(13.08), Longitude: coordinate(80.27), Name: "சென்னை", Address: "Chennai, India"}}, map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual", "to": "919999999999", "type": "location", "location": map[string]any{"latitude": 13.08, "longitude": 80.27, "name": "சென்னை", "address": "Chennai, India"}}},
		{"contact-card", Input{Operation: "messages.contacts", To: "919999999999", Contacts: []ContactCard{contact}, ReplyTo: "wamid.quote"}, map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual", "to": "919999999999", "type": "contacts", "contacts": contactPayload, "context": map[string]any{"message_id": "wamid.quote"}}},
		{"reaction", Input{Operation: "messages.react", To: "919999999999", ReplyTo: "wamid.target", Emoji: "👍🏽"}, map[string]any{"messaging_product": "whatsapp", "to": "919999999999", "type": "reaction", "reaction": map[string]any{"message_id": "wamid.target", "emoji": "👍🏽"}}},
		{"reaction-clear", Input{Operation: "messages.react", To: "919999999999", ReplyTo: "wamid.target", Emoji: ""}, map[string]any{"messaging_product": "whatsapp", "to": "919999999999", "type": "reaction", "reaction": map[string]any{"message_id": "wamid.target", "emoji": ""}}},
		{"sticker-reply", Input{Operation: "messages.media", To: "919999999999", MediaType: "sticker", MediaID: "123", ReplyTo: "wamid.quote"}, map[string]any{"messaging_product": "whatsapp", "to": "919999999999", "type": "sticker", "sticker": map[string]any{"id": "123"}, "context": map[string]any{"message_id": "wamid.quote"}}},
		{"group-sticker-reply", Input{Operation: "groups.media", To: "group-1", MediaType: "sticker", MediaURL: "https://example.invalid/sticker.webp", ReplyTo: "wamid.quote"}, map[string]any{"messaging_product": "whatsapp", "recipient_type": "group", "to": "group-1", "type": "sticker", "sticker": map[string]any{"link": "https://example.invalid/sticker.webp"}, "context": map[string]any{"message_id": "wamid.quote"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/groups") {
					fmt.Fprint(w, `{"data":{"groups":[{"id":"group-1"}]}}`)
					return
				}
				calls++
				if r.Method != "POST" || r.URL.Path != "/v25.0/111/messages" || r.Header.Get("Authorization") != "Bearer provider-secret" {
					t.Error("wrong route or auth")
				}
				var actual map[string]any
				if json.NewDecoder(r.Body).Decode(&actual) != nil || !reflect.DeepEqual(actual, tc.body) {
					t.Errorf("payload differs\ngot %#v\nwant %#v", actual, tc.body)
				}
				fmt.Fprint(w, `{"messages":[{"id":"wamid.accepted"}]}`)
			}))
			defer server.Close()
			s, _ := New([]Account{testAccount()})
			s.base = server.URL
			result, err := s.Execute(context.Background(), testAccount(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if result["messages"].([]any)[0].(map[string]any)["id"] != "wamid.accepted" || calls != 1 {
				t.Fatal("response changed or send retried")
			}
		})
	}
}
func TestRichMessageValidationSimulation(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("invalid message reached provider") }))
	defer provider.Close()
	s, _ := New([]Account{testAccount()})
	s.base = provider.URL
	invalid := []Input{{Operation: "messages.location", To: "919999999999"}, {Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(1)}}, {Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(91), Longitude: coordinate(0)}}, {Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(0), Longitude: coordinate(-181)}}, {Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(math.NaN()), Longitude: coordinate(0)}}, {Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(0), Longitude: coordinate(math.Inf(1))}}, {Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(0), Longitude: coordinate(0), Name: strings.Repeat("x", 257)}}, {Operation: "messages.contacts", To: "919999999999"}, {Operation: "messages.contacts", To: "919999999999", Contacts: make([]ContactCard, 258)}, {Operation: "messages.contacts", To: "919999999999", Contacts: []ContactCard{{Name: ContactName{FormattedName: " "}}}}, {Operation: "messages.contacts", To: "919999999999", Contacts: []ContactCard{richCard()}, ReplyTo: "../unsafe"}, {Operation: "messages.react", To: "919999999999", ReplyTo: "wamid.a", Emoji: strings.Repeat("😀", 17)}, {Operation: "messages.react", To: "919999999999", ReplyTo: ""}, {Operation: "messages.media", To: "919999999999", MediaType: "sticker", MediaID: "123", Caption: "not supported"}, {Operation: "messages.media", To: "919999999999", MediaType: "sticker", MediaID: "123", Filename: "sticker.webp"}}
	for _, op := range []string{"groups.location", "groups.contacts", "groups.react"} {
		invalid = append(invalid, Input{Operation: op, To: "group-1", ID: "group-1", Emoji: "👍", Location: &Location{Latitude: coordinate(0), Longitude: coordinate(0)}, Contacts: []ContactCard{richCard()}})
	}
	for _, in := range invalid {
		if _, err := s.execute(context.Background(), testAccount(), in, true); err == nil {
			t.Errorf("invalid input accepted %s", in.Operation)
		}
	}
	for _, change := range []func(*ContactCard){func(c *ContactCard) { c.Birthday = "2001-02-29" }, func(c *ContactCard) { c.Emails[0].Email = "Name <user@example.invalid>" }, func(c *ContactCard) { c.URLs[0].URL = "javascript:alert(1)" }, func(c *ContactCard) { c.URLs[0].URL = "https://user:secret@example.invalid" }, func(c *ContactCard) { c.Phones[0].Phone = "call me" }, func(c *ContactCard) { c.Addresses[0].CountryCode = "IND" }, func(c *ContactCard) { c.Phones = make([]ContactPhone, 21) }} {
		card := richCard()
		change(&card)
		if _, err := s.execute(context.Background(), testAccount(), Input{Operation: "messages.contacts", To: "919999999999", Contacts: []ContactCard{card}}, true); err == nil {
			t.Error("invalid contact detail accepted")
		}
	}
	cards := make([]ContactCard, 257)
	for i := range cards {
		cards[i] = richCard()
	}
	if _, err := s.execute(context.Background(), testAccount(), Input{Operation: "messages.contacts", To: "919999999999", Contacts: cards}, true); err == nil {
		t.Error("oversized contact payload accepted")
	}
	if calls != 0 {
		t.Fatal("invalid payload caused network request")
	}
}
func TestRichMessageProviderRejectionDoesNotRetry(t *testing.T) {
	for _, operation := range []string{"messages.location", "messages.contacts", "messages.react", "messages.media"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":{"message":"provider-token-must-stay-private"}}`)
			}))
			defer server.Close()
			s, _ := New([]Account{testAccount()})
			s.base = server.URL
			in := Input{Operation: operation, To: "919999999999", Location: &Location{Latitude: coordinate(0), Longitude: coordinate(0)}, Contacts: []ContactCard{richCard()}, ReplyTo: "wamid.target", Emoji: "👍", MediaType: "sticker", MediaID: "123"}
			_, err := s.Execute(context.Background(), testAccount(), in)
			if err == nil || strings.Contains(err.Error(), "provider-token") || calls != 1 {
				t.Fatal("provider rejection missing, leaking or retried", err)
			}
		})
	}
}
