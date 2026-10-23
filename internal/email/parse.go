package email

import (
	"bytes"
	"encoding/base64"
	"errors"
	"golang.org/x/text/encoding/charmap"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"unicode/utf8"
)

const MaxMessageBytes = 6 * 1024 * 1024

type Received struct {
	MessageID     string       `json:"message_id"`
	InReplyTo     string       `json:"in_reply_to,omitempty"`
	References    []string     `json:"references,omitempty"`
	From          string       `json:"from"`
	To            []string     `json:"to"`
	Cc            []string     `json:"cc,omitempty"`
	Subject       string       `json:"subject"`
	Body          string       `json:"body"`
	HTML          string       `json:"html,omitempty"`
	AutoSubmitted bool         `json:"auto_submitted"`
	Attachments   []Attachment `json:"attachments,omitempty"`
}

func decodeCharset(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "", "utf-8", "us-ascii":
		return input, nil
	case "iso-8859-1":
		return charmap.ISO8859_1.NewDecoder().Reader(input), nil
	case "windows-1252":
		return charmap.Windows1252.NewDecoder().Reader(input), nil
	default:
		return nil, errors.New("unsupported email charset")
	}
}
func Parse(raw []byte) (Received, error) {
	result := Received{}
	if len(raw) == 0 || len(raw) > MaxMessageBytes {
		return result, errors.New("email exceeds size limit")
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return result, errors.New("invalid email")
	}
	decoder := mime.WordDecoder{CharsetReader: decodeCharset}
	result.Subject, err = decoder.DecodeHeader(message.Header.Get("Subject"))
	if err != nil || len(result.Subject) > 998 {
		return result, errors.New("invalid email subject")
	}
	from, err := message.Header.AddressList("From")
	if err != nil || len(from) != 1 {
		return result, errors.New("email needs one sender")
	}
	result.From = strings.ToLower(from[0].Address)
	for _, header := range []string{"To", "Cc"} {
		values, err := message.Header.AddressList(header)
		if err != nil && message.Header.Get(header) != "" {
			return result, errors.New("invalid recipient header")
		}
		if len(values) > 50 {
			return result, errors.New("too many recipients")
		}
		for _, address := range values {
			if header == "To" {
				result.To = append(result.To, address.Address)
			} else {
				result.Cc = append(result.Cc, address.Address)
			}
		}
	}
	result.MessageID = message.Header.Get("Message-ID")
	if result.MessageID != "" && !messageIDRE.MatchString(result.MessageID) {
		return result, errors.New("invalid message ID")
	}
	result.InReplyTo = message.Header.Get("In-Reply-To")
	if !messageIDRE.MatchString(result.InReplyTo) {
		result.InReplyTo = ""
	}
	for _, id := range strings.Fields(message.Header.Get("References")) {
		if messageIDRE.MatchString(id) {
			result.References = append(result.References, id)
		}
	}
	if len(result.References) > 20 {
		result.References = result.References[len(result.References)-20:]
	}
	auto := strings.ToLower(message.Header.Get("Auto-Submitted"))
	precedence := strings.ToLower(message.Header.Get("Precedence"))
	result.AutoSubmitted = (auto != "" && auto != "no") || message.Header.Get("List-ID") != "" || precedence == "bulk" || precedence == "list" || precedence == "junk"
	parts, total := 0, 0
	var parsePart func(textproto.MIMEHeader, io.Reader, int) error
	parsePart = func(header textproto.MIMEHeader, reader io.Reader, depth int) error {
		parts++
		if depth > 10 || parts > 50 {
			return errors.New("email MIME nesting exceeds limit")
		}
		kind, params, err := mime.ParseMediaType(header.Get("Content-Type"))
		if header.Get("Content-Type") == "" {
			kind = "text/plain"
			params = map[string]string{}
			err = nil
		}
		if err != nil {
			return errors.New("invalid MIME content type")
		}
		if strings.HasPrefix(kind, "multipart/") {
			boundary := params["boundary"]
			if boundary == "" {
				return errors.New("missing MIME boundary")
			}
			multi := multipart.NewReader(reader, boundary)
			for {
				part, err := multi.NextRawPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				if err = parsePart(part.Header, part, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
		case "base64":
			reader = base64.NewDecoder(base64.StdEncoding, reader)
		case "quoted-printable":
			reader = quotedprintable.NewReader(reader)
		case "", "7bit", "8bit", "binary":
		default:
			return errors.New("unsupported transfer encoding")
		}
		disposition, dp, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
		filename := dp["filename"]
		if filename == "" {
			filename = params["name"]
		}
		attachment := filename != "" || disposition == "attachment" || (kind != "text/plain" && kind != "text/html")
		if attachment {
			raw, err := io.ReadAll(io.LimitReader(reader, MaxAttachmentBytes+1))
			total += len(raw)
			if err != nil || total > MaxAttachmentBytes || len(result.Attachments) >= 8 {
				return errors.New("email attachments exceed limit")
			}
			if filename == "" {
				filename = "attachment.bin"
			}
			if len(filename) > 120 || strings.ContainsAny(filename, "/\\\r\n\x00\"") {
				filename = "attachment.bin"
			}
			result.Attachments = append(result.Attachments, Attachment{Filename: filename, ContentType: kind, ContentBase64: base64.StdEncoding.EncodeToString(raw)})
			return nil
		}
		reader, err = decodeCharset(params["charset"], reader)
		if err != nil {
			return err
		}
		limit := 100000
		if kind == "text/html" {
			limit = 200000
		}
		raw, err := io.ReadAll(io.LimitReader(reader, int64(limit+1)))
		if err != nil || len(raw) > limit || !utf8.Valid(raw) {
			return errors.New("email text exceeds limit or has invalid encoding")
		}
		if kind == "text/plain" {
			result.Body += string(raw)
			if len(result.Body) > 100000 {
				return errors.New("email text exceeds limit")
			}
		} else {
			result.HTML += string(raw)
			if len(result.HTML) > 200000 {
				return errors.New("email HTML exceeds limit")
			}
		}
		return nil
	}
	if err = parsePart(textproto.MIMEHeader(message.Header), message.Body, 0); err != nil {
		return Received{}, err
	}
	if strings.TrimSpace(result.Body) == "" {
		if result.HTML != "" {
			result.Body = "[HTML email]"
		} else {
			result.Body = "[Email attachment]"
		}
	}
	return result, nil
}
