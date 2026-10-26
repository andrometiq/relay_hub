// Package whatsapp provides a scoped Graph adapter. No credentials enter Core/UI.
package whatsapp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Account struct {
	AppID             string `json:"app_id,omitempty"`
	SyncMetadata      bool   `json:"sync_metadata,omitempty"`
	MessagesPerSecond int    `json:"messages_per_second,omitempty"`
	CoreAPIKey        string `json:"core_api_key,omitempty"`
	CoreAPISecret     string `json:"core_api_secret,omitempty"`
	CallbackSecret    string `json:"callback_secret,omitempty"`
	AppSecret         string `json:"app_secret"`
	VerifyToken       string `json:"verify_token"`
	ID                string `json:"id"`
	Site              string `json:"site"`
	CoreToken         string `json:"core_token"`
	AccessToken       string `json:"access_token"`
	Version           string `json:"graph_version"`
	PhoneID           string `json:"phone_id"`
	WABAID            string `json:"waba_id"`
	Templates         bool   `json:"templates"`
	Groups            bool   `json:"groups"`
	Calls             bool   `json:"calls"`
}
type Service struct {
	Metadata     *MetadataSync
	accountsMu   sync.RWMutex
	webhookSlots chan struct{}
	callbackWake chan struct{}
	Events       EventStore
	Outbox       *Outbox
	slots        chan struct{}
	Accounts     []Account
	client       *http.Client
	base         string
}

func (s *Service) AccountsSnapshot() []Account {
	s.accountsMu.RLock()
	defer s.accountsMu.RUnlock()
	return append([]Account(nil), s.Accounts...)
}
func (s *Service) ReplaceAccounts(accounts []Account) error {
	validated, err := New(accounts)
	if err != nil {
		return err
	}
	s.accountsMu.Lock()
	s.Accounts = append([]Account(nil), validated.Accounts...)
	s.accountsMu.Unlock()
	return nil
}

// ValidateCredentials confirms the access token can read both configured Meta
// identities. It performs no mutation and returns no provider payload.
func (s *Service) ValidateCredentials(ctx context.Context, account Account) error {
	if account.AppSecret == "" || account.VerifyToken == "" {
		return errors.New("webhook credentials required")
	}
	if _, err := New([]Account{account}); err != nil {
		return err
	}
	phone, err := s.graph(ctx, account, http.MethodGet, account.PhoneID, url.Values{"fields": {"id"}}, nil)
	if err != nil || strings.TrimSpace(stringValue(phone["id"])) != account.PhoneID {
		return errors.New("phone number identity could not be verified")
	}
	business, err := s.graph(ctx, account, http.MethodGet, account.WABAID, url.Values{"fields": {"id"}}, nil)
	if err != nil || strings.TrimSpace(stringValue(business["id"])) != account.WABAID {
		return errors.New("business account identity could not be verified")
	}
	query := url.Values{"fields": {"id"}, "limit": {"100"}}
	for page := 0; page < 100; page++ {
		result, err := s.graph(ctx, account, http.MethodGet, account.WABAID+"/phone_numbers", query, nil)
		if err != nil {
			break
		}
		rows, _ := result["data"].([]any)
		for _, row := range rows {
			phone, _ := row.(map[string]any)
			if stringValue(phone["id"]) == account.PhoneID {
				return nil
			}
		}
		paging, _ := result["paging"].(map[string]any)
		cursors, _ := paging["cursors"].(map[string]any)
		next := stringValue(cursors["after"])
		if paging["has_more"] != true || next == "" || next == query.Get("after") {
			break
		}
		query.Set("after", next)
	}
	return errors.New("phone number identity could not be verified")
}

func stringValue(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case json.Number:
		return item.String()
	default:
		return ""
	}
}

type Input struct {
	CallbackData          string          `json:"callback_data,omitempty"`
	FlowScreen            string          `json:"flow_screen,omitempty"`
	FlowCTA               string          `json:"flow_cta,omitempty"`
	FlowToken             string          `json:"flow_token,omitempty"`
	FlowData              map[string]any  `json:"flow_data,omitempty"`
	Categories            []string        `json:"categories,omitempty"`
	EndpointURI           string          `json:"endpoint_uri,omitempty"`
	FlowJSON              json.RawMessage `json:"flow_json,omitempty"`
	ParameterFormat       string          `json:"parameter_format,omitempty"`
	MessageSendTTLSeconds int             `json:"message_send_ttl_seconds,omitempty"`
	RequestKey            string          `json:"request_key,omitempty"`
	Location              *Location       `json:"location,omitempty"`
	Contacts              []ContactCard   `json:"contacts,omitempty"`
	tracking              string
	beforeSubmit          func(context.Context) error
	Before                string   `json:"before,omitempty"`
	Description           string   `json:"description,omitempty"`
	Participants          []string `json:"participants,omitempty"`
	JoinRequests          []string `json:"join_requests,omitempty"`
	JoinApprovalMode      string   `json:"join_approval_mode,omitempty"`
	ExpirationDays        int      `json:"expiration_days,omitempty"`
	PhotoBase64           string   `json:"photo_base64,omitempty"`
	MediaType             string   `json:"media_type,omitempty"`
	MediaID               string   `json:"media_id,omitempty"`
	MediaURL              string   `json:"media_url,omitempty"`
	ContentBase64         string   `json:"content_base64,omitempty"`
	MIMEType              string   `json:"mime_type,omitempty"`
	Caption               string   `json:"caption,omitempty"`
	Filename              string   `json:"filename,omitempty"`
	MessageID             string   `json:"message_id,omitempty"`
	Typing                bool     `json:"typing,omitempty"`
	Start                 int64    `json:"start,omitempty"`
	End                   int64    `json:"end,omitempty"`
	Granularity           string   `json:"granularity,omitempty"`
	Dimensions            []string `json:"dimensions,omitempty"`

	Operation  string           `json:"operation"`
	ID         string           `json:"id,omitempty"`
	ReplyTo    string           `json:"reply_to,omitempty"`
	Emoji      string           `json:"emoji,omitempty"`
	To         string           `json:"to,omitempty"`
	Name       string           `json:"name,omitempty"`
	Language   string           `json:"language,omitempty"`
	Category   string           `json:"category,omitempty"`
	Body       string           `json:"body,omitempty"`
	Subject    string           `json:"subject,omitempty"`
	Action     string           `json:"action,omitempty"`
	SDP        string           `json:"sdp,omitempty"`
	SDPType    string           `json:"sdp_type,omitempty"`
	After      string           `json:"after,omitempty"`
	Components []map[string]any `json:"components,omitempty"`
}

var ErrBusy = errors.New("Hub is busy; operation was not submitted")

// ErrGroupNotFound means ownership was not established and no mutation was sent.
var ErrGroupNotFound = errors.New("group not found in this account")

var digits = regexp.MustCompile(`^[0-9]{1,30}$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
var callIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.=:-]{1,512}$`)
var templateName = regexp.MustCompile(`^[a-z0-9_]{1,512}$`)
var language = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?$`)

func New(accounts []Account) (*Service, error) {
	seen := map[string]bool{}
	tokens := map[string]bool{}
	for _, a := range accounts {
		site, e := url.Parse(a.Site)
		if a.MessagesPerSecond < 0 || a.MessagesPerSecond > 1000 || !identifier.MatchString(a.ID) || len(a.ID) > 100 || seen[a.ID] || (a.CoreToken != "" && (len(a.CoreToken) < 32 || strings.ContainsAny(a.CoreToken, "\r\n\x00") || tokens[a.CoreToken])) || a.AccessToken == "" || strings.ContainsAny(a.AccessToken, "\r\n") || !digits.MatchString(a.PhoneID) || !digits.MatchString(a.WABAID) || !regexp.MustCompile(`^v[0-9]+\.0$`).MatchString(a.Version) || e != nil || site.Host == "" || (site.Scheme != "http" && site.Scheme != "https") || site.User != nil || site.RawQuery != "" || site.Fragment != "" || (site.Path != "" && site.Path != "/") {
			return nil, errors.New("invalid or duplicate WhatsApp account configuration")
		}
		if a.CoreAPIKey != "" || a.CoreAPISecret != "" || a.CallbackSecret != "" {
			if a.CoreAPIKey == "" || a.CoreAPISecret == "" || len(a.CallbackSecret) < 32 || strings.ContainsAny(a.CoreAPIKey+a.CoreAPISecret+a.CallbackSecret, "\r\n\x00") {
				return nil, errors.New("complete callback credentials required")
			}
		}
		seen[a.ID] = true
		tokens[a.CoreToken] = true
	}
	return &Service{webhookSlots: make(chan struct{}, 4), callbackWake: make(chan struct{}, 1), Accounts: accounts, slots: make(chan struct{}, 4), base: "https://graph.facebook.com", client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects refused") }}}, nil
}

// NewWithHTTPClient installs a caller-owned transport for instrumentation or
// local provider simulation. The Graph host, account validation, request timeout
// ceiling and redirect refusal remain identical to the production constructor.
// Copy the client so enforcing these limits never mutates the caller's client.
func NewWithHTTPClient(accounts []Account, client *http.Client) (*Service, error) {
	s, err := New(accounts)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	configured := *client
	configured.CheckRedirect = s.client.CheckRedirect
	if configured.Timeout <= 0 || configured.Timeout > s.client.Timeout {
		configured.Timeout = s.client.Timeout
	}
	s.client = &configured
	return s, nil
}

func Load(path string) (*Service, error) {
	if path == "" {
		return New(nil)
	}
	info, e := os.Stat(path)
	if e != nil {
		return nil, errors.New("cannot inspect WhatsApp configuration")
	}
	if info.Size() > 65536 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("WhatsApp configuration must be private (0600)")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var accounts []Account
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&accounts) != nil {
		return nil, errors.New("invalid WhatsApp configuration")
	}
	return New(accounts)
}
func (s *Service) Authenticate(token string) (Account, bool) {
	for _, a := range s.AccountsSnapshot() {
		if len(token) >= 32 && subtle.ConstantTimeCompare([]byte(token), []byte(a.CoreToken)) == 1 {
			return a, true
		}
	}
	return Account{}, false
}
func (s *Service) Execute(ctx context.Context, a Account, in Input) (map[string]any, error) {
	return s.execute(ctx, a, in, false)
}
func (s *Service) execute(ctx context.Context, a Account, in Input, validateOnly bool) (result map[string]any, err error) {
	submitted := false
	before := func() error {
		if in.beforeSubmit != nil {
			if err := in.beforeSubmit(ctx); err != nil {
				return &deferredError{err}
			}
		}
		submitted = true
		return nil
	}
	defer func() {
		if err != nil && !submitted {
			err = &preSubmissionError{err}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Validation makes no provider request, so it never competes for a slot.
	if !validateOnly {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			return nil, ErrBusy
		}
	}
	if in.Operation == "groups.react" || in.Operation == "groups.location" || in.Operation == "groups.contacts" {
		return nil, errors.New("this message type is not documented as supported by WhatsApp Groups API")
	}
	if in.Operation == "groups.participants.add" || in.Operation == "groups.invite.revoke" {
		return nil, errors.New("unsupported by WhatsApp Groups API; use an invite link with join approval, or reset the invite link")
	}
	if strings.HasPrefix(in.Operation, "groups.") && in.Operation != "groups.list" && in.Operation != "groups.create" {
		id := in.ID
		if in.Operation == "groups.send" || in.Operation == "groups.media" || in.Operation == "groups.template" || in.Operation == "groups.pin" {
			id = in.To
		}
		if !a.Groups || !identifier.MatchString(id) {
			return nil, errors.New("enabled group and valid ID required")
		}
		if !validateOnly {
			if err := s.checkGroup(ctx, a, id); err != nil {
				return nil, err
			}
		}
	}
	if in.Operation == "messages.flow" {
		return s.sendFlow(ctx, a, in, validateOnly, before)
	}
	if strings.HasPrefix(in.Operation, "flows.") {
		return s.flow(ctx, a, in, validateOnly)
	}
	method := "POST"
	path := ""
	query := url.Values{}
	var body any
	switch in.Operation {
	case "groups.photo":
		return s.groupPhoto(ctx, a, in, validateOnly)
	case "messages.history":
		return s.history(ctx, a, in)
	case "events.list":
		return s.events(ctx, a, in.After)
	case "events.latest":
		return s.latestEvents(ctx, a)
	case "capabilities":
		return map[string]any{"account": a.ID, "site": a.Site, "templates": a.Templates, "flows": a.Templates, "groups": a.Groups, "calls": a.Calls, "recording": false}, nil
	case "messages.location", "messages.contacts":
		if !digits.MatchString(in.To) || len(in.To) < 7 || len(in.To) > 15 {
			return nil, errors.New("valid recipient required")
		}
		payload := map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual", "to": in.To}
		if in.Operation == "messages.location" {
			if err := in.Location.validate(); err != nil {
				return nil, err
			}
			payload["type"] = "location"
			payload["location"] = in.Location
		} else {
			if err := validateContacts(in.Contacts); err != nil {
				return nil, err
			}
			payload["type"] = "contacts"
			payload["contacts"] = in.Contacts
		}
		if err := replyContext(payload, in.ReplyTo); err != nil {
			return nil, err
		}
		path = a.PhoneID + "/messages"
		body = payload
	case "messages.media", "groups.media":
		if in.Operation == "messages.media" && (!digits.MatchString(in.To) || len(in.To) < 7 || len(in.To) > 15) {
			return nil, errors.New("valid recipient required")
		}
		if in.MediaType != "image" && in.MediaType != "video" && in.MediaType != "audio" && in.MediaType != "document" && in.MediaType != "sticker" {
			return nil, errors.New("invalid media type")
		}
		media := map[string]any{}
		var upload []byte
		var uploadType string
		if in.ContentBase64 != "" {
			var err error
			upload, uploadType, err = attachmentBytes(in)
			if err != nil {
				return nil, err
			}
		} else if in.MediaID != "" && in.MediaURL == "" {
			if !digits.MatchString(in.MediaID) {
				return nil, errors.New("invalid media ID")
			}
			media["id"] = in.MediaID
		} else if in.MediaURL != "" && in.MediaID == "" {
			u, err := url.Parse(in.MediaURL)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(in.MediaURL) > 2048 {
				return nil, errors.New("media requires an HTTPS URL without credentials")
			}
			media["link"] = in.MediaURL
		} else {
			return nil, errors.New("supply one media ID or URL")
		}
		if in.Caption != "" {
			if (in.MediaType != "image" && in.MediaType != "video" && in.MediaType != "document") || len([]rune(in.Caption)) > 1024 {
				return nil, errors.New("caption unsupported or too long")
			}
			media["caption"] = in.Caption
		}
		if in.Filename != "" {
			if in.MediaType != "document" || len(in.Filename) > 255 || strings.ContainsAny(in.Filename, "/\\\r\n") {
				return nil, errors.New("invalid document filename")
			}
			media["filename"] = in.Filename
		}
		payload := map[string]any{"messaging_product": "whatsapp", "to": in.To, "type": in.MediaType, in.MediaType: media}
		if in.Operation == "groups.media" {
			payload["recipient_type"] = "group"
		}
		if in.ReplyTo != "" {
			if !callIdentifier.MatchString(in.ReplyTo) {
				return nil, errors.New("invalid reply ID")
			}
			payload["context"] = map[string]string{"message_id": in.ReplyTo}
		}
		if len(upload) > 0 && !validateOnly {
			id, err := s.uploadAttachment(ctx, a, in, upload, uploadType)
			if err != nil {
				return nil, &deferredError{err}
			}
			media["id"] = id
		}
		path = a.PhoneID + "/messages"
		body = payload
	case "account.metadata":
		if s.Metadata == nil {
			return map[string]any{"status": "not_synced"}, nil
		}
		return s.Metadata.Read(ctx, a.ID)
	case "billing.pricing":
		return s.pricing(ctx, a, in, validateOnly)
	case "messages.read":
		if !callIdentifier.MatchString(in.MessageID) {
			return nil, errors.New("valid message ID required")
		}
		payload := map[string]any{"messaging_product": "whatsapp", "status": "read", "message_id": in.MessageID}
		if in.Typing {
			payload["typing_indicator"] = map[string]string{"type": "text"}
		}
		path = a.PhoneID + "/messages"
		body = payload
	case "account.profile":
		method = "GET"
		path = a.PhoneID
		query.Set("fields", "id,display_phone_number,verified_name,quality_rating")
	case "templates.delete":
		if !a.Templates || !templateName.MatchString(in.Name) {
			return nil, errors.New("enabled templates and template name required")
		}
		method = "DELETE"
		path = a.WABAID + "/message_templates"
		query.Set("name", in.Name)
		if in.ID != "" {
			if !digits.MatchString(in.ID) {
				return nil, errors.New("invalid template ID")
			}
			query.Set("hsm_id", in.ID)
		}
	case "messages.send", "messages.react":
		if !digits.MatchString(in.To) || len(in.To) < 7 || len(in.To) > 15 {
			return nil, errors.New("recipient must include country code")
		}
		payload := map[string]any{"messaging_product": "whatsapp", "to": in.To}
		if in.Operation == "messages.send" {
			if strings.TrimSpace(in.Body) == "" || len([]rune(in.Body)) > 4096 {
				return nil, errors.New("text must contain 1–4096 characters")
			}
			payload["type"] = "text"
			payload["text"] = map[string]any{"body": in.Body}
			if in.ReplyTo != "" {
				if !callIdentifier.MatchString(in.ReplyTo) {
					return nil, errors.New("invalid reply message ID")
				}
				payload["context"] = map[string]string{"message_id": in.ReplyTo}
			}
		} else {
			if !callIdentifier.MatchString(in.ReplyTo) || len([]rune(in.Emoji)) > 16 {
				return nil, errors.New("message ID and valid reaction required")
			}
			payload["type"] = "reaction"
			payload["reaction"] = map[string]string{"message_id": in.ReplyTo, "emoji": in.Emoji}
		}
		path = a.PhoneID + "/messages"
		body = payload
	case "templates.media_upload":
		return s.uploadTemplateSample(ctx, a, in, validateOnly)
	case "messages.media_upload":
		raw, mediaType, err := attachmentBytes(in)
		if err != nil {
			return nil, err
		}
		if validateOnly {
			return map[string]any{"valid": true}, nil
		}
		id, err := s.uploadAttachment(ctx, a, in, raw, mediaType)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id}, nil
	case "templates.list":
		if !a.Templates {
			return nil, errors.New("templates are not enabled for this account")
		}
		method = "GET"
		path = a.WABAID + "/message_templates"
		query.Set("limit", "50")
		if len(in.After) > 2048 {
			return nil, errors.New("invalid cursor")
		}
		if in.After != "" {
			query.Set("after", in.After)
		}
	case "templates.create":
		if e := normalizeParameterFormat(&in); e != nil {
			return nil, e
		}
		if in.MessageSendTTLSeconds < 0 || in.MessageSendTTLSeconds > 2147483647 {
			return nil, errors.New("invalid template validity")
		}

		if !a.Templates {
			return nil, errors.New("templates are not enabled for this account")
		}
		if !templateName.MatchString(in.Name) || !language.MatchString(in.Language) || (in.Category != "UTILITY" && in.Category != "MARKETING" && in.Category != "AUTHENTICATION") || len(in.Components) == 0 || len(in.Components) > 20 {
			return nil, errors.New("template name, language, category and components are required")
		}
		path = a.WABAID + "/message_templates"
		body = map[string]any{"name": in.Name, "language": in.Language, "category": in.Category, "components": in.Components}
		if in.ParameterFormat != "" {
			body.(map[string]any)["parameter_format"] = in.ParameterFormat
		}
		if in.MessageSendTTLSeconds > 0 {
			body.(map[string]any)["message_send_ttl_seconds"] = in.MessageSendTTLSeconds
		}
	case "templates.update":
		if !a.Templates || !digits.MatchString(in.ID) || len(in.Components) == 0 || len(in.Components) > 20 {
			return nil, errors.New("enabled template and valid ID/components are required")
		}
		if !templateName.MatchString(in.Name) {
			return nil, errors.New("template name is required to verify ownership")
		}
		owned, e := s.graph(ctx, a, "GET", a.WABAID+"/message_templates", url.Values{"name": {in.Name}, "limit": {"100"}}, nil)
		if e != nil {
			return nil, e
		}
		found := false
		if rows, ok := owned["data"].([]any); ok {
			for _, row := range rows {
				if value, ok := row.(map[string]any); ok && value["id"] == in.ID {
					found = true
				}
			}
		}
		if !found {
			return nil, errors.New("template does not belong to this account")
		}
		if e := normalizeParameterFormat(&in); e != nil {
			return nil, e
		}
		path = in.ID
		body = map[string]any{"components": in.Components}
		if in.ParameterFormat != "" {
			// Without it Meta reads named variables as positional and refuses the edit.
			body.(map[string]any)["parameter_format"] = in.ParameterFormat
		}
		if in.MessageSendTTLSeconds < 0 || in.MessageSendTTLSeconds > 2147483647 {
			return nil, errors.New("invalid template validity")
		}
		if in.MessageSendTTLSeconds > 0 {
			body.(map[string]any)["message_send_ttl_seconds"] = in.MessageSendTTLSeconds
		}
	case "templates.send", "groups.template":
		if !a.Templates || (in.Operation == "templates.send" && (!digits.MatchString(in.To) || len(in.To) < 7 || len(in.To) > 15)) || !templateName.MatchString(in.Name) || !language.MatchString(in.Language) {
			return nil, errors.New("enabled template, recipient, name and language are required")
		}
		path = a.PhoneID + "/messages"
		body = map[string]any{"messaging_product": "whatsapp", "to": in.To, "type": "template", "template": map[string]any{"name": in.Name, "language": map[string]string{"code": in.Language}, "components": append([]map[string]any{}, in.Components...)}}
		if in.Operation == "groups.template" {
			body.(map[string]any)["recipient_type"] = "group"
		}
	case "groups.list":
		if !a.Groups {
			return nil, errors.New("groups are not enabled for this account")
		}
		method = "GET"
		path = a.PhoneID + "/groups"
		query.Set("limit", "50")
		if len(in.After) > 2048 {
			return nil, errors.New("invalid cursor")
		}
		if in.After != "" {
			query.Set("after", in.After)
		}
	case "groups.info", "groups.update", "groups.delete", "groups.invite.get", "groups.invite.reset", "groups.invite.create", "groups.participants.remove", "groups.requests.list", "groups.requests.approve", "groups.requests.reject":
		path = in.ID
		payload := map[string]any{"messaging_product": "whatsapp"}
		switch in.Operation {
		case "groups.info":
			method = "GET"
			query.Set("fields", "id,subject,description,participants,join_approval_mode")
		case "groups.update":
			if strings.TrimSpace(in.Subject) == "" || len([]rune(in.Subject)) > 128 || len([]rune(in.Description)) > 2048 {
				return nil, errors.New("valid subject and description required")
			}
			payload["subject"] = strings.TrimSpace(in.Subject)
			payload["description"] = in.Description
		case "groups.delete":
			method = "DELETE"
		case "groups.invite.get", "groups.invite.reset", "groups.invite.create":
			path += "/invite_link"
			if in.Operation == "groups.invite.get" {
				method = "GET"
			}
		case "groups.participants.remove":
			path += "/participants"
			method = "DELETE"
			if len(in.Participants) < 1 || len(in.Participants) > 8 {
				return nil, errors.New("select 1 to 8 participants")
			}
			people := []map[string]string{}
			for _, p := range in.Participants {
				if !digits.MatchString(p) || len(p) < 7 || len(p) > 15 {
					return nil, errors.New("invalid participant")
				}
				people = append(people, map[string]string{"user": p})
			}
			payload["participants"] = people
		case "groups.requests.list":
			method = "GET"
			path += "/join_requests"
			query.Set("limit", "50")
			if len(in.After) > 2048 {
				return nil, errors.New("invalid cursor")
			}
			if in.After != "" {
				query.Set("after", in.After)
			}
		case "groups.requests.approve", "groups.requests.reject":
			path += "/join_requests"
			if in.Operation == "groups.requests.reject" {
				method = "DELETE"
			}
			if len(in.JoinRequests) < 1 || len(in.JoinRequests) > 50 {
				return nil, errors.New("select 1 to 50 join requests")
			}
			for _, id := range in.JoinRequests {
				if !callIdentifier.MatchString(id) {
					return nil, errors.New("invalid join request")
				}
			}
			payload["join_requests"] = in.JoinRequests
		}
		if method != "GET" {
			body = payload
		}
	case "groups.create":
		if !a.Groups || strings.TrimSpace(in.Subject) == "" || len([]rune(in.Subject)) > 128 || len([]rune(in.Description)) > 2048 {
			return nil, errors.New("enabled groups and a subject up to 128 characters are required")
		}
		path = a.PhoneID + "/groups"
		mode := in.JoinApprovalMode
		if mode == "" {
			mode = "approval_required"
		}
		if mode != "approval_required" && mode != "auto_approve" {
			return nil, errors.New("invalid join approval mode")
		}
		body = map[string]any{"messaging_product": "whatsapp", "subject": strings.TrimSpace(in.Subject), "join_approval_mode": mode, "description": in.Description}
	case "groups.pin":
		if !callIdentifier.MatchString(in.ReplyTo) || (in.Action != "pin" && in.Action != "unpin") || (in.Action == "pin" && (in.ExpirationDays < 1 || in.ExpirationDays > 30)) || (in.Action == "unpin" && in.ExpirationDays != 0) {
			return nil, errors.New("valid message, pin action and expiration of 1 to 30 days required")
		}
		pin := map[string]any{"type": in.Action, "message_id": in.ReplyTo}
		if in.Action == "pin" {
			pin["expiration_days"] = in.ExpirationDays
		}
		path = a.PhoneID + "/messages"
		body = map[string]any{"messaging_product": "whatsapp", "recipient_type": "group", "to": in.To, "type": "pin", "pin": pin}
	case "groups.send":
		if !a.Groups || !identifier.MatchString(in.To) || strings.TrimSpace(in.Body) == "" || len([]rune(in.Body)) > 4096 {
			return nil, errors.New("enabled group, recipient and text up to 4096 characters are required")
		}
		path = a.PhoneID + "/messages"
		body = map[string]any{"messaging_product": "whatsapp", "recipient_type": "group", "to": in.To, "type": "text", "text": map[string]any{"body": in.Body}}
		if in.ReplyTo != "" {
			if !callIdentifier.MatchString(in.ReplyTo) {
				return nil, errors.New("invalid reply message ID")
			}
			body.(map[string]any)["context"] = map[string]string{"message_id": in.ReplyTo}
		}
	case "calls.permission.get":
		if !a.Calls || !digits.MatchString(in.To) {
			return nil, errors.New("enabled calling and recipient required")
		}
		method = "GET"
		path = a.PhoneID + "/call_permissions"
		query.Set("user_wa_id", in.To)
	case "calls.permission.request":
		if !a.Calls || !digits.MatchString(in.To) {
			return nil, errors.New("enabled calling and a recipient are required")
		}
		path = a.PhoneID + "/messages"
		body = map[string]any{"messaging_product": "whatsapp", "to": in.To, "type": "interactive", "interactive": map[string]any{"type": "call_permission_request", "action": map[string]string{"name": "call_permission_request"}}}
	case "calls.action":
		if !a.Calls {
			return nil, errors.New("calls are not enabled for this account")
		}
		payload := map[string]any{"messaging_product": "whatsapp", "action": in.Action}
		if len(in.CallbackData) > 512 {
			return nil, errors.New("call callback data exceeds 512 bytes")
		}
		if in.CallbackData != "" {
			payload["biz_opaque_callback_data"] = in.CallbackData
		}
		switch in.Action {
		case "connect":
			if !digits.MatchString(in.To) {
				return nil, errors.New("recipient required")
			}
			payload["to"] = in.To
		case "pre_accept", "accept", "reject", "terminate":
			if !callIdentifier.MatchString(in.ID) {
				return nil, errors.New("call ID required")
			}
			payload["call_id"] = in.ID
		default:
			return nil, errors.New("invalid call action")
		}
		if in.Action == "connect" || in.Action == "pre_accept" || in.SDP != "" || in.SDPType != "" {
			if (in.Action != "connect" && in.Action != "pre_accept" && in.Action != "accept") || !strings.HasPrefix(in.SDP, "v=0") || len(in.SDP) > 60000 || (in.SDPType != "offer" && in.SDPType != "answer") || (in.Action == "accept" && in.SDPType != "answer") {
				return nil, errors.New("valid SDP session required")
			}
			payload["session"] = map[string]string{"sdp_type": in.SDPType, "sdp": in.SDP}
		}
		path = a.PhoneID + "/calls"
		body = payload
	default:
		return nil, errors.New("unsupported WhatsApp operation")
	}
	// A read receipt sends no message, so it does not use send throughput.
	if !validateOnly && in.tracking == "" && path == a.PhoneID+"/messages" && in.Operation != "messages.read" && s.Outbox != nil {
		allowed, err := s.Outbox.Permit(ctx, a)
		if err != nil {
			return nil, errors.New("rate limiter unavailable; not submitted")
		}
		if !allowed {
			return nil, ErrBusy
		}
	}
	if in.tracking != "" && path == a.PhoneID+"/messages" {
		if payload, ok := body.(map[string]any); ok {
			payload["biz_opaque_callback_data"] = "relay-outbox:" + in.tracking
		}
	}
	if validateOnly {
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > 65536 {
			return nil, errors.New("request too large")
		}
		return nil, nil
	}
	return s.graphSubmission(ctx, a, method, path, query, body, before)
}
func (s *Service) graph(ctx context.Context, a Account, method, path string, query url.Values, body any) (map[string]any, error) {
	return s.graphSubmission(ctx, a, method, path, query, body, nil)
}
func (s *Service) graphSubmission(ctx context.Context, a Account, method, path string, query url.Values, body any, before func() error) (map[string]any, error) {
	encoded, e := json.Marshal(body)
	if e != nil {
		return nil, errors.New("invalid request body")
	}
	if len(encoded) > 65536 {
		return nil, errors.New("request too large")
	}
	// Meta otherwise writes error text in the business account's language.
	params := url.Values{"locale": {"en_US"}}
	for key, values := range query {
		params[key] = values
	}
	endpoint := s.base + "/" + a.Version + "/" + path + "?" + params.Encode()
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(encoded)
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if e != nil {
		return nil, errors.New("cannot build provider request")
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	if before != nil {
		if err := before(); err != nil {
			return nil, err
		}
	}
	return s.request(req)
}

func (s *Service) request(req *http.Request) (map[string]any, error) {
	response, e := s.client.Do(req)
	if e != nil {
		err := errors.New("provider unavailable; outcome may be unknown, do not blindly retry")
		if req.Method == http.MethodGet {
			return nil, &deferredError{err}
		}
		return nil, err
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if e != nil && req.Method == http.MethodGet {
		return nil, &deferredError{errors.New("provider response interrupted")}
	}
	if (e != nil || len(data) > 1<<20) && response.StatusCode < 300 {
		return nil, errors.New("provider response invalid or too large")
	}
	var result map[string]any
	decodeErr := json.Unmarshal(data, &result)
	if response.StatusCode >= 300 || result["error"] != nil {
		rejection := &ProviderError{Status: response.StatusCode, Transient: req.Method == http.MethodGet && (response.StatusCode >= 500 || response.StatusCode == 408)}
		var envelope struct {
			Error struct {
				Code      int    `json:"code"`
				Subcode   int    `json:"error_subcode"`
				Transient bool   `json:"is_transient"`
				Title     string `json:"error_user_title"`
				Message   string `json:"error_user_msg"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			rejection.Code, rejection.Subcode = envelope.Error.Code, envelope.Error.Subcode
			rejection.Transient = rejection.Transient || envelope.Error.Transient
			rejection.Title, rejection.Message = safeText(envelope.Error.Title), safeText(envelope.Error.Message)
		}
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil {
			rejection.RetryAfter = max(0, min(seconds, 3600))
		} else if stamp, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
			rejection.RetryAfter = max(0, min(int(time.Until(stamp).Seconds())+1, 3600))
		}
		return nil, rejection
	}
	if decodeErr != nil || result == nil {
		return nil, errors.New("invalid provider response")
	}
	// Graph paging URLs may embed access tokens. Return only opaque cursors.
	if paging, ok := result["paging"].(map[string]any); ok {
		next, _ := paging["next"].(string)
		result["paging"] = map[string]any{"cursors": paging["cursors"], "has_more": next != ""}
	}
	return result, nil
}

func (s *Service) checkGroup(ctx context.Context, a Account, id string) error {
	after := ""
	for page := 0; page < 10; page++ {
		query := url.Values{"limit": {"100"}}
		if after != "" {
			query.Set("after", after)
		}
		result, err := s.graph(ctx, a, "GET", a.PhoneID+"/groups", query, nil)
		if err != nil {
			return err
		}
		var rows []any
		if data, ok := result["data"].(map[string]any); ok {
			rows, _ = data["groups"].([]any)
		} else {
			rows, _ = result["data"].([]any)
		}
		for _, row := range rows {
			if group, ok := row.(map[string]any); ok && group["id"] == id {
				return nil
			}
		}
		previous := after
		after = ""
		if paging, ok := result["paging"].(map[string]any); ok {
			if cursors, ok := paging["cursors"].(map[string]any); ok {
				after, _ = cursors["after"].(string)
			}
		}
		if after == "" || after == previous {
			break
		}
	}
	return ErrGroupNotFound
}

// ProviderError distinguishes an explicit rejection from a lost response.
// It keeps only Meta's user-facing title and message, never the raw body.
type ProviderError struct {
	Status     int    `json:"http_status"`
	Code       int    `json:"code,omitempty"`
	Subcode    int    `json:"subcode,omitempty"`
	Transient  bool   `json:"transient,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
	Title      string `json:"title,omitempty"`
	Message    string `json:"message,omitempty"`
}

func (e *ProviderError) Retryable() bool {
	return e.Status == 429 || e.Transient || e.Code == 130429 || e.Code == 131056
}

type preSubmissionError struct{ error }

func (e *preSubmissionError) Unwrap() error { return e.error }

type deferredError struct{ error }

func (e *deferredError) Unwrap() error { return e.error }

func normalizeParameterFormat(in *Input) error {
	in.ParameterFormat = strings.ToLower(in.ParameterFormat)
	if in.ParameterFormat != "" && in.ParameterFormat != "positional" && in.ParameterFormat != "named" {
		return errors.New("invalid template parameter format")
	}
	return nil
}

func (e *ProviderError) Error() string {
	return "provider rejected operation" + e.Detail() + "; inspect delivery status before retry"
}

// Detail is the safe provider code and user text, empty when Meta sent none.
func (e *ProviderError) Detail() string {
	text := ""
	if e.Code != 0 {
		text = " (code " + strconv.Itoa(e.Code) + ")"
	}
	for _, part := range []string{e.Title, e.Message} {
		if part != "" {
			text += ": " + part
		}
	}
	return text
}

// safeText bounds provider text to one printable line for operator display.
func safeText(value string) string {
	value = strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
	if runes := []rune(value); len(runes) > 300 {
		value = string(runes[:300])
	}
	return value
}
