package whatsapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func profileText(value string) *string { return &value }

func TestProfileReadKeepsOnlyProfileFields(t *testing.T) {
	s := fakeGraph(t, func(r *http.Request) string {
		if r.Method != "GET" || r.URL.Path != "/v25.0/111/whatsapp_business_profile" || !strings.Contains(r.URL.Query().Get("fields"), "about") {
			t.Errorf("profile read: %s %s", r.Method, r.URL)
		}
		return `{"data":[{"about":"Fabrics for every season","email":"hello@example.com","websites":["https://example.com"],"vertical":"APPAREL","messaging_product":"whatsapp","id":"internal"}]}`
	})
	result, err := s.Execute(context.Background(), testAccount(), Input{Operation: "profile.get"})
	profile, _ := result["profile"].(map[string]any)
	if err != nil || profile["about"] != "Fabrics for every season" || profile["vertical"] != "APPAREL" || profile["id"] != nil || profile["messaging_product"] != nil {
		t.Fatalf("profile %v %v", result, err)
	}
}

func TestProfileUpdateSendsOnlyChangedFields(t *testing.T) {
	var body map[string]any
	s := fakeGraph(t, func(r *http.Request) string {
		if r.Method != "POST" || r.URL.Path != "/v25.0/111/whatsapp_business_profile" {
			t.Errorf("profile update: %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		return `{"success":true}`
	})
	in := Input{Operation: "profile.update", Profile: &BusinessProfile{About: profileText(" Fabrics for every season "), Websites: &[]string{"https://example.com"}}}
	if result, err := s.Execute(context.Background(), testAccount(), in); err != nil || result["updated"] != true {
		t.Fatal(result, err)
	}
	if body["about"] != "Fabrics for every season" || body["messaging_product"] != "whatsapp" || body["email"] != nil || len(body["websites"].([]any)) != 1 {
		t.Fatalf("profile body %v", body)
	}
}

func TestProfileUpdateRefusesFieldsMetaRefuses(t *testing.T) {
	s := fakeGraph(t, func(r *http.Request) string {
		t.Errorf("an invalid profile reached Meta: %s", r.URL.Path)
		return `{}`
	})
	for name, profile := range map[string]*BusinessProfile{
		"nothing":       {},
		"long about":    {About: profileText(strings.Repeat("a", 140))},
		"empty about":   {About: profileText("  ")},
		"bad email":     {Email: profileText("Sam Customer <sam@example.com>")},
		"three sites":   {Websites: &[]string{"https://a.example.com", "https://b.example.com", "https://c.example.com"}},
		"ftp site":      {Websites: &[]string{"ftp://example.com"}},
		"unknown field": {Vertical: profileText("SPACE")},
	} {
		if _, err := s.Execute(context.Background(), testAccount(), Input{Operation: "profile.update", Profile: profile}); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestProfilePhotoIsUploadedThenSetByHandle(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	paths := []string{}
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v25.0/333/uploads":
			io.WriteString(w, `{"id":"upload:photo=="}`)
		case "/v25.0/upload:photo==":
			io.ReadAll(r.Body)
			io.WriteString(w, `{"h":"4:photo-handle"}`)
		default:
			json.NewDecoder(r.Body).Decode(&body)
			io.WriteString(w, `{"success":true}`)
		}
	}))
	defer server.Close()
	a := testAccount()
	a.AppID = "333"
	s, _ := New([]Account{a})
	s.base = server.URL
	in := Input{Operation: "profile.update", MIMEType: "image/png", ContentBase64: base64.StdEncoding.EncodeToString(png)}
	if _, err := s.Execute(context.Background(), a, in); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 || paths[2] != "POST /v25.0/111/whatsapp_business_profile" || body["profile_picture_handle"] != "4:photo-handle" {
		t.Fatalf("photo upload %v %v", paths, body)
	}
	in.MIMEType, in.ContentBase64 = "application/pdf", base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 fixture"))
	if _, err := s.Execute(context.Background(), a, in); err == nil {
		t.Fatal("a PDF profile photo was accepted")
	}
}
