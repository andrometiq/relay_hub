package whatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/attachment"
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

func TestMediaUploadReturnsIDWithoutSending(t *testing.T) {
	uploaded := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v25.0/111/media" {
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		uploaded++
		io.WriteString(w, `{"id":"987654"}`)
	}))
	defer server.Close()
	service, err := NewWithHTTPClient([]Account{testAccount()}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service.base = server.URL
	input := Input{Operation: "messages.media_upload", MediaType: "document", MIMEType: "application/pdf", Filename: "invoice.pdf", ContentBase64: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\nsynthetic fixture"))}
	if result, err := service.execute(context.Background(), testAccount(), input, true); err != nil || result["valid"] != true || uploaded != 0 {
		t.Fatal("validation", result, err, uploaded)
	}
	result, err := service.Execute(context.Background(), testAccount(), input)
	if err != nil || result["id"] != "987654" || uploaded != 1 {
		t.Fatal("upload", result, err, uploaded)
	}
	for _, change := range []func(*Input){func(in *Input) { in.ContentBase64 = "" }, func(in *Input) { in.MediaURL = "https://example.test/a.pdf" }, func(in *Input) { in.MediaType = "image" }, func(in *Input) { in.MIMEType = "text/html" }, func(in *Input) { in.Filename = "../invoice.pdf" }} {
		bad := input
		change(&bad)
		if _, err = service.Execute(context.Background(), testAccount(), bad); err == nil {
			t.Fatal("invalid upload accepted", bad)
		}
	}
	if uploaded != 1 {
		t.Fatal("invalid input uploaded")
	}
}

func TestAttachmentAcceptsTheLimitAndRefusesOneByteMore(t *testing.T) {
	pdf := func(size int) Input {
		raw := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), size-9)...)
		return Input{MediaType: "document", MIMEType: "application/pdf", Filename: "large.pdf", ContentBase64: base64.StdEncoding.EncodeToString(raw)}
	}
	if raw, _, err := attachmentBytes(pdf(attachment.MaxBytes)); err != nil || len(raw) != attachment.MaxBytes {
		t.Fatal("attachment at the limit refused", err)
	}
	if _, _, err := attachmentBytes(pdf(attachment.MaxBytes + 1)); err == nil {
		t.Fatal("attachment above the limit accepted")
	}
}

func TestBulkAcceptsTextAndReferencedMediaOnly(t *testing.T) {
	for _, in := range []Input{
		{Operation: "templates.send"},
		{Operation: "messages.send"},
		{Operation: "messages.media", MediaID: "123"},
		{Operation: "messages.media", MediaURL: "https://example.test/a.jpg"},
	} {
		if err := queueable(in, true); err != nil {
			t.Fatal("bulk refused", in.Operation, err)
		}
	}
	inline := Input{Operation: "messages.media", ContentBase64: "JVBERi0="}
	if err := queueable(inline, true); err == nil || !strings.Contains(err.Error(), "media_id or media_url") {
		t.Fatal("bulk accepted inline bytes", err)
	}
	if err := queueable(inline, false); err != nil {
		t.Fatal("conversational inline media refused", err)
	}
	for _, operation := range []string{"messages.react", "messages.location", "messages.media_upload"} {
		if err := queueable(Input{Operation: operation}, true); err == nil {
			t.Fatal("bulk accepted", operation)
		}
	}
}
