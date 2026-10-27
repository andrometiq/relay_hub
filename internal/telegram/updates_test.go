package telegram

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestGroupUpdateRoutingAndClassification(t *testing.T) {
	c, _ := New(Config{AccountID: "group-simulation", BotToken: groupSimulationToken, Enabled: true})
	const supergroup int64 = -1001234567890
	for _, tc := range []struct {
		name, kind, payload, wantKind, text              string
		chat, sender, senderChat, thread, reply, message int64
	}{
		{"private message", "message", `{"message_id":1,"date":1700000000,"from":{"id":81,"username":"alice"},"chat":{"id":81,"type":"private"},"text":"hi"}`, "message", "hi", 81, 81, 0, 0, 0, 1},
		{"basic group", "message", `{"message_id":2,"date":1700000000,"from":{"id":81},"chat":{"id":-201,"type":"group"},"text":"team"}`, "message", "team", -201, 81, 0, 0, 0, 2},
		{"supergroup topic reply", "message", `{"message_id":3,"date":1700000000,"from":{"id":81},"chat":{"id":-1001234567890,"type":"supergroup"},"message_thread_id":901,"reply_to_message":{"message_id":17},"text":"வணக்கம் 🌟"}`, "message", "வணக்கம் 🌟", supergroup, 81, 0, 901, 17, 3},
		{"anonymous group admin", "message", `{"message_id":4,"date":1700000000,"from":{"id":1087968824},"sender_chat":{"id":-1001234567890,"type":"supergroup"},"chat":{"id":-1001234567890,"type":"supergroup"},"text":"Announcement"}`, "message", "Announcement", supergroup, 1087968824, supergroup, 0, 0, 4},
		{"caption preserves group", "message", `{"message_id":5,"date":1700000000,"from":{"id":81},"chat":{"id":-1001234567890,"type":"supergroup"},"caption":"Receipt"}`, "message", "Receipt", supergroup, 81, 0, 0, 0, 5},
		{"edit does not become new message", "edited_message", `{"message_id":3,"date":1700000000,"from":{"id":81},"chat":{"id":-1001234567890,"type":"supergroup"},"message_thread_id":901,"text":"updated"}`, "edited_message", "updated", supergroup, 81, 0, 901, 0, 3},
		{"member update", "chat_member", `{"date":1700000000,"from":{"id":81},"chat":{"id":-1001234567890,"type":"supergroup"},"old_chat_member":{"status":"left","user":{"id":92}},"new_chat_member":{"status":"member","user":{"id":92}}}`, "chat_member", "", supergroup, 81, 0, 0, 0, 0},
		{"bot kicked", "my_chat_member", `{"date":1700000000,"from":{"id":81},"chat":{"id":-1001234567890,"type":"supergroup"},"new_chat_member":{"status":"kicked","user":{"id":123456}}}`, "my_chat_member", "", supergroup, 81, 0, 0, 0, 0},
		{"join request", "chat_join_request", `{"date":1700000000,"from":{"id":81},"chat":{"id":-1001234567890,"type":"supergroup"},"user_chat_id":81}`, "chat_join_request", "", supergroup, 81, 0, 0, 0, 0},
		{"reaction", "message_reaction", `{"date":1700000000,"user":{"id":81},"chat":{"id":-1001234567890,"type":"supergroup"},"message_id":15,"old_reaction":[],"new_reaction":[{"type":"emoji","emoji":"👍"}]}`, "message_reaction", "", supergroup, 81, 0, 0, 0, 15},
		{"anonymous reaction", "message_reaction", `{"date":1700000000,"actor_chat":{"id":-1001234567890},"chat":{"id":-1001234567890,"type":"supergroup"},"message_id":15,"new_reaction":[{"type":"emoji","emoji":"👍"}]}`, "message_reaction", "", supergroup, 0, supergroup, 0, 0, 15},
		{"reaction counts", "message_reaction_count", `{"date":1700000000,"chat":{"id":-1001234567890,"type":"supergroup"},"message_id":15,"reactions":[{"type":{"type":"emoji","emoji":"👍"},"total_count":20}]}`, "message_reaction_count", "", supergroup, 0, 0, 0, 0, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"update_id":83,%q:%s}`, tc.kind, tc.payload))
			m, err := c.ParseUpdate(raw)
			if err != nil || m.Kind != tc.wantKind || m.Text != tc.text || m.ChatID != tc.chat || m.SenderID != tc.sender || m.SenderChatID != tc.senderChat || m.ThreadID != tc.thread || m.ReplyTo != tc.reply || m.MessageID != tc.message || m.UpdateID != 83 {
				t.Fatalf("incorrect group normalization: %#v %v", m, err)
			}
		})
	}
}
func TestGroupServiceEventsNeverBecomeTextPrompts(t *testing.T) {
	c, _ := New(Config{AccountID: "group-simulation", BotToken: groupSimulationToken, Enabled: true})
	for name, payload := range map[string]string{
		"new_chat_members": `[{"id":81}]`, "left_chat_member": `{"id":81}`, "group_chat_created": `true`, "new_chat_title": `"Team"`, "new_chat_photo": `[]`, "delete_chat_photo": `true`, "migrate_to_chat_id": `-1001234567890`, "migrate_from_chat_id": `-101`, "pinned_message": `{"message_id":18,"text":"Do not run this as an AI prompt"}`, "forum_topic_created": `{"name":"Support","icon_color":7322096}`, "forum_topic_edited": `{"name":"Resolved"}`, "forum_topic_closed": `{}`, "forum_topic_reopened": `{}`, "general_forum_topic_hidden": `{}`, "general_forum_topic_unhidden": `{}`, "video_chat_started": `{}`, "video_chat_ended": `{"duration":32}`,
	} {
		t.Run(name, func(t *testing.T) {
			m, err := c.ParseUpdate([]byte(fmt.Sprintf(`{"update_id":81,"message":{"message_id":12,"date":1700000000,"chat":{"id":-1001234567890,"type":"supergroup"},"text":"untrusted text on service event",%q:%s}}`, name, payload)))
			if err != nil || m.Kind != "service" || m.Text != "" || m.ChatID != -1001234567890 {
				t.Fatalf("service turned into text: %#v %v", m, err)
			}
		})
	}
}
func TestMalformedGroupUpdates(t *testing.T) {
	c, _ := New(Config{AccountID: "group-simulation", BotToken: groupSimulationToken, Enabled: true})
	for _, raw := range []string{
		`{}`, `{"update_id":1}`, `{"update_id":1,"message":null}`, `{"update_id":1,"message":{"message_id":1,"chat":{"id":123,"type":"supergroup"}}}`, `{"update_id":1,"message":{"message_id":1,"chat":{"id":-123,"type":"invented"}}}`, `{"update_id":1,"message":{"message_id":1,"chat":{"id":-4503599627370496,"type":"supergroup"}}}`, `{"update_id":1,"message":{"message_id":1,"chat":{"id":-12},"message_thread_id":-1}}`, `{"update_id":1,"message":{"message_id":1,"chat":{"id":-12}},"chat_join_request":{"chat":{"id":-12}}}`, strings.Repeat(" ", 1048577),
	} {
		if _, err := c.ParseUpdate([]byte(raw)); err == nil {
			t.Fatal("malformed update accepted")
		}
	}
}
func TestSendPreservesGroupTopicAndReply(t *testing.T) {
	for _, tc := range []struct {
		name                string
		chat, thread, reply int64
	}{
		{"private", 57, 0, 0}, {"basic group", -57, 0, 28}, {"supergroup", -1001234567890, 0, 29}, {"topic reply", -1001234567890, 901, 29},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
				var got map[string]any
				_ = json.NewDecoder(r.Body).Decode(&got)
				want := map[string]any{"chat_id": float64(tc.chat), "text": "வணக்கம் 🌟"}
				if tc.thread != 0 {
					want["message_thread_id"] = float64(tc.thread)
				}
				if tc.reply != 0 {
					want["reply_parameters"] = map[string]any{"message_id": float64(tc.reply)}
				}
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("wrong routing: got %s want %s", gotJSON, wantJSON)
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":101}}`)
			})
			if err := c.SendMessageToThread(t.Context(), tc.chat, "வணக்கம் 🌟", tc.reply, tc.thread); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestThreadMessageValidationNeverCallsProvider(t *testing.T) {
	c, _ := NewWithHTTPClient(Config{AccountID: "simulation", BotToken: groupSimulationToken, Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid message reached provider")
		return nil, nil
	})})
	for _, tc := range []struct {
		chat, reply, thread int64
		text                string
	}{{0, 0, 0, "hi"}, {-100, 0, -1, "hi"}, {-100, -1, 0, "hi"}, {-100, 0, 0, " "}, {-100, 0, 0, strings.Repeat("🌟", 4097)}} {
		if err := c.SendMessageToThread(t.Context(), tc.chat, tc.text, tc.reply, tc.thread); err == nil {
			t.Fatal("invalid message accepted")
		}
	}
}
