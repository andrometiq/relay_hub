package email

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
)

func TestRejectNULInPersistedEmailText(t *testing.T) {
	for _, header := range []string{"Subject", "From", "To", "Cc", "Message-ID", "In-Reply-To", "References"} {
		raw := "From: a@example.invalid\r\nTo: b@example.invalid\r\n" + header + ": =?utf-8?B?SGkA?=\r\n\r\nbody"
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("accepted NUL in %s", header)
		}
	}
	for _, kind := range []string{"text/plain", "text/html"} {
		raw := "From: a@example.invalid\r\nContent-Type: " + kind + "\r\nContent-Transfer-Encoding: base64\r\n\r\nSGkA"
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("accepted NUL in %s", kind)
		}
	}
}

func TestPlainSMTPEncodingAndParameterizedAttachment(t *testing.T) {
	c := &Client{cfg: Config{From: "a@example.invalid", AccountID: "local"}}
	body := strings.Repeat("Tamil தமிழ் ", 200)
	input := Message{To: []string{"b@example.invalid"}, Subject: "test", Body: body}
	raw, _, err := c.compose(input)
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := io.ReadAll(message.Body)
	for _, line := range strings.Split(string(encoded), "\r\n") {
		if len(line) > 76 {
			t.Fatal("overlong SMTP line", len(line))
		}
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(string(encoded))))
	if err != nil || string(decoded) != body {
		t.Fatal("body changed", err)
	}
	input.Attachments = []Attachment{{Filename: "report.csv", ContentType: "text/csv; charset=utf-8", ContentBase64: base64.StdEncoding.EncodeToString([]byte("a,b"))}}
	raw, _, err = c.compose(input)
	if err != nil {
		t.Fatal(err)
	}
	message, _ = mail.ReadMessage(strings.NewReader(string(raw)))
	_, params, _ := mime.ParseMediaType(message.Header.Get("Content-Type"))
	reader := multipart.NewReader(message.Body, params["boundary"])
	reader.NextPart()
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	kind, params, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || kind != "text/csv" || params["charset"] != "utf-8" || params["name"] != "report.csv" {
		t.Fatal("lost attachment type", kind, params, err)
	}
}
