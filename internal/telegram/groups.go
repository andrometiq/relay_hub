package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// GroupInput exposes only documented Bot API operations, never an arbitrary
// method or provider URL. Zero values omit optional numeric settings.
type GroupInput struct {
	Operation           string          `json:"operation"`
	ChatID              int64           `json:"chat_id"`
	UserID              int64           `json:"user_id,omitempty"`
	ThreadID            int64           `json:"message_thread_id,omitempty"`
	MessageID           int64           `json:"message_id,omitempty"`
	Title               string          `json:"title,omitempty"`
	Description         string          `json:"description,omitempty"`
	Name                string          `json:"name,omitempty"`
	InviteLink          string          `json:"invite_link,omitempty"`
	ExpireDate          int64           `json:"expire_date,omitempty"`
	UntilDate           int64           `json:"until_date,omitempty"`
	MemberLimit         int             `json:"member_limit,omitempty"`
	CreatesJoinRequest  *bool           `json:"creates_join_request,omitempty"`
	OnlyIfBanned        *bool           `json:"only_if_banned,omitempty"`
	RevokeMessages      *bool           `json:"revoke_messages,omitempty"`
	DisableNotification *bool           `json:"disable_notification,omitempty"`
	Permissions         map[string]bool `json:"permissions,omitempty"`
	AdministratorRights map[string]bool `json:"administrator_rights,omitempty"`
	SenderChatID        int64           `json:"sender_chat_id,omitempty"`
	CustomTitle         string          `json:"custom_title,omitempty"`
	PhotoBase64         string          `json:"photo_base64,omitempty"`
}

func GroupReadOperation(op string) bool {
	switch op {
	case "info", "member", "count", "administrators":
		return true
	}
	return false
}

var permissionNames = map[string]bool{
	"can_send_messages": true, "can_send_audios": true, "can_send_documents": true, "can_send_photos": true, "can_send_videos": true, "can_send_video_notes": true, "can_send_voice_notes": true, "can_send_polls": true, "can_send_other_messages": true, "can_add_web_page_previews": true, "can_react_to_messages": true, "can_edit_tag": true, "can_change_info": true, "can_invite_users": true, "can_pin_messages": true, "can_manage_topics": true,
}
var administratorNames = map[string]bool{
	"is_anonymous": true, "can_manage_chat": true, "can_delete_messages": true, "can_manage_video_chats": true, "can_restrict_members": true, "can_promote_members": true, "can_change_info": true, "can_invite_users": true, "can_post_stories": true, "can_edit_stories": true, "can_delete_stories": true, "can_post_messages": true, "can_edit_messages": true, "can_pin_messages": true, "can_manage_topics": true, "can_manage_direct_messages": true, "can_manage_tags": true, "can_send_welcome_messages": true,
}

func checkedPermissions(values map[string]bool, allowed map[string]bool) error {
	if len(values) == 0 || len(values) > len(allowed) {
		return errors.New("Telegram permissions must explicitly specify supported rights")
	}
	for name := range values {
		if !allowed[name] {
			return errors.New("unsupported Telegram permission")
		}
	}
	return nil
}
func validInvite(link string) bool {
	u, e := url.Parse(link)
	return e == nil && u.Scheme == "https" && (u.Host == "t.me" || u.Host == "telegram.me") && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (strings.HasPrefix(u.Path, "/+") || strings.HasPrefix(u.Path, "/joinchat/")) && len(link) < 512
}
func (in GroupInput) request() (string, map[string]any, error) {
	if in.Operation == "create" || in.Operation == "delete" {
		return "", nil, errors.New("Telegram bots cannot create or delete groups; create the group in Telegram and add the bot, or use leave")
	}
	if !validChatID(in.ChatID) || in.ChatID >= 0 {
		return "", nil, errors.New("Telegram group chat_id must be a negative group or supergroup identifier")
	}
	if in.UserID < 0 || in.UserID >= (1<<52) || in.ThreadID < 0 || in.ThreadID >= 1<<52 || in.MessageID < 0 || in.MessageID >= 1<<52 || in.ExpireDate < 0 || in.UntilDate < 0 {
		return "", nil, errors.New("invalid Telegram group parameter")
	}
	body := map[string]any{"chat_id": in.ChatID}
	var method string
	switch in.Operation {
	case "info":
		method = "getChat"
	case "member":
		method = "getChatMember"
	case "count":
		method = "getChatMemberCount"
	case "administrators":
		method = "getChatAdministrators"
	case "title":
		if strings.TrimSpace(in.Title) == "" || utf8.RuneCountInString(in.Title) > 128 {
			return "", nil, errors.New("Telegram group title must contain 1-128 characters")
		}
		method = "setChatTitle"
		body["title"] = in.Title
	case "description":
		if utf8.RuneCountInString(in.Description) > 255 {
			return "", nil, errors.New("Telegram group description exceeds 255 characters")
		}
		method = "setChatDescription"
		body["description"] = in.Description
	case "photo.set":
		method = "setChatPhoto"
	case "photo.delete":
		method = "deleteChatPhoto"
	case "permissions", "members.restrict":
		if err := checkedPermissions(in.Permissions, permissionNames); err != nil {
			return "", nil, err
		}
		method = "setChatPermissions"
		if in.Operation == "members.restrict" {
			method = "restrictChatMember"
			body["until_date"] = in.UntilDate
		}
		body["permissions"] = in.Permissions
		body["use_independent_chat_permissions"] = true
	case "invite.create", "invite.edit", "invite.revoke":
		if utf8.RuneCountInString(in.Name) > 32 || in.MemberLimit < 0 || in.MemberLimit > 99999 {
			return "", nil, errors.New("invalid Telegram invite settings")
		}
		if in.CreatesJoinRequest != nil && *in.CreatesJoinRequest && in.MemberLimit != 0 {
			return "", nil, errors.New("Telegram join-request invitations cannot have a member limit")
		}
		method = "createChatInviteLink"
		if in.Operation != "invite.create" {
			if !validInvite(in.InviteLink) {
				return "", nil, errors.New("invalid Telegram invite link")
			}
			body["invite_link"] = in.InviteLink
			if in.Operation == "invite.edit" {
				method = "editChatInviteLink"
			} else {
				method = "revokeChatInviteLink"
			}
		}
		if in.Operation != "invite.revoke" {
			body["name"] = in.Name
			if in.ExpireDate != 0 {
				body["expire_date"] = in.ExpireDate
			}
			if in.MemberLimit != 0 {
				body["member_limit"] = in.MemberLimit
			}
			if in.CreatesJoinRequest != nil {
				body["creates_join_request"] = *in.CreatesJoinRequest
			}
		}
	case "requests.approve":
		method = "approveChatJoinRequest"
	case "requests.reject":
		method = "declineChatJoinRequest"
	case "members.ban":
		method = "banChatMember"
		body["until_date"] = in.UntilDate
		if in.RevokeMessages != nil {
			body["revoke_messages"] = *in.RevokeMessages
		}
	case "members.unban":
		method = "unbanChatMember"
		body["only_if_banned"] = true
		if in.OnlyIfBanned != nil {
			body["only_if_banned"] = *in.OnlyIfBanned
		}
	case "members.promote":
		if err := checkedPermissions(in.AdministratorRights, administratorNames); err != nil {
			return "", nil, err
		}
		method = "promoteChatMember"
		for k, v := range in.AdministratorRights {
			body[k] = v
		}
	case "members.ban_sender", "members.unban_sender":
		if !validChatID(in.SenderChatID) || in.SenderChatID >= 0 {
			return "", nil, errors.New("Telegram sender_chat_id must be a negative chat identifier")
		}
		method = "banChatSenderChat"
		if in.Operation == "members.unban_sender" {
			method = "unbanChatSenderChat"
		}
		body["sender_chat_id"] = in.SenderChatID
	case "members.title":
		if utf8.RuneCountInString(in.CustomTitle) > 16 || !utf8.ValidString(in.CustomTitle) {
			return "", nil, errors.New("Telegram administrator title exceeds 16 characters")
		}
		for _, r := range in.CustomTitle {
			if !(unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsPunct(r) || r == ' ' || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)) || r == 0xfe0f {
				return "", nil, errors.New("Telegram administrator title cannot contain emoji or control characters")
			}
		}
		method = "setChatAdministratorCustomTitle"
		body["custom_title"] = in.CustomTitle
	case "leave":
		method = "leaveChat"
	case "topics.create", "topics.edit":
		if strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 128 {
			return "", nil, errors.New("Telegram topic name must contain 1-128 characters")
		}
		method = "createForumTopic"
		if in.Operation == "topics.edit" {
			method = "editForumTopic"
		}
		body["name"] = in.Name
	case "topics.close":
		method = "closeForumTopic"
	case "topics.reopen":
		method = "reopenForumTopic"
	case "topics.delete":
		method = "deleteForumTopic"
	case "topics.unpin_all":
		method = "unpinAllForumTopicMessages"
	case "topics.general.edit":
		if strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 128 {
			return "", nil, errors.New("Telegram topic name must contain 1-128 characters")
		}
		method = "editGeneralForumTopic"
		body["name"] = in.Name
	case "topics.general.close":
		method = "closeGeneralForumTopic"
	case "topics.general.reopen":
		method = "reopenGeneralForumTopic"
	case "topics.general.hide":
		method = "hideGeneralForumTopic"
	case "topics.general.unhide":
		method = "unhideGeneralForumTopic"
	case "topics.general.unpin_all":
		method = "unpinAllGeneralForumTopicMessages"
	case "pins.pin":
		if in.MessageID <= 0 {
			return "", nil, errors.New("Telegram pin requires a message_id")
		}
		method = "pinChatMessage"
		body["message_id"] = in.MessageID
		if in.DisableNotification != nil {
			body["disable_notification"] = *in.DisableNotification
		}
	case "pins.unpin":
		method = "unpinChatMessage"
		if in.MessageID > 0 {
			body["message_id"] = in.MessageID
		}
	case "pins.unpin_all":
		method = "unpinAllChatMessages"
	default:
		return "", nil, errors.New("unsupported Telegram group operation")
	}
	if in.Operation == "member" || strings.HasPrefix(in.Operation, "requests.") || (strings.HasPrefix(in.Operation, "members.") && in.Operation != "members.ban_sender" && in.Operation != "members.unban_sender") {
		if in.UserID <= 0 {
			return "", nil, errors.New("Telegram operation requires a positive user_id")
		}
		body["user_id"] = in.UserID
	}
	if strings.HasPrefix(in.Operation, "topics.") && in.Operation != "topics.create" && !strings.HasPrefix(in.Operation, "topics.general.") {
		if in.ThreadID <= 0 {
			return "", nil, errors.New("Telegram topic operation requires a message_thread_id")
		}
		body["message_thread_id"] = in.ThreadID
	}
	return method, body, nil
}

// ExecuteGroup sends once. Telegram permission/rate/migration failures retain
// structured metadata, but cannot accidentally replay a destructive operation.
func (c *Client) ExecuteGroup(ctx context.Context, in GroupInput) (map[string]any, error) {
	if !c.cfg.Enabled {
		return nil, errors.New("Telegram account is disabled")
	}
	method, body, err := in.request()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if in.Operation == "photo.set" {
		raw, err = c.setPhoto(ctx, in.ChatID, in.PhotoBase64)
	} else {
		raw, err = c.call(ctx, method, body)
	}
	if err != nil {
		return nil, err
	}
	if !validGroupResult(in, raw) {
		return nil, errors.New("Telegram did not confirm group operation")
	}
	var result any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, errors.New("invalid Telegram group response")
	}
	return map[string]any{"operation": in.Operation, "chat_id": in.ChatID, "result": result}, nil
}
func validGroupResult(in GroupInput, raw json.RawMessage) bool {
	switch in.Operation {
	case "info":
		var v struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		}
		return json.Unmarshal(raw, &v) == nil && v.ID == in.ChatID && (v.Type == "group" || v.Type == "supergroup")
	case "count":
		var n int64
		return json.Unmarshal(raw, &n) == nil && n >= 0
	case "member":
		return validMember(raw, in.UserID)
	case "administrators":
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil || list == nil {
			return false
		}
		for _, v := range list {
			if !validMember(v, 0) {
				return false
			}
		}
		return true
	case "invite.create", "invite.edit", "invite.revoke":
		var v struct {
			Link string `json:"invite_link"`
		}
		return json.Unmarshal(raw, &v) == nil && validInvite(v.Link)
	case "topics.create":
		var v struct {
			ID int64 `json:"message_thread_id"`
		}
		return json.Unmarshal(raw, &v) == nil && v.ID > 0
	default:
		var ok bool
		return json.Unmarshal(raw, &ok) == nil && ok
	}
}
func validMember(raw json.RawMessage, userID int64) bool {
	var v struct {
		Status string `json:"status"`
		User   struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &v) != nil || v.User.ID <= 0 || (userID != 0 && userID != v.User.ID) {
		return false
	}
	switch v.Status {
	case "creator", "administrator", "member", "restricted", "left", "kicked":
		return true
	}
	return false
}
