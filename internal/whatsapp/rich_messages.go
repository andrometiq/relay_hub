package whatsapp

import (
	"errors"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// These structures match Meta's documented contacts and location message
// objects. Plain metadata stays typed; callers cannot inject arbitrary Graph
// properties or replace the account/recipient chosen by the authenticated API.
// https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/contacts-messages
// https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/location-messages

type Location struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Name      string   `json:"name,omitempty"`
	Address   string   `json:"address,omitempty"`
}
type ContactCard struct {
	Name         ContactName          `json:"name"`
	Phones       []ContactPhone       `json:"phones,omitempty"`
	Emails       []ContactEmail       `json:"emails,omitempty"`
	Addresses    []ContactAddress     `json:"addresses,omitempty"`
	URLs         []ContactURL         `json:"urls,omitempty"`
	Organization *ContactOrganization `json:"org,omitempty"`
	Birthday     string               `json:"birthday,omitempty"`
}
type ContactName struct {
	FormattedName string `json:"formatted_name"`
	FirstName     string `json:"first_name,omitempty"`
	LastName      string `json:"last_name,omitempty"`
	MiddleName    string `json:"middle_name,omitempty"`
	Prefix        string `json:"prefix,omitempty"`
	Suffix        string `json:"suffix,omitempty"`
}
type ContactPhone struct {
	Phone      string `json:"phone,omitempty"`
	Type       string `json:"type,omitempty"`
	WhatsAppID string `json:"wa_id,omitempty"`
}
type ContactEmail struct {
	Email string `json:"email"`
	Type  string `json:"type,omitempty"`
}
type ContactAddress struct {
	Street      string `json:"street,omitempty"`
	City        string `json:"city,omitempty"`
	State       string `json:"state,omitempty"`
	Zip         string `json:"zip,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	Type        string `json:"type,omitempty"`
}
type ContactURL struct {
	URL  string `json:"url"`
	Type string `json:"type,omitempty"`
}
type ContactOrganization struct {
	Company    string `json:"company,omitempty"`
	Department string `json:"department,omitempty"`
	Title      string `json:"title,omitempty"`
}

func boundedText(value string, limit int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, '\x00')
}
func (l *Location) validate() error {
	if l == nil || l.Latitude == nil || l.Longitude == nil {
		return errors.New("latitude and longitude are required")
	}
	lat, lon := *l.Latitude, *l.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return errors.New("invalid coordinates")
	}
	if !boundedText(l.Name, 256) || !boundedText(l.Address, 1024) {
		return errors.New("location name or address is too long or invalid")
	}
	return nil
}

var cardPhone = regexp.MustCompile(`^[+0-9(). -]{1,64}$`)
var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

func validateContacts(cards []ContactCard) error {
	if len(cards) < 1 || len(cards) > 257 {
		return errors.New("send between 1 and 257 contact cards")
	}
	for _, card := range cards {
		if strings.TrimSpace(card.Name.FormattedName) == "" {
			return errors.New("contact formatted name is required")
		}
		for _, text := range []string{card.Name.FormattedName, card.Name.FirstName, card.Name.LastName, card.Name.MiddleName, card.Name.Prefix, card.Name.Suffix} {
			if !boundedText(text, 256) {
				return errors.New("invalid contact name")
			}
		}
		if len(card.Phones) > 20 || len(card.Emails) > 20 || len(card.Addresses) > 20 || len(card.URLs) > 20 {
			return errors.New("at most 20 entries per contact field are supported")
		}
		if card.Birthday != "" {
			if _, err := time.Parse("2006-01-02", card.Birthday); err != nil {
				return errors.New("birthday must be a valid YYYY-MM-DD date")
			}
		}
		for _, p := range card.Phones {
			if (p.Phone == "" && p.WhatsAppID == "") || (p.Phone != "" && (!cardPhone.MatchString(p.Phone) || !strings.ContainsAny(p.Phone, "0123456789"))) || (p.WhatsAppID != "" && (!digits.MatchString(p.WhatsAppID) || len(p.WhatsAppID) > 15)) || !boundedText(p.Type, 64) {
				return errors.New("invalid contact phone")
			}
		}
		for _, e := range card.Emails {
			address, err := mail.ParseAddress(e.Email)
			if err != nil || address.Address != e.Email || len(e.Email) > 254 || strings.ContainsAny(e.Email, "\r\n") || !boundedText(e.Type, 64) {
				return errors.New("invalid contact email")
			}
		}
		for _, a := range card.Addresses {
			for _, text := range []string{a.Street, a.City, a.State, a.Zip, a.Country, a.Type} {
				if !boundedText(text, 1024) {
					return errors.New("invalid contact address")
				}
			}
			if a.CountryCode != "" && !countryCode.MatchString(a.CountryCode) {
				return errors.New("country code must contain two uppercase letters")
			}
		}
		for _, u := range card.URLs {
			parsed, err := url.Parse(u.URL)
			if err != nil || len(u.URL) > 2048 || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || strings.ContainsAny(u.URL, "\r\n") || !boundedText(u.Type, 64) {
				return errors.New("invalid contact website URL")
			}
		}
		if card.Organization != nil {
			for _, text := range []string{card.Organization.Company, card.Organization.Department, card.Organization.Title} {
				if !boundedText(text, 256) {
					return errors.New("invalid contact organization")
				}
			}
		}
	}
	return nil
}
func replyContext(payload map[string]any, replyTo string) error {
	if replyTo == "" {
		return nil
	}
	if !callIdentifier.MatchString(replyTo) {
		return errors.New("invalid reply message ID")
	}
	payload["context"] = map[string]string{"message_id": replyTo}
	return nil
}

// Interactive is Meta's reply-button or list message. Core sends choices and
// polls with it; replies come back as interactive button_reply or list_reply.
// https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/interactive-reply-buttons-messages
// https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/interactive-list-messages
type Interactive struct {
	Type   string            `json:"type"`
	Body   InteractiveText   `json:"body"`
	Action InteractiveAction `json:"action"`
}
type InteractiveText struct {
	Text string `json:"text"`
}
type InteractiveAction struct {
	Button   string               `json:"button,omitempty"`
	Buttons  []InteractiveButton  `json:"buttons,omitempty"`
	Sections []InteractiveSection `json:"sections,omitempty"`
}
type InteractiveButton struct {
	Type  string           `json:"type"`
	Reply InteractiveReply `json:"reply"`
}
type InteractiveReply struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type InteractiveSection struct {
	Title string           `json:"title,omitempty"`
	Rows  []InteractiveRow `json:"rows"`
}
type InteractiveRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

func requiredText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && boundedText(value, limit)
}

// uniqueOption checks one reply ID and title; IDs and titles must be unique.
func uniqueOption(seen map[string]bool, id, title string, idLimit, titleLimit int) error {
	if !requiredText(id, idLimit) || !requiredText(title, titleLimit) {
		return errors.New("interactive option ID or title is empty or too long")
	}
	if seen[id] || seen["title:"+title] {
		return errors.New("interactive option IDs and titles must be unique")
	}
	seen[id], seen["title:"+title] = true, true
	return nil
}

func (i *Interactive) validate() error {
	if i == nil {
		return errors.New("interactive message is required")
	}
	seen := map[string]bool{}
	switch i.Type {
	case "button":
		if !requiredText(i.Body.Text, 1024) || i.Action.Button != "" || len(i.Action.Sections) > 0 || len(i.Action.Buttons) < 1 || len(i.Action.Buttons) > 3 {
			return errors.New("reply buttons need a body of up to 1024 characters and 1-3 buttons")
		}
		for _, button := range i.Action.Buttons {
			if button.Type != "reply" {
				return errors.New("reply buttons must have type reply")
			}
			if err := uniqueOption(seen, button.Reply.ID, button.Reply.Title, 256, 20); err != nil {
				return err
			}
		}
	case "list":
		if !requiredText(i.Body.Text, 4096) || !requiredText(i.Action.Button, 20) || len(i.Action.Buttons) > 0 || len(i.Action.Sections) < 1 || len(i.Action.Sections) > 10 {
			return errors.New("a list needs a body of up to 4096 characters, a button label of up to 20 and 1-10 sections")
		}
		rows := 0
		for _, section := range i.Action.Sections {
			if !boundedText(section.Title, 24) || len(i.Action.Sections) > 1 && strings.TrimSpace(section.Title) == "" {
				return errors.New("list section titles have up to 24 characters and are required for more than one section")
			}
			for _, row := range section.Rows {
				if !boundedText(row.Description, 72) {
					return errors.New("list row description is too long")
				}
				if err := uniqueOption(seen, row.ID, row.Title, 200, 24); err != nil {
					return err
				}
			}
			rows += len(section.Rows)
		}
		if rows < 1 || rows > 10 {
			return errors.New("a list has 1-10 rows")
		}
	default:
		return errors.New("interactive type must be button or list")
	}
	return nil
}
