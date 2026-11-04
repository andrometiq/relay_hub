package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"unicode/utf8"
)

const whatsappProfileFields = "about,address,description,email,profile_picture_url,websites,vertical"

// BusinessProfile is the public WhatsApp profile of one number. A nil field keeps its value.
type BusinessProfile struct {
	About       *string   `json:"about,omitempty"`
	Address     *string   `json:"address,omitempty"`
	Description *string   `json:"description,omitempty"`
	Email       *string   `json:"email,omitempty"`
	Websites    *[]string `json:"websites,omitempty"`
	Vertical    *string   `json:"vertical,omitempty"`
}

// profileVerticals are the business categories Meta accepts.
var profileVerticals = map[string]bool{"UNDEFINED": true, "OTHER": true, "AUTO": true, "BEAUTY": true, "APPAREL": true, "EDU": true,
	"ENTERTAIN": true, "EVENT_PLAN": true, "FINANCE": true, "GROCERY": true, "GOVT": true, "HOTEL": true, "HEALTH": true, "NONPROFIT": true,
	"PROF_SERVICES": true, "RETAIL": true, "TRAVEL": true, "RESTAURANT": true, "NOT_A_BIZ": true}

// readProfile is profile.get: the number's business profile as Meta shows it.
func (s *Service) readProfile(ctx context.Context, a Account) (map[string]any, error) {
	result, err := s.graph(ctx, a, http.MethodGet, a.PhoneID+"/whatsapp_business_profile", url.Values{"fields": {whatsappProfileFields}}, nil)
	if err != nil {
		return nil, err
	}
	rows, _ := result["data"].([]any)
	profile := map[string]any{}
	if len(rows) > 0 {
		row, _ := rows[0].(map[string]any)
		for _, name := range strings.Split(whatsappProfileFields, ",") {
			if value, ok := row[name]; ok {
				profile[name] = value
			}
		}
	}
	return map[string]any{"profile": profile}, nil
}

// updateProfile is profile.update. A photo in content_base64 goes through the
// Resumable Upload API first; Meta takes its handle.
func (s *Service) updateProfile(ctx context.Context, a Account, in Input, validateOnly bool) (map[string]any, error) {
	body, err := profileBody(in.Profile)
	if err != nil {
		return nil, err
	}
	var photo []byte
	var photoType string
	if in.ContentBase64 != "" {
		in.MediaType = "image"
		if photo, photoType, err = attachmentBytes(in); err != nil {
			return nil, err
		}
		if len(photo) > 5<<20 || !digits.MatchString(a.AppID) {
			return nil, errors.New("the profile photo must be a JPEG or PNG image up to 5 MB")
		}
	}
	if len(body) == 1 && photo == nil {
		return nil, errors.New("change at least one profile field")
	}
	if validateOnly {
		return map[string]any{"valid": true}, nil
	}
	if photo != nil {
		handle, err := s.resumableUpload(ctx, a, photo, photoType, "profile")
		if err != nil {
			return nil, err
		}
		body["profile_picture_handle"] = handle
	}
	if _, err = s.graph(ctx, a, http.MethodPost, a.PhoneID+"/whatsapp_business_profile", nil, body); err != nil {
		return nil, err
	}
	return map[string]any{"updated": true}, nil
}

// profileBody checks the fields against Meta's limits.
func profileBody(profile *BusinessProfile) (map[string]any, error) {
	body := map[string]any{"messaging_product": "whatsapp"}
	if profile == nil {
		return body, nil
	}
	for name, field := range map[string]struct {
		value *string
		limit int
	}{"about": {profile.About, 139}, "address": {profile.Address, 256}, "description": {profile.Description, 512}, "email": {profile.Email, 128}} {
		if field.value == nil {
			continue
		}
		value := strings.TrimSpace(*field.value)
		if utf8.RuneCountInString(value) > field.limit || strings.ContainsAny(value, "\x00") || (name == "about" && value == "") {
			return nil, errors.New("the profile " + name + " is too long or empty")
		}
		if name == "email" && value != "" {
			if address, err := mail.ParseAddress(value); err != nil || address.Address != value {
				return nil, errors.New("enter a valid profile email address")
			}
		}
		body[name] = value
	}
	if profile.Websites != nil {
		if len(*profile.Websites) > 2 {
			return nil, errors.New("a profile may list at most two websites")
		}
		for _, site := range *profile.Websites {
			u, err := url.Parse(site)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(site) > 256 {
				return nil, errors.New("profile websites must be http or https addresses")
			}
		}
		body["websites"] = *profile.Websites
	}
	if profile.Vertical != nil {
		if !profileVerticals[*profile.Vertical] {
			return nil, errors.New("unsupported business category")
		}
		body["vertical"] = *profile.Vertical
	}
	return body, nil
}
