package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MessageInput covers the supported message operations without accepting an
// arbitrary Bot API method or additional account/provider credentials.
type MessageInput struct {
	FileID        string     `json:"file_id,omitempty"`
	Filename      string     `json:"filename,omitempty"`
	ContentBase64 string     `json:"content_base64,omitempty"`
	Caption       string     `json:"caption,omitempty"`
	RequestKey    string     `json:"request_key,omitempty"`
	Operation     string     `json:"operation"`
	ChatID        int64      `json:"chat_id"`
	ThreadID      int64      `json:"message_thread_id,omitempty"`
	ReplyTo       int64      `json:"reply_to,omitempty"`
	Text          string     `json:"text,omitempty"`
	Sticker       string     `json:"sticker,omitempty"`
	MessageID     int64      `json:"message_id,omitempty"`
	Reaction      []Reaction `json:"reaction,omitempty"`
	IsBig         bool       `json:"is_big,omitempty"`
	Latitude      *float64   `json:"latitude,omitempty"`
	Longitude     *float64   `json:"longitude,omitempty"`
	PhoneNumber   string     `json:"phone_number,omitempty"`
	FirstName     string     `json:"first_name,omitempty"`
	LastName      string     `json:"last_name,omitempty"`
	VCard         string     `json:"vcard,omitempty"`
}
type Reaction struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

var fileIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
var emojiIDRE = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
var phoneRE = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{2,30}$`)

func cleanSingleLine(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validCoordinates(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsInf(lat, 0) && !math.IsNaN(lon) && !math.IsInf(lon, 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}
func validStickerReference(ref string) bool {
	if fileIDRE.MatchString(ref) {
		return true
	}
	if len(ref) > 2048 {
		return false
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Host == "" || (u.Port() != "" && u.Port() != "443") || strings.ToLower(path.Ext(u.Path)) != ".webp" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || host == "localhost" {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	}
	return strings.Contains(host, ".") && !strings.ContainsAny(host, " \\\t\r\n")
}
func validReactionEmoji(value string) bool {
	if value == "" || !cleanSingleLine(value, 16) {
		return false
	}
	symbolic := false
	for _, r := range value {
		switch {
		case unicode.Is(unicode.So, r), unicode.Is(unicode.Sk, r):
			symbolic = true
		case r == 0x20e3:
			symbolic = true
		case r == 0xfe0f, r == 0x200d, r >= '0' && r <= '9', r == '#', r == '*':
		default:
			return false
		}
	}
	return symbolic
}
func validateVCard(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || !utf8.ValidString(value) {
		return errors.New("Telegram contact vcard exceeds 2048 bytes or is invalid UTF-8")
	}
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	if strings.Contains(normalized, "\r") {
		return errors.New("invalid Telegram contact vcard line endings")
	}
	for _, r := range normalized {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return errors.New("invalid Telegram contact vcard")
		}
	}
	lines := strings.Split(strings.TrimSuffix(normalized, "\n"), "\n")
	if len(lines) < 4 || lines[0] != "BEGIN:VCARD" || lines[len(lines)-1] != "END:VCARD" {
		return errors.New("Telegram contact vcard must contain one complete card")
	}
	version := false
	for i, line := range lines {
		if i != 0 && line == "BEGIN:VCARD" || i != len(lines)-1 && line == "END:VCARD" {
			return errors.New("multiple Telegram contact vcards are not supported")
		}
		if strings.HasPrefix(line, "VERSION:") {
			if version || (line != "VERSION:3.0" && line != "VERSION:4.0") {
				return errors.New("unsupported Telegram vcard version")
			}
			version = true
		}
	}
	if !version {
		return errors.New("Telegram contact vcard requires VERSION:3.0 or VERSION:4.0")
	}
	return nil
}
func (in MessageInput) request() (string, map[string]any, error) {
	if !validChatID(in.ChatID) || in.ReplyTo < 0 || in.ReplyTo >= 1<<52 || in.ThreadID < 0 || in.ThreadID >= 1<<52 || in.MessageID < 0 || in.MessageID >= 1<<52 {
		return "", nil, errors.New("invalid Telegram message routing")
	}
	if (in.Operation != "text" && in.Text != "") || (in.Operation != "sticker" && in.Sticker != "") || (in.Operation != "location" && (in.Latitude != nil || in.Longitude != nil)) || (in.Operation != "contact" && (in.PhoneNumber != "" || in.FirstName != "" || in.LastName != "" || in.VCard != "")) {
		return "", nil, errors.New("Telegram message parameters do not match the selected operation")
	}
	media := mediaOperation(in.Operation)
	if !media && (in.FileID != "" || in.Filename != "" || in.ContentBase64 != "" || in.Caption != "") {
		return "", nil, errors.New("media parameters require a media operation")
	}
	body := map[string]any{"chat_id": in.ChatID}
	var method string
	switch in.Operation {
	case "photo", "video", "audio", "voice", "document":
		if _, err := in.uploadBytes(); err != nil {
			return "", nil, err
		}
		if !utf8.ValidString(in.Caption) || utf8.RuneCountInString(in.Caption) > 1024 {
			return "", nil, errors.New("caption exceeds 1024 characters")
		}
		method = map[string]string{"photo": "sendPhoto", "video": "sendVideo", "audio": "sendAudio", "voice": "sendVoice", "document": "sendDocument"}[in.Operation]
		if in.FileID != "" {
			body[in.Operation] = in.FileID
		}
		if in.Caption != "" {
			body["caption"] = in.Caption
		}
	case "text":
		if strings.TrimSpace(in.Text) == "" || !utf8.ValidString(in.Text) || utf8.RuneCountInString(in.Text) > 4096 {
			return "", nil, errors.New("Telegram text must contain 1-4096 characters")
		}
		method = "sendMessage"
		body["text"] = in.Text
	case "sticker":
		if !validStickerReference(in.Sticker) {
			return "", nil, errors.New("Telegram sticker requires a file_id or public HTTPS WEBP URL; animated/video stickers require file_id")
		}
		method = "sendSticker"
		body["sticker"] = in.Sticker
	case "reaction":
		if in.MessageID < 1 || in.Reaction == nil || len(in.Reaction) > 1 || in.ReplyTo != 0 || in.ThreadID != 0 {
			return "", nil, errors.New("Telegram reaction requires a target message_id and an explicit zero-or-one reaction list; replies/topics are not reaction parameters")
		}
		for _, r := range in.Reaction {
			switch r.Type {
			case "emoji":
				if !validReactionEmoji(r.Emoji) || r.CustomEmojiID != "" {
					return "", nil, errors.New("invalid Telegram reaction emoji")
				}
			case "custom_emoji":
				if !emojiIDRE.MatchString(r.CustomEmojiID) || r.Emoji != "" {
					return "", nil, errors.New("invalid Telegram custom reaction")
				}
			default:
				return "", nil, errors.New("unsupported Telegram reaction type; bots cannot send paid reactions")
			}
		}
		method = "setMessageReaction"
		body["message_id"] = in.MessageID
		body["reaction"] = in.Reaction
		if in.IsBig {
			body["is_big"] = true
		}
	case "location":
		if in.Latitude == nil || in.Longitude == nil || !validCoordinates(*in.Latitude, *in.Longitude) {
			return "", nil, errors.New("Telegram location requires latitude -90..90 and longitude -180..180")
		}
		method = "sendLocation"
		body["latitude"], body["longitude"] = *in.Latitude, *in.Longitude
	case "contact":
		if !phoneRE.MatchString(in.PhoneNumber) || strings.TrimSpace(in.FirstName) == "" || !cleanSingleLine(in.FirstName, 64) || !cleanSingleLine(in.LastName, 64) {
			return "", nil, errors.New("invalid Telegram contact phone or name")
		}
		if err := validateVCard(in.VCard); err != nil {
			return "", nil, err
		}
		method = "sendContact"
		body["phone_number"], body["first_name"] = in.PhoneNumber, in.FirstName
		if in.LastName != "" {
			body["last_name"] = in.LastName
		}
		if in.VCard != "" {
			body["vcard"] = in.VCard
		}
	default:
		return "", nil, errors.New("unsupported Telegram message operation")
	}
	if in.Operation != "reaction" {
		if in.MessageID != 0 || in.Reaction != nil || in.IsBig {
			return "", nil, errors.New("reaction-only parameters on Telegram send operation")
		}
		if in.ReplyTo > 0 {
			body["reply_parameters"] = map[string]any{"message_id": in.ReplyTo}
		}
		if in.ThreadID > 0 {
			body["message_thread_id"] = in.ThreadID
		}
	}
	return method, body, nil
}

// ExecuteMessage sends exactly once and returns only a confirmed receipt.
func (c *Client) ExecuteMessage(ctx context.Context, in MessageInput) (map[string]any, error) {
	if !c.cfg.Enabled {
		return nil, errors.New("Telegram account is disabled")
	}
	method, body, err := in.request()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if in.ContentBase64 != "" {
		raw, err = c.upload(ctx, method, body, in)
	} else {
		raw, err = c.call(ctx, method, body)
	}
	if err != nil {
		return nil, err
	}
	messageID := in.MessageID
	if in.Operation == "reaction" {
		var ok bool
		if json.Unmarshal(raw, &ok) != nil || !ok {
			return nil, errors.New("Telegram did not confirm reaction acceptance")
		}
	} else {
		var receipt struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		}
		if json.Unmarshal(raw, &receipt) != nil || receipt.MessageID < 1 || receipt.MessageID >= 1<<52 || (receipt.Chat.ID != 0 && receipt.Chat.ID != in.ChatID) {
			return nil, errors.New("Telegram did not confirm message acceptance")
		}
		messageID = receipt.MessageID
	}
	var result any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&result) != nil {
		return nil, errors.New("invalid Telegram message response")
	}
	return map[string]any{"operation": in.Operation, "chat_id": in.ChatID, "message_id": messageID, "result": result}, nil
}

// Validate allows durable admission to reject malformed requests before journaling.
func (in MessageInput) Validate() error { _, _, err := in.request(); return err }
