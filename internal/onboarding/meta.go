package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// alreadyRequested lists the Meta error code and subcode pairs that mean an
// smb_app_data sync was already requested for the number. The pair is the
// simulator's choice; confirm it on a Meta test number before customers.
var alreadyRequested = [][2]int{{4, 2593111}}

// historyDeclined is the history webhook error when the customer declines to share chats.
const historyDeclined = 2593109

// registerRateLimited is Meta's error for too many register calls in 72 hours.
const registerRateLimited = 133016

const phoneFields = "id,display_phone_number,verified_name,code_verification_status,name_status,platform_type,status,quality_rating,whatsapp_business_manager_messaging_limit"
const phoneStatusFields = "id,display_phone_number,verified_name,name_status,platform_type,is_on_biz_app,status,quality_rating,whatsapp_business_manager_messaging_limit"

// metaError is one Graph rejection. It keeps only Meta's codes and safe text.
type metaError struct {
	Status, Code, Subcode int
	Type, Message         string
	Transient             bool
}

func (e *metaError) Error() string {
	return "Meta rejected the request (code " + strconv.Itoa(e.Code) + "): " + e.Message
}

// retryLater reports an outcome that a later attempt can change.
func (e *metaError) retryLater() bool {
	return e.Transient || e.Status >= 500 || e.Status == 429 || e.Status == 408 || e.Code == 1 || e.Code == 2
}

func (e *metaError) alreadyRequested() bool {
	return slices.Contains(alreadyRequested, [2]int{e.Code, e.Subcode})
}

// errUnavailable is a lost request or response; the outcome is unknown.
var errUnavailable = errors.New("Meta could not be reached")

// graph calls the Meta Graph API for one signup. It is the only Meta-aware code.
type graph struct {
	client  *http.Client
	base    string
	version string
	appID   string
	secret  string
}

func (g graph) call(ctx context.Context, method, path string, query url.Values, bearer string, body any) (map[string]any, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("locale", "en_US")
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, g.base+"/"+g.version+"/"+path+"?"+query.Encode(), reader)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, errUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, errUnavailable
	}
	var result map[string]any
	decodeErr := json.Unmarshal(raw, &result)
	if response.StatusCode >= 300 || result["error"] != nil {
		return nil, parseMetaError(response.StatusCode, raw)
	}
	if decodeErr != nil || result == nil {
		return nil, errUnavailable
	}
	return result, nil
}

func parseMetaError(status int, raw []byte) *metaError {
	failure := &metaError{Status: status}
	var envelope struct {
		Error struct {
			Code      int    `json:"code"`
			Subcode   int    `json:"error_subcode"`
			Type      string `json:"type"`
			Message   string `json:"message"`
			UserMsg   string `json:"error_user_msg"`
			Transient bool   `json:"is_transient"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		failure.Code, failure.Subcode, failure.Type, failure.Transient = envelope.Error.Code, envelope.Error.Subcode, envelope.Error.Type, envelope.Error.Transient
		failure.Message = safeText(envelope.Error.UserMsg)
		if failure.Message == "" {
			failure.Message = safeText(envelope.Error.Message)
		}
	}
	return failure
}

// exchange is G1: the 30-second code for a business token.
func (g graph) exchange(ctx context.Context, code string) (string, error) {
	result, err := g.call(ctx, http.MethodGet, "oauth/access_token", url.Values{"client_id": {g.appID}, "client_secret": {g.secret}, "code": {code}}, "", nil)
	if err != nil {
		return "", err
	}
	token, _ := result["access_token"].(string)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errUnavailable
	}
	return token, nil
}

// tokenTargets is G2: it checks the token belongs to the app and returns the
// WABAs it may manage and message.
func (g graph) tokenTargets(ctx context.Context, token string) ([]string, error) {
	result, err := g.call(ctx, http.MethodGet, "debug_token", url.Values{"input_token": {token}}, g.appID+"|"+g.secret, nil)
	if err != nil {
		return nil, err
	}
	data, _ := result["data"].(map[string]any)
	if valid, _ := data["is_valid"].(bool); !valid || text(data["app_id"]) != g.appID {
		return nil, &stepError{Code: "token_invalid", Message: "Meta did not accept the access from Facebook. Start again."}
	}
	scopes := map[string][]string{}
	granular, _ := data["granular_scopes"].([]any)
	for _, item := range granular {
		scope, _ := item.(map[string]any)
		targets, _ := scope["target_ids"].([]any)
		for _, target := range targets {
			scopes[text(scope["scope"])] = append(scopes[text(scope["scope"])], text(target))
		}
	}
	manage, message := scopes["whatsapp_business_management"], scopes["whatsapp_business_messaging"]
	wabas := []string{}
	for _, id := range manage {
		if id != "" && slices.Contains(message, id) && !slices.Contains(wabas, id) {
			wabas = append(wabas, id)
		}
	}
	if len(wabas) == 0 {
		return nil, &stepError{Code: "app_not_approved", Message: "Relay cannot reach this WhatsApp account yet."}
	}
	return wabas, nil
}

// waba is G3.
func (g graph) waba(ctx context.Context, token, waba string) (map[string]any, error) {
	return g.call(ctx, http.MethodGet, waba, url.Values{"fields": {"id,name,owner_business_info,currency"}}, token, nil)
}

// fundingReady tells whether the account has a payment method. Meta lets only
// Solution Partners read primary_funding_id, so a Tech Provider gets nil.
func (g graph) fundingReady(ctx context.Context, token, waba string) *bool {
	account, err := g.call(ctx, http.MethodGet, waba, url.Values{"fields": {"primary_funding_id"}}, token, nil)
	if err != nil {
		return nil
	}
	ready := text(account["primary_funding_id"]) != ""
	return &ready
}

// phones is G4, up to 100 numbers.
func (g graph) phones(ctx context.Context, token, waba string) ([]map[string]any, error) {
	result, err := g.call(ctx, http.MethodGet, waba+"/phone_numbers", url.Values{"fields": {phoneFields}, "limit": {"100"}}, token, nil)
	if err != nil {
		return nil, err
	}
	rows, _ := result["data"].([]any)
	phones := []map[string]any{}
	for _, row := range rows {
		if phone, ok := row.(map[string]any); ok && text(phone["id"]) != "" {
			phones = append(phones, phone)
		}
	}
	return phones, nil
}

// subscribe is G5; on an unknown outcome subscribed checks G6.
func (g graph) subscribe(ctx context.Context, token, waba string) error {
	_, err := g.call(ctx, http.MethodPost, waba+"/subscribed_apps", nil, token, nil)
	return err
}

func (g graph) subscribed(ctx context.Context, token, waba string) (bool, error) {
	result, err := g.call(ctx, http.MethodGet, waba+"/subscribed_apps", nil, token, nil)
	if err != nil {
		return false, err
	}
	rows, _ := result["data"].([]any)
	for _, row := range rows {
		app, _ := row.(map[string]any)
		data, _ := app["whatsapp_business_api_data"].(map[string]any)
		if text(data["id"]) == g.appID {
			return true, nil
		}
	}
	return false, nil
}

// register is G7 with the number's stored PIN.
func (g graph) register(ctx context.Context, token, phone, pin string) error {
	_, err := g.call(ctx, http.MethodPost, phone+"/register", nil, token, map[string]string{"messaging_product": "whatsapp", "pin": pin})
	return err
}

// phoneStatus is G8.
func (g graph) phoneStatus(ctx context.Context, token, phone string) (map[string]any, error) {
	return g.call(ctx, http.MethodGet, phone, url.Values{"fields": {phoneStatusFields}}, token, nil)
}

// smbSync is G9 (smb_app_state_sync) and G10 (history).
func (g graph) smbSync(ctx context.Context, token, phone, kind string) error {
	_, err := g.call(ctx, http.MethodPost, phone+"/smb_app_data", nil, token, map[string]string{"messaging_product": "whatsapp", "sync_type": kind})
	return err
}

// phoneProfile keeps the safe fields of a Graph phone number.
func phoneProfile(phone map[string]any) map[string]any {
	profile := map[string]any{}
	for _, key := range []string{"id", "display_phone_number", "verified_name", "name_status", "quality_rating", "platform_type", "status", "code_verification_status"} {
		if value := text(phone[key]); value != "" {
			profile[key] = value
		}
	}
	if limit := text(phone["whatsapp_business_manager_messaging_limit"]); limit != "" {
		profile["messaging_limit"] = limit
	}
	return profile
}

func text(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// safeText bounds provider text to one printable line.
func safeText(value string) string {
	value = strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
	if runes := []rune(value); len(runes) > 300 {
		value = string(runes[:300])
	}
	return value
}
