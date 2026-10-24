package email

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"time"
)

const MaxAttachmentBytes = 4 * 1024 * 1024

type Attachment struct {
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	ContentBase64 string `json:"content_base64"`
}
type Message struct {
	To            []string     `json:"to"`
	Cc            []string     `json:"cc,omitempty"`
	Bcc           []string     `json:"bcc,omitempty"`
	Subject       string       `json:"subject"`
	Body          string       `json:"body"`
	HTML          string       `json:"html,omitempty"`
	InReplyTo     string       `json:"in_reply_to,omitempty"`
	References    []string     `json:"references,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	RequestKey    string       `json:"request_key,omitempty"`
	AutoSubmitted bool         `json:"auto_submitted,omitempty"`
}

var messageIDRE = regexp.MustCompile(`^<[^<>\s@]{1,120}@[^<>\s@]{1,120}>$`)

func ValidMessageID(id string) bool { return messageIDRE.MatchString(id) }

func (in Message) Validate() error {
	if strings.ContainsAny(in.Subject, "\r\n") {
		return errors.New("invalid email header")
	}
	if len(in.To) == 0 || len(in.To)+len(in.Cc)+len(in.Bcc) > 50 || strings.TrimSpace(in.Subject) == "" || len(in.Subject) > 998 || strings.ContainsAny(in.Subject, "\r\n") || strings.TrimSpace(in.Body) == "" || len(in.Body) > 100000 || len(in.HTML) > 200000 {
		return errors.New("invalid email recipients, subject or body")
	}
	for _, list := range [][]string{in.To, in.Cc, in.Bcc} {
		for _, v := range list {
			if _, err := mail.ParseAddress(v); err != nil || strings.ContainsAny(v, "\r\n") {
				return errors.New("invalid recipient")
			}
		}
	}
	if in.InReplyTo != "" && !messageIDRE.MatchString(in.InReplyTo) {
		return errors.New("invalid reply message ID")
	}
	if len(in.References) > 20 {
		return errors.New("too many thread references")
	}
	for _, id := range in.References {
		if !messageIDRE.MatchString(id) {
			return errors.New("invalid thread reference")
		}
	}
	if len(in.Attachments) > 8 {
		return errors.New("at most eight attachments are allowed")
	}
	total := 0
	for _, a := range in.Attachments {
		if len(a.Filename) == 0 || len(a.Filename) > 120 || strings.ContainsAny(a.Filename, "/\\\r\n\x00\"") {
			return errors.New("invalid attachment filename")
		}
		if _, _, err := mime.ParseMediaType(a.ContentType); err != nil || strings.ContainsAny(a.ContentType, "\r\n") {
			return errors.New("invalid attachment content type")
		}
		if len(a.ContentBase64) > base64.StdEncoding.EncodedLen(MaxAttachmentBytes) {
			return errors.New("attachments exceed 4 MiB")
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(a.ContentBase64)
		total += len(raw)
		if err != nil || len(raw) == 0 || total > MaxAttachmentBytes {
			return errors.New("attachments exceed 4 MiB or contain invalid data")
		}
	}
	return nil
}
func (c *Client) compose(in Message) ([]byte, []string, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	if strings.ContainsAny(c.cfg.From, "\r\n") {
		return nil, nil, errors.New("invalid sender")
	}
	recipients := []string{}
	for _, list := range [][]string{in.To, in.Cc, in.Bcc} {
		for _, value := range list {
			address, _ := mail.ParseAddress(value)
			recipients = append(recipients, address.Address)
		}
	}
	headers := []string{"From: " + c.cfg.From, "To: " + strings.Join(in.To, ", "), "Subject: " + mime.QEncoding.Encode("utf-8", in.Subject), "MIME-Version: 1.0", "Date: " + time.Now().Format(time.RFC1123Z), "Message-ID: " + c.MessageID(in.RequestKey)}
	if len(in.Cc) > 0 {
		headers = append(headers, "Cc: "+strings.Join(in.Cc, ", "))
	}
	if in.InReplyTo != "" {
		headers = append(headers, "In-Reply-To: "+in.InReplyTo)
	}
	if len(in.References) > 0 {
		headers = append(headers, "References: "+strings.Join(in.References, " "))
	}
	if in.AutoSubmitted {
		headers = append(headers, "Auto-Submitted: auto-replied", "X-Auto-Response-Suppress: All")
	}
	var content bytes.Buffer
	if len(in.Attachments) == 0 && in.HTML == "" {
		headers = append(headers, "Content-Type: text/plain; charset=utf-8", "Content-Transfer-Encoding: quoted-printable")
		q := quotedprintable.NewWriter(&content)
		if _, err := q.Write([]byte(in.Body)); err != nil {
			return nil, nil, err
		}
		if err := q.Close(); err != nil {
			return nil, nil, err
		}
	} else {
		mixed := multipart.NewWriter(&content)
		headers = append(headers, "Content-Type: multipart/mixed; boundary="+mixed.Boundary())
		if in.HTML != "" {
			var alternative bytes.Buffer
			alt := multipart.NewWriter(&alternative)
			for _, part := range []struct{ kind, text string }{{"text/plain", in.Body}, {"text/html", in.HTML}} {
				writer, err := alt.CreatePart(textproto.MIMEHeader{"Content-Type": {part.kind + "; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
				if err != nil {
					return nil, nil, err
				}
				q := quotedprintable.NewWriter(writer)
				if _, err = q.Write([]byte(part.text)); err != nil {
					return nil, nil, err
				}
				if err = q.Close(); err != nil {
					return nil, nil, err
				}
			}
			if err := alt.Close(); err != nil {
				return nil, nil, err
			}
			writer, err := mixed.CreatePart(textproto.MIMEHeader{"Content-Type": {"multipart/alternative; boundary=" + alt.Boundary()}})
			if err != nil {
				return nil, nil, err
			}
			writer.Write(alternative.Bytes())
		} else {
			writer, err := mixed.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
			if err != nil {
				return nil, nil, err
			}
			q := quotedprintable.NewWriter(writer)
			q.Write([]byte(in.Body))
			q.Close()
		}
		for _, a := range in.Attachments {
			base, params, _ := mime.ParseMediaType(a.ContentType)
			params["name"] = a.Filename
			writer, err := mixed.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType(base, params)}, "Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename})}, "Content-Transfer-Encoding": {"base64"}})
			if err != nil {
				return nil, nil, err
			}
			for start := 0; start < len(a.ContentBase64); start += 76 {
				fmt.Fprint(writer, a.ContentBase64[start:min(start+76, len(a.ContentBase64))]+"\r\n")
			}
		}
		if err := mixed.Close(); err != nil {
			return nil, nil, err
		}
	}
	return append([]byte(strings.Join(headers, "\r\n")+"\r\n\r\n"), content.Bytes()...), recipients, nil
}

func (c *Client) MessageID(key string) string {
	if key == "" {
		key = rand.Text()
	}
	sum := sha256.Sum256([]byte(c.cfg.AccountID + ":" + key))
	from, _ := mail.ParseAddress(c.cfg.From)
	domain := strings.SplitN(from.Address, "@", 2)[1]
	return "<relay-" + hex.EncodeToString(sum[:]) + "@" + domain + ">"
}
