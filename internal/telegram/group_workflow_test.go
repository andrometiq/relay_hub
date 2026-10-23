package telegram

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The simulator maintains chat/topic/member state across calls; this catches
// lifecycle and routing mistakes that independent canned responses miss.
func TestSimulatedGroupLifecycle(t *testing.T) {
	const group int64 = -1001234567890
	topic, closed, joined, banned, botPresent := false, false, false, false, true
	title := "Support"
	accepted := 0
	c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in["chat_id"] != float64(group) {
			t.Error("operation escaped group")
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		reject := func(code int) {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"simulation rejection"}`, code)
		}
		if !botPresent {
			reject(403)
			return
		}
		result := `true`
		switch method {
		case "setChatTitle":
			title = in["title"].(string)
		case "getChat":
			result = fmt.Sprintf(`{"id":%d,"type":"supergroup","title":%q}`, group, title)
		case "createChatInviteLink":
			result = `{"invite_link":"https://t.me/+simulation"}`
		case "approveChatJoinRequest":
			joined = true
		case "getChatMember":
			status := "left"
			if joined {
				status = "member"
			}
			if banned {
				status = "kicked"
			}
			result = fmt.Sprintf(`{"status":%q,"user":{"id":81}}`, status)
		case "banChatMember":
			banned = true
			joined = false
		case "unbanChatMember":
			banned = false
		case "createForumTopic":
			topic = true
			closed = false
			result = `{"message_thread_id":501,"name":"Support"}`
		case "closeForumTopic":
			if !topic {
				reject(400)
				return
			}
			closed = true
		case "reopenForumTopic":
			if !topic {
				reject(400)
				return
			}
			closed = false
		case "deleteForumTopic":
			topic = false
		case "sendMessage":
			if !topic || closed || in["message_thread_id"] != float64(501) {
				reject(400)
				return
			}
			reply, ok := in["reply_parameters"].(map[string]any)
			if !ok || reply["message_id"] != float64(88) {
				t.Error("reply escaped origin message")
			}
			if in["text"] != "வணக்கம் — support" {
				t.Error("message modified")
			}
			accepted++
			result = fmt.Sprintf(`{"message_id":%d}`, 1000+accepted)
		case "leaveChat":
			botPresent = false
		default:
			t.Errorf("unimplemented simulator operation %s", method)
			reject(400)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":%s}`, result)
	})
	call := func(in GroupInput) map[string]any {
		t.Helper()
		in.ChatID = group
		result, err := c.ExecuteGroup(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	call(GroupInput{Operation: "title", Title: "Customer support"})
	info := call(GroupInput{Operation: "info"})
	if info["result"].(map[string]any)["title"] != "Customer support" {
		t.Fatal("group mutation not visible")
	}
	call(GroupInput{Operation: "invite.create", CreatesJoinRequest: boolPointer(true)})
	call(GroupInput{Operation: "requests.approve", UserID: 81})
	if call(GroupInput{Operation: "member", UserID: 81})["result"].(map[string]any)["status"] != "member" {
		t.Fatal("join not visible")
	}
	call(GroupInput{Operation: "members.ban", UserID: 81})
	if call(GroupInput{Operation: "member", UserID: 81})["result"].(map[string]any)["status"] != "kicked" {
		t.Fatal("ban not visible")
	}
	call(GroupInput{Operation: "members.unban", UserID: 81})
	if call(GroupInput{Operation: "member", UserID: 81})["result"].(map[string]any)["status"] != "left" {
		t.Fatal("unban incorrectly added member")
	}
	call(GroupInput{Operation: "topics.create", Name: "Support"})
	send := func() error {
		return c.SendMessageToThread(t.Context(), group, "வணக்கம் — support", 88, 501)
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	call(GroupInput{Operation: "topics.close", ThreadID: 501})
	if err := send(); err == nil {
		t.Fatal("closed topic accepted send")
	}
	call(GroupInput{Operation: "topics.reopen", ThreadID: 501})
	if err := send(); err != nil {
		t.Fatal(err)
	}
	call(GroupInput{Operation: "topics.delete", ThreadID: 501})
	if err := send(); err == nil {
		t.Fatal("deleted topic accepted send")
	}
	call(GroupInput{Operation: "leave"})
	if _, err := c.ExecuteGroup(t.Context(), GroupInput{Operation: "info", ChatID: group}); err == nil {
		t.Fatal("removed bot retained access")
	}
	if accepted != 2 {
		t.Fatalf("unexpected sends or blind retry: %d", accepted)
	}
}
