package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"

	"github.com/andrometiq/relay_hub/internal/attachment"
)

func TestMIMEAttachmentsThreadAndHiddenRecipients(t *testing.T) {
	c, _ := New(Config{AccountID: "fixture", Host: "smtp.example.invalid", Port: 587, From: "sender@example.invalid", Enabled: true})
	input := Message{To: []string{"to@example.invalid"}, Cc: []string{"cc@example.invalid"}, Bcc: []string{"private@example.invalid"}, Subject: "தமிழ் subject", Body: "Plain text", HTML: "<strong>HTML text</strong>", InReplyTo: "<original@example.invalid>", References: []string{"<root@example.invalid>"}, Attachments: []Attachment{{Filename: "evidence.txt", ContentType: "text/plain", ContentBase64: base64.StdEncoding.EncodeToString([]byte("private attachment bytes"))}}, AutoSubmitted: true}
	raw, recipients, err := c.compose(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 3 || strings.Contains(string(raw), "private@example.invalid") {
		t.Fatal("BCC leaked or omitted from envelope")
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || subject != input.Subject {
		t.Fatal("unicode subject changed", err)
	}
	if message.Header.Get("In-Reply-To") != input.InReplyTo || message.Header.Get("References") != input.References[0] || message.Header.Get("Auto-Submitted") != "auto-replied" {
		t.Fatal("thread/loop headers missing")
	}
	_, params, _ := mime.ParseMediaType(message.Header.Get("Content-Type"))
	parts := multipart.NewReader(message.Body, params["boundary"])
	alternative, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ = mime.ParseMediaType(alternative.Header.Get("Content-Type"))
	alts := multipart.NewReader(alternative, params["boundary"])
	for _, expected := range []string{input.Body, input.HTML} {
		part, err := alts.NextRawPart()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil || string(body) != expected {
			t.Fatal("alternative content changed", err)
		}
	}
	file, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, file))
	if err != nil || string(body) != "private attachment bytes" {
		t.Fatal("attachment changed", err)
	}
	for _, id := range []string{"bad", "<a@b>\r\nBcc: attack@example.invalid"} {
		input.InReplyTo = id
		if input.Validate() == nil {
			t.Fatal("header injection accepted")
		}
	}
}

func TestMIMERoundTripAndLoopFlags(t *testing.T) {
	c, _ := New(Config{AccountID: "fixture", Host: "smtp.example.invalid", Port: 587, From: "sender@example.invalid", Enabled: true})
	input := Message{To: []string{"to@example.invalid"}, Subject: "தமிழ்", Body: "Plain message", HTML: "<p>HTML</p>", AutoSubmitted: true, Attachments: []Attachment{{Filename: "a.txt", ContentType: "text/plain", ContentBase64: base64.StdEncoding.EncodeToString([]byte("bytes"))}}}
	raw, _, err := c.compose(input)
	if err != nil {
		t.Fatal(err)
	}
	received, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if received.Body != input.Body || received.HTML != input.HTML || received.Subject != input.Subject || !received.AutoSubmitted || len(received.Attachments) != 1 || received.Attachments[0].ContentBase64 != input.Attachments[0].ContentBase64 {
		t.Fatal("MIME round trip changed content")
	}
	if _, err = Parse(make([]byte, MaxMessageBytes+1)); err == nil {
		t.Fatal("oversized mail accepted")
	}
}

func TestLargestAttachmentsFitOneMessageAndParseBack(t *testing.T) {
	c, _ := New(Config{AccountID: "fixture", Host: "smtp.example.invalid", Port: 587, From: "sender@example.invalid", Enabled: true})
	message := func(sizes ...int) Message {
		in := Message{To: []string{"to@example.invalid"}, Subject: "Large", Body: "Plain message"}
		for i, size := range sizes {
			in.Attachments = append(in.Attachments, Attachment{Filename: fmt.Sprintf("part%d.bin", i), ContentType: "application/octet-stream", ContentBase64: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), size))})
		}
		return in
	}
	half := attachment.MaxBytes / 2
	if err := message(half, attachment.MaxBytes-half+1).Validate(); err == nil {
		t.Fatal("attachments above the combined limit accepted")
	}
	in := message(half, attachment.MaxBytes-half)
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _, err := c.compose(in)
	if err != nil || len(raw) > MaxMessageBytes {
		t.Fatal("largest message does not fit", len(raw), err)
	}
	received, err := Parse(raw)
	if err != nil || len(received.Attachments) != 2 {
		t.Fatal("largest message did not parse back", err)
	}
}
