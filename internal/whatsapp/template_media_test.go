package whatsapp

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTemplateSampleUpload(t *testing.T) {
	raw := "%PDF-1.7\nfixture"
	for _, session := range []string{"upload:fixture==?sig=signed_value", "https://example.invalid/upload", "upload:../messages", "upload:fixture?access_token=bad"} {
		t.Run(session, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if r.Method != "POST" || r.URL.Path != "/v25.0/333/uploads" || r.URL.Query().Get("file_length") != "16" || r.URL.Query().Get("file_type") != "application/pdf" || r.Header.Get("Authorization") != "Bearer provider-secret" {
						t.Errorf("incorrect session request")
					}
					io.WriteString(w, `{"id":"`+session+`"}`)
					return
				}
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/v25.0/upload:fixture==" || r.URL.Query().Get("sig") != "signed_value" || string(body) != raw || r.Header.Get("file_offset") != "0" || r.Header.Get("Authorization") != "OAuth provider-secret" {
					t.Errorf("incorrect binary upload")
				}
				io.WriteString(w, `{"h":"4:sample-handle"}`)
			}))
			defer server.Close()
			a := testAccount()
			a.AppID = "333"
			s, _ := New([]Account{a})
			s.base = server.URL
			in := Input{Operation: "templates.media_upload", MediaType: "document", MIMEType: "application/pdf", Filename: "sample.pdf", ContentBase64: base64.StdEncoding.EncodeToString([]byte(raw))}
			result, err := s.Execute(context.Background(), a, in)
			if session == "upload:fixture==?sig=signed_value" {
				if err != nil || result["handle"] != "4:sample-handle" || calls != 2 {
					t.Fatalf("upload failed: %v %v %d", result, err, calls)
				}
			} else if err == nil || calls != 1 {
				t.Fatalf("unsafe session followed: %v %d", err, calls)
			}
			calls = 0
			a.AppID = ""
			if _, err = s.Execute(context.Background(), a, in); err == nil || calls != 0 {
				t.Fatal("missing app ID was accepted")
			}
			a.AppID = "333"
			a.Templates = false
			if _, err = s.Execute(context.Background(), a, in); err == nil || calls != 0 {
				t.Fatal("disabled templates were accepted")
			}
		})
	}
}
