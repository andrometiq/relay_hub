package telegram

import (
	"encoding/json"
	"errors"
	"time"
)

type updateChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type updateUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
type updateMessage struct {
	MessageID  int64            `json:"message_id"`
	Date       int64            `json:"date"`
	Text       string           `json:"text"`
	Caption    string           `json:"caption"`
	Chat       updateChat       `json:"chat"`
	From       updateUser       `json:"from"`
	SenderChat updateChat       `json:"sender_chat"`
	ThreadID   int64            `json:"message_thread_id"`
	ReplyTo    *ReplyPreview    `json:"reply_to_message"`
	Sticker    *StickerContent  `json:"sticker"`
	Location   *LocationContent `json:"location"`
	Contact    *ContactContent  `json:"contact"`
}

type ReplyPreview struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text,omitempty"`
	Caption   string `json:"caption,omitempty"`
}
type StickerContent struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Type         string `json:"type"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	IsAnimated   bool   `json:"is_animated"`
	IsVideo      bool   `json:"is_video"`
	Emoji        string `json:"emoji,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}
type LocationContent struct {
	Latitude           float64 `json:"latitude"`
	Longitude          float64 `json:"longitude"`
	HorizontalAccuracy float64 `json:"horizontal_accuracy,omitempty"`
	LivePeriod         int64   `json:"live_period,omitempty"`
}
type ContactContent struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
	VCard       string `json:"vcard,omitempty"`
}
type ReactionCount struct {
	Type       Reaction `json:"type"`
	TotalCount int64    `json:"total_count"`
}

var supportedUpdates = []string{"message", "edited_message", "channel_post", "edited_channel_post", "message_reaction", "message_reaction_count", "my_chat_member", "chat_member", "chat_join_request"}

// ParseUpdate preserves group participants and topic/reply routing independently
// of the conversation. Non-message events are accepted and classified, allowing
// the journal to retain them without treating membership events as AI prompts.
func (c *Client) ParseUpdate(raw []byte) (Message, error) {
	if !c.cfg.Enabled {
		return Message{}, errors.New("Telegram account is disabled")
	}
	var update map[string]json.RawMessage
	if len(raw) > 1048576 || json.Unmarshal(raw, &update) != nil {
		return Message{}, errors.New("invalid Telegram update")
	}
	var id int64
	if json.Unmarshal(update["update_id"], &id) != nil || id < 1 {
		return Message{}, errors.New("invalid Telegram update")
	}
	var kind string
	var payload json.RawMessage
	for _, candidate := range supportedUpdates {
		if v, ok := update[candidate]; ok && string(v) != "null" {
			if kind != "" {
				return Message{}, errors.New("ambiguous Telegram update")
			}
			kind = candidate
			payload = v
		}
	}
	if kind == "" {
		return Message{}, errors.New("unsupported Telegram update")
	}
	m := Message{UpdateID: id, Kind: kind}
	switch kind {
	case "message", "edited_message", "channel_post", "edited_channel_post":
		var msg updateMessage
		if json.Unmarshal(payload, &msg) != nil || msg.MessageID <= 0 || msg.ThreadID < 0 {
			return Message{}, errors.New("invalid Telegram message update")
		}
		m.ChatID, m.ChatType, m.MessageID = msg.Chat.ID, msg.Chat.Type, msg.MessageID
		m.SenderID, m.SenderChatID, m.Username = msg.From.ID, msg.SenderChat.ID, msg.From.Username
		m.Text, m.ThreadID, m.Date = msg.Text, msg.ThreadID, time.Unix(msg.Date, 0).UTC()
		m.ContentType = "text"
		typed := 0
		if msg.Sticker != nil {
			if !fileIDRE.MatchString(msg.Sticker.FileID) {
				return Message{}, errors.New("invalid Telegram sticker update")
			}
			m.ContentType, m.Sticker = "sticker", msg.Sticker
			typed++
		}
		if msg.Location != nil {
			if !validCoordinates(msg.Location.Latitude, msg.Location.Longitude) {
				return Message{}, errors.New("invalid Telegram location update")
			}
			m.ContentType, m.Location = "location", msg.Location
			typed++
		}
		if msg.Contact != nil {
			m.ContentType, m.Contact = "contact", msg.Contact
			typed++
		}
		if typed > 1 || (typed > 0 && msg.Text != "") {
			return Message{}, errors.New("ambiguous Telegram message content")
		}
		if m.Text == "" {
			m.Text = msg.Caption
		}
		if msg.ReplyTo != nil {
			m.ReplyTo = msg.ReplyTo.MessageID
			m.Quote = msg.ReplyTo
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(payload, &fields)
		// Retain raw unsupported media without falsely labeling its caption as a
		// fully supported text/media message. These types need their own adapters.
		for _, field := range []string{"photo", "audio", "voice", "video", "video_note", "document", "animation", "poll", "venue"} {
			if v, ok := fields[field]; ok && string(v) != "null" {
				m.ContentType = "unsupported"
				break
			}
		}
		for _, field := range []string{"new_chat_members", "left_chat_member", "new_chat_title", "new_chat_photo", "delete_chat_photo", "group_chat_created", "supergroup_chat_created", "channel_chat_created", "message_auto_delete_timer_changed", "migrate_to_chat_id", "migrate_from_chat_id", "pinned_message", "forum_topic_created", "forum_topic_edited", "forum_topic_closed", "forum_topic_reopened", "general_forum_topic_hidden", "general_forum_topic_unhidden", "video_chat_scheduled", "video_chat_started", "video_chat_ended", "video_chat_participants_invited"} {
			if v, ok := fields[field]; ok && string(v) != "null" {
				m.Kind = "service"
				m.ContentType = "service"
				m.Text = ""
				break
			}
		}
	default:
		var event struct {
			Chat           updateChat      `json:"chat"`
			From           updateUser      `json:"from"`
			User           updateUser      `json:"user"`
			ActorChat      updateChat      `json:"actor_chat"`
			Date           int64           `json:"date"`
			MessageID      int64           `json:"message_id"`
			OldReaction    []Reaction      `json:"old_reaction"`
			NewReaction    []Reaction      `json:"new_reaction"`
			ReactionCounts []ReactionCount `json:"reactions"`
		}
		if json.Unmarshal(payload, &event) != nil {
			return Message{}, errors.New("invalid Telegram group event")
		}
		m.ChatID, m.ChatType, m.SenderID, m.SenderChatID = event.Chat.ID, event.Chat.Type, event.From.ID, event.ActorChat.ID
		m.Username, m.MessageID, m.Date = event.From.Username, event.MessageID, time.Unix(event.Date, 0).UTC()
		if kind == "message_reaction" || kind == "message_reaction_count" {
			m.ContentType = "reaction"
			m.Reactions, m.OldReactions, m.ReactionCounts = event.NewReaction, event.OldReaction, event.ReactionCounts
		}
		if event.User.ID != 0 {
			m.SenderID, m.Username = event.User.ID, event.User.Username
		}
	}
	if !validChatID(m.ChatID) {
		return Message{}, errors.New("invalid Telegram chat")
	}
	switch m.ChatType {
	case "", "private", "group", "supergroup", "channel":
	default:
		return Message{}, errors.New("invalid Telegram chat type")
	}
	if (m.ChatType == "group" || m.ChatType == "supergroup" || m.ChatType == "channel") && m.ChatID > 0 {
		return Message{}, errors.New("invalid Telegram group identifier")
	}
	return m, nil
}
