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

func TestPrivateAttachmentUploadAndSend(t *testing.T) {
	uploaded, sent := 0, 0
	raw := []byte("%PDF-1.4\nsynthetic fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Error("missing provider credential")
		}
		if r.URL.Path == "/v25.0/111/media" {
			uploaded++
			if err := r.ParseMultipartForm(5 << 20); err != nil {
				t.Fatal(err)
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("messaging_product") != "whatsapp" || r.FormValue("type") != "application/pdf" {
				t.Error("missing upload fields")
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			body, _ := io.ReadAll(file)
			if string(body) != string(raw) || header.Filename != "invoice.pdf" {
				t.Error("upload changed")
			}
			io.WriteString(w, `{"id":"123456"}`)
			return
		}
		if r.URL.Path == "/v25.0/111/messages" {
			sent++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["document"].(map[string]any)["id"] != "123456" || body["to"] != "919999999999" {
				t.Error("wrong media recipient")
			}
			io.WriteString(w, `{"messages":[{"id":"wamid.fixture"}]}`)
			return
		}
		t.Error("unexpected route")
		w.WriteHeader(400)
	}))
	defer server.Close()
	service, err := NewWithHTTPClient([]Account{testAccount()}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service.base = server.URL
	input := Input{Operation: "messages.media", To: "919999999999", MediaType: "document", MIMEType: "application/pdf", Filename: "invoice.pdf", ContentBase64: base64.StdEncoding.EncodeToString(raw), Caption: "Invoice"}
	if _, err = service.execute(context.Background(), testAccount(), input, true); err != nil {
		t.Fatal(err)
	}
	if uploaded != 0 || sent != 0 {
		t.Fatal("validation performed provider mutations")
	}
	if _, err = service.Execute(context.Background(), testAccount(), input); err != nil {
		t.Fatal(err)
	}
	if uploaded != 1 || sent != 1 {
		t.Fatal("upload/send count")
	}
	for _, change := range []func(*Input){func(in *Input) { in.MediaID = "123" }, func(in *Input) { in.ContentBase64 = "invalid" }, func(in *Input) { in.MIMEType = "text/html" }, func(in *Input) { in.Filename = "../invoice.pdf" }, func(in *Input) { in.Caption = strings.Repeat("x", 1025) }} {
		bad := input
		change(&bad)
		if _, err = service.execute(context.Background(), testAccount(), bad, true); err == nil {
			t.Fatal("invalid upload accepted")
		}
	}
	if uploaded != 1 || sent != 1 {
		t.Fatal("invalid input submitted")
	}
}
