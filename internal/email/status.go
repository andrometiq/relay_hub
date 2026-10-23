package email

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
)

type DeliveryStatus struct {
	MessageID string `json:"message_id"`
	Recipient string `json:"recipient"`
	State     string `json:"state"`
	Code      string `json:"code"`
}

// DeliveryRank favors positive delivery evidence over a contradictory failure.
// A delay can never reverse either terminal outcome.
func DeliveryRank(state string) int {
	switch state {
	case "delayed":
		return 1
	case "bounced":
		return 2
	case "delivered":
		return 3
	}
	return 0
}

// MaxRecipientBytes bounds the normalized mailbox stored in the evidence key.
const MaxRecipientBytes = 254

var enhancedStatusRE = regexp.MustCompile(`^[245]\.[0-9]{1,3}\.[0-9]{1,3}$`)

// ParseDeliveryStatuses accepts only structured DSNs, never human-readable bounce text.
// The caller must authenticate the report's source before using these claims.
func ParseDeliveryStatuses(raw []byte) ([]DeliveryStatus, error) {
	if len(raw) > MaxMessageBytes {
		return nil, errors.New("DSN exceeds size limit")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	kind, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/report" || !strings.EqualFold(params["report-type"], "delivery-status") {
		return nil, nil
	}
	if params["boundary"] == "" {
		return nil, errors.New("missing DSN boundary")
	}
	reader := multipart.NewReader(msg.Body, params["boundary"])
	var report []byte
	var original string
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if count >= 10 {
			return nil, errors.New("too many DSN parts")
		}
		kind, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(part, MaxMessageBytes+1))
		if err != nil || len(content) > MaxMessageBytes {
			return nil, errors.New("invalid DSN part")
		}
		switch kind {
		case "message/delivery-status":
			if report != nil {
				return nil, errors.New("duplicate delivery-status part")
			}
			report = content
		case "message/rfc822", "text/rfc822-headers":
			header, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(content, []byte("\r\n\r\n")...)))).ReadMIMEHeader()
			if err != nil || len(header.Values("Message-ID")) != 1 || original != "" {
				return nil, errors.New("invalid original DSN headers")
			}
			original = header.Get("Message-ID")
		}
	}
	if len(report) == 0 {
		return nil, errors.New("missing delivery-status part")
	}
	readerHeaders := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(report, []byte("\r\n\r\n")...))))
	header, err := readerHeaders.ReadMIMEHeader()
	if err != nil || len(header.Values("Reporting-MTA")) != 1 || header.Get("Reporting-MTA") == "" || len(header.Values("Original-Message-ID")) > 1 {
		return nil, errors.New("invalid DSN report")
	}
	if id := header.Get("Original-Message-ID"); id != "" {
		if original != "" && original != id {
			return nil, errors.New("conflicting DSN message IDs")
		}
		original = id
	}
	if !ValidMessageID(original) {
		return nil, errors.New("DSN needs original Message-ID")
	}
	statuses := []DeliveryStatus{}
	for count := 0; ; count++ {
		header, err = readerHeaders.ReadMIMEHeader()
		if err == io.EOF || (err == nil && len(header) == 0) {
			break
		}
		if err != nil {
			return nil, err
		}
		if count >= 50 {
			return nil, errors.New("too many DSN recipients")
		}
		for _, key := range []string{"Final-Recipient", "Action", "Status"} {
			if len(header.Values(key)) != 1 {
				return nil, errors.New("invalid DSN recipient fields")
			}
		}
		addressType, address, ok := strings.Cut(header.Get("Final-Recipient"), ";")
		address = strings.TrimSpace(address)
		if len(address) > MaxRecipientBytes {
			return nil, errors.New("DSN recipient exceeds size limit")
		}
		recipient, err := mail.ParseAddress(address)
		if !ok || !strings.EqualFold(strings.TrimSpace(addressType), "rfc822") || err != nil {
			return nil, errors.New("invalid DSN recipient")
		}
		normalized := strings.ToLower(recipient.Address)
		if len(normalized) > MaxRecipientBytes {
			return nil, errors.New("DSN recipient exceeds size limit")
		}
		state, class := "", byte(0)
		switch strings.ToLower(header.Get("Action")) {
		case "delayed":
			state, class = "delayed", '4'
		case "failed":
			state, class = "bounced", '5'
		case "delivered":
			state, class = "delivered", '2'
		case "relayed", "expanded":
			continue
		default:
			return nil, errors.New("unsupported DSN action")
		}
		code := header.Get("Status")
		if !enhancedStatusRE.MatchString(code) || code[0] != class {
			return nil, errors.New("invalid DSN status")
		}
		statuses = append(statuses, DeliveryStatus{MessageID: original, Recipient: normalized, State: state, Code: code})
	}
	return statuses, nil
}
