package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// Flow IDs are Graph-global; verify membership before every ID-scoped operation.
func (s *Service) checkFlow(ctx context.Context, a Account, id string) error {
	after := ""
	for page := 0; page < 20; page++ {
		q := url.Values{"fields": {"id"}, "limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		result, err := s.graph(ctx, a, "GET", a.WABAID+"/flows", q, nil)
		if err != nil {
			return err
		}
		rows, _ := result["data"].([]any)
		for _, row := range rows {
			if value, ok := row.(map[string]any); ok && value["id"] == id {
				return nil
			}
		}
		paging, _ := result["paging"].(map[string]any)
		cursors, _ := paging["cursors"].(map[string]any)
		next, _ := cursors["after"].(string)
		if paging["has_more"] != true || next == "" || next == after {
			break
		}
		after = next
	}
	return errors.New("flow does not belong to this account or exceeds the ownership lookup limit")
}

func (s *Service) flow(ctx context.Context, a Account, in Input, validateOnly bool) (map[string]any, error) {
	if !a.Templates || !digits.MatchString(a.WABAID) {
		return nil, errors.New("WhatsApp business management is not enabled")
	}
	switch in.Operation {
	case "flows.list", "flows.get", "flows.json", "flows.assets", "flows.create", "flows.update", "flows.upload", "flows.publish", "flows.deprecate", "flows.delete":
	default:
		return nil, errors.New("unsupported flow operation")
	}
	if in.Operation != "flows.list" && in.Operation != "flows.create" && !digits.MatchString(in.ID) {
		return nil, errors.New("valid flow ID required")
	}
	if len(in.After) > 2048 {
		return nil, errors.New("invalid flow cursor")
	}
	body := map[string]any{}
	if in.Operation == "flows.create" || in.Operation == "flows.update" {
		if len(in.Name) > 200 || strings.ContainsAny(in.Name, "\x00\r\n") || (in.Operation == "flows.create" && strings.TrimSpace(in.Name) == "") {
			return nil, errors.New("flow name required, up to 200 characters")
		}
		if in.Name != "" {
			body["name"] = in.Name
		}
		allowed := map[string]bool{"SIGN_UP": true, "SIGN_IN": true, "APPOINTMENT_BOOKING": true, "LEAD_GENERATION": true, "CONTACT_US": true, "CUSTOMER_SUPPORT": true, "SURVEY": true, "OTHER": true}
		if len(in.Categories) > 8 || (in.Operation == "flows.create" && len(in.Categories) == 0) {
			return nil, errors.New("select flow categories")
		}
		for _, category := range in.Categories {
			if !allowed[category] {
				return nil, errors.New("invalid flow category")
			}
		}
		if len(in.Categories) > 0 {
			body["categories"] = in.Categories
		}
		if in.EndpointURI != "" {
			u, err := url.Parse(in.EndpointURI)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(in.EndpointURI) > 2048 {
				return nil, errors.New("flow endpoint must be an HTTPS URL")
			}
			body["endpoint_uri"] = in.EndpointURI
		}
		if len(body) == 0 {
			return nil, errors.New("supply flow metadata")
		}
	}
	if in.Operation == "flows.upload" {
		var document map[string]any
		if len(in.FlowJSON) == 0 || len(in.FlowJSON) > 1<<20 || json.Unmarshal(in.FlowJSON, &document) != nil || document == nil {
			return nil, errors.New("flow JSON must be an object up to 1 MiB")
		}
	}
	if validateOnly {
		return map[string]any{"valid": true}, nil
	}
	if in.Operation != "flows.list" && in.Operation != "flows.create" {
		if err := s.checkFlow(ctx, a, in.ID); err != nil {
			return nil, err
		}
	}
	switch in.Operation {
	case "flows.list":
		q := url.Values{"limit": {"50"}, "fields": {"id,name,status,categories,validation_errors"}}
		if in.After != "" {
			q.Set("after", in.After)
		}
		return s.graph(ctx, a, "GET", a.WABAID+"/flows", q, nil)
	case "flows.get":
		return s.graph(ctx, a, "GET", in.ID, url.Values{"fields": {"id,name,status,categories,validation_errors,endpoint_uri,json_version,data_api_version,health_status"}}, nil)
	case "flows.json":
		return s.flowJSON(ctx, a, in.ID)
	case "flows.assets":
		return s.graph(ctx, a, "GET", in.ID+"/assets", nil, nil)
	case "flows.create":
		return s.graph(ctx, a, "POST", a.WABAID+"/flows", nil, body)
	case "flows.update":
		return s.graph(ctx, a, "POST", in.ID, nil, body)
	case "flows.delete":
		return s.graph(ctx, a, "DELETE", in.ID, nil, nil)
	case "flows.publish", "flows.deprecate":
		return s.graph(ctx, a, "POST", in.ID+"/"+strings.TrimPrefix(in.Operation, "flows."), nil, map[string]any{})
	case "flows.upload":
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		if err := writer.WriteField("name", "flow.json"); err != nil {
			return nil, err
		}
		if err := writer.WriteField("asset_type", "FLOW_JSON"); err != nil {
			return nil, err
		}
		part, err := writer.CreateFormFile("file", "flow.json")
		if err != nil {
			return nil, err
		}
		if _, err = part.Write(in.FlowJSON); err != nil {
			return nil, err
		}
		if err = writer.Close(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", s.base+"/"+a.Version+"/"+in.ID+"/assets", &buffer)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+a.AccessToken)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return s.request(req)
	}
	return nil, errors.New("unsupported flow operation")
}

// The asset URL is obtained only after account ownership verification. Signed CDN
// URLs and the provider bearer never enter the Core response or browser.
func (s *Service) flowJSON(ctx context.Context, a Account, id string) (map[string]any, error) {
	assets, err := s.graph(ctx, a, "GET", id+"/assets", nil, nil)
	if err != nil {
		return nil, err
	}
	rows, _ := assets["data"].([]any)
	for _, row := range rows {
		asset, ok := row.(map[string]any)
		if !ok || asset["asset_type"] != "FLOW_JSON" {
			continue
		}
		raw, _ := asset["download_url"].(string)
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" || !strings.HasSuffix(u.Hostname(), ".fbcdn.net") {
			return nil, errors.New("untrusted Flow asset destination")
		}
		request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if err != nil {
			return nil, errors.New("invalid Flow asset destination")
		}
		response, err := s.client.Do(request)
		if err != nil {
			return nil, errors.New("Flow asset unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, errors.New("Flow asset download failed")
		}
		rawJSON, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(rawJSON) > 1<<20 {
			return nil, errors.New("Flow asset exceeds 1 MiB")
		}
		var document map[string]any
		if json.Unmarshal(rawJSON, &document) != nil || document == nil {
			return nil, errors.New("invalid Flow JSON asset")
		}
		return map[string]any{"flow_json": document}, nil
	}
	return map[string]any{"flow_json": nil}, nil
}

func (s *Service) sendFlow(ctx context.Context, a Account, in Input, validateOnly bool) (map[string]any, error) {
	if !a.Templates || !digits.MatchString(in.ID) || !digits.MatchString(in.To) || len(in.To) < 7 || len(in.To) > 15 || strings.TrimSpace(in.Body) == "" || len([]rune(in.Body)) > 1024 || strings.TrimSpace(in.FlowCTA) == "" || len([]rune(in.FlowCTA)) > 20 || !identifier.MatchString(in.FlowScreen) || !identifier.MatchString(in.FlowToken) {
		return nil, errors.New("valid published Flow, recipient, message, button, screen and token required")
	}
	raw, err := json.Marshal(in.FlowData)
	if err != nil || len(raw) > 12000 {
		return nil, errors.New("Flow initial data exceeds 12000 bytes")
	}
	if in.ReplyTo != "" && !callIdentifier.MatchString(in.ReplyTo) {
		return nil, errors.New("invalid reply reference")
	}
	if validateOnly {
		return map[string]any{"valid": true}, nil
	}
	if err := s.checkFlow(ctx, a, in.ID); err != nil {
		return nil, err
	}
	detail, err := s.graph(ctx, a, "GET", in.ID, url.Values{"fields": {"status"}}, nil)
	if err != nil {
		return nil, err
	}
	if detail["status"] != "PUBLISHED" {
		return nil, errors.New("only published Flows can be sent")
	}
	actionPayload := map[string]any{"screen": in.FlowScreen}
	if len(in.FlowData) > 0 {
		actionPayload["data"] = in.FlowData
	}
	body := map[string]any{"messaging_product": "whatsapp", "to": in.To, "recipient_type": "individual", "type": "interactive", "interactive": map[string]any{"type": "flow", "body": map[string]string{"text": in.Body}, "action": map[string]any{"name": "flow", "parameters": map[string]any{"flow_message_version": "3", "flow_action": "navigate", "flow_token": in.FlowToken, "flow_id": in.ID, "flow_cta": in.FlowCTA, "flow_action_payload": actionPayload}}}}
	if in.ReplyTo != "" {
		body["context"] = map[string]string{"message_id": in.ReplyTo}
	}
	if in.tracking != "" {
		body["biz_opaque_callback_data"] = in.tracking
	}
	return s.graph(ctx, a, "POST", a.PhoneID+"/messages", nil, body)
}
