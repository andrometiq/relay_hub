package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const groupSimulationToken = "123456:abcdefghijklmnopqrstuvwxyzABCDE"

func simulatedGroupClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	hc := &http.Client{Timeout: time.Minute, Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || !strings.HasPrefix(r.URL.Path, "/bot"+groupSimulationToken+"/") {
			t.Errorf("unexpected provider origin: %s", r.URL.Host)
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})}
	c, err := NewWithHTTPClient(Config{AccountID: "group-simulation", BotToken: groupSimulationToken, Enabled: true}, hc)
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Timeout != 12*time.Second || c.http == hc {
		t.Fatal("client deadline/copy policy missing")
	}
	return c
}
func boolPointer(v bool) *bool { return &v }
func TestGroupOperationContracts(t *testing.T) {
	const chat int64 = -1001234567890
	base := map[string]any{"chat_id": float64(chat)}
	for _, tc := range []struct {
		in     GroupInput
		method string
		fields map[string]any
		result string
	}{
		{GroupInput{Operation: "info"}, "getChat", nil, `{"id":-1001234567890,"type":"supergroup","title":"Test"}`},
		{GroupInput{Operation: "member", UserID: 27}, "getChatMember", map[string]any{"user_id": float64(27)}, `{"status":"member","user":{"id":27}}`},
		{GroupInput{Operation: "count"}, "getChatMemberCount", nil, `4`},
		{GroupInput{Operation: "administrators"}, "getChatAdministrators", nil, `[{"status":"administrator","user":{"id":27}}]`},
		{GroupInput{Operation: "title", Title: "வாடிக்கையாளர்கள் 🔧"}, "setChatTitle", map[string]any{"title": "வாடிக்கையாளர்கள் 🔧"}, `true`},
		{GroupInput{Operation: "description", Description: "தமிழ் மற்றும் English"}, "setChatDescription", map[string]any{"description": "தமிழ் மற்றும் English"}, `true`},
		{GroupInput{Operation: "photo.delete"}, "deleteChatPhoto", nil, `true`},
		{GroupInput{Operation: "permissions", Permissions: map[string]bool{"can_send_messages": false, "can_send_photos": true}}, "setChatPermissions", map[string]any{"permissions": map[string]any{"can_send_messages": false, "can_send_photos": true}, "use_independent_chat_permissions": true}, `true`},
		{GroupInput{Operation: "invite.create", Name: "Support", ExpireDate: 1900000000, MemberLimit: 10}, "createChatInviteLink", map[string]any{"name": "Support", "expire_date": float64(1900000000), "member_limit": float64(10)}, `{"invite_link":"https://t.me/+synthetic-one"}`},
		{GroupInput{Operation: "invite.edit", InviteLink: "https://t.me/+synthetic-one", Name: "Review", CreatesJoinRequest: boolPointer(true)}, "editChatInviteLink", map[string]any{"invite_link": "https://t.me/+synthetic-one", "name": "Review", "creates_join_request": true}, `{"invite_link":"https://t.me/+synthetic-one"}`},
		{GroupInput{Operation: "invite.revoke", InviteLink: "https://t.me/+synthetic-one"}, "revokeChatInviteLink", map[string]any{"invite_link": "https://t.me/+synthetic-one"}, `{"invite_link":"https://t.me/+synthetic-one","is_revoked":true}`},
		{GroupInput{Operation: "requests.approve", UserID: 27}, "approveChatJoinRequest", map[string]any{"user_id": float64(27)}, `true`},
		{GroupInput{Operation: "requests.reject", UserID: 27}, "declineChatJoinRequest", map[string]any{"user_id": float64(27)}, `true`},
		{GroupInput{Operation: "members.ban", UserID: 27, UntilDate: 1900000000, RevokeMessages: boolPointer(false)}, "banChatMember", map[string]any{"user_id": float64(27), "until_date": float64(1900000000), "revoke_messages": false}, `true`},
		{GroupInput{Operation: "members.unban", UserID: 27}, "unbanChatMember", map[string]any{"user_id": float64(27), "only_if_banned": true}, `true`},
		{GroupInput{Operation: "members.restrict", UserID: 27, Permissions: map[string]bool{"can_send_messages": false}}, "restrictChatMember", map[string]any{"user_id": float64(27), "until_date": float64(0), "permissions": map[string]any{"can_send_messages": false}, "use_independent_chat_permissions": true}, `true`},
		{GroupInput{Operation: "members.promote", UserID: 27, AdministratorRights: map[string]bool{"can_manage_topics": true, "can_invite_users": false}}, "promoteChatMember", map[string]any{"user_id": float64(27), "can_manage_topics": true, "can_invite_users": false}, `true`},
		{GroupInput{Operation: "members.ban_sender", SenderChatID: -100123}, "banChatSenderChat", map[string]any{"sender_chat_id": float64(-100123)}, `true`},
		{GroupInput{Operation: "members.unban_sender", SenderChatID: -100123}, "unbanChatSenderChat", map[string]any{"sender_chat_id": float64(-100123)}, `true`},
		{GroupInput{Operation: "members.title", UserID: 27, CustomTitle: "ஆதரவு"}, "setChatAdministratorCustomTitle", map[string]any{"user_id": float64(27), "custom_title": "ஆதரவு"}, `true`},
		{GroupInput{Operation: "leave"}, "leaveChat", nil, `true`},
		{GroupInput{Operation: "topics.create", Name: "Payments"}, "createForumTopic", map[string]any{"name": "Payments"}, `{"message_thread_id":82,"name":"Payments"}`},
		{GroupInput{Operation: "topics.edit", ThreadID: 82, Name: "Receipts"}, "editForumTopic", map[string]any{"message_thread_id": float64(82), "name": "Receipts"}, `true`},
		{GroupInput{Operation: "topics.close", ThreadID: 82}, "closeForumTopic", map[string]any{"message_thread_id": float64(82)}, `true`},
		{GroupInput{Operation: "topics.reopen", ThreadID: 82}, "reopenForumTopic", map[string]any{"message_thread_id": float64(82)}, `true`},
		{GroupInput{Operation: "topics.delete", ThreadID: 82}, "deleteForumTopic", map[string]any{"message_thread_id": float64(82)}, `true`},
		{GroupInput{Operation: "topics.unpin_all", ThreadID: 82}, "unpinAllForumTopicMessages", map[string]any{"message_thread_id": float64(82)}, `true`},
		{GroupInput{Operation: "topics.general.edit", Name: "Main"}, "editGeneralForumTopic", map[string]any{"name": "Main"}, `true`},
		{GroupInput{Operation: "topics.general.close"}, "closeGeneralForumTopic", nil, `true`},
		{GroupInput{Operation: "topics.general.reopen"}, "reopenGeneralForumTopic", nil, `true`},
		{GroupInput{Operation: "topics.general.hide"}, "hideGeneralForumTopic", nil, `true`},
		{GroupInput{Operation: "topics.general.unhide"}, "unhideGeneralForumTopic", nil, `true`},
		{GroupInput{Operation: "topics.general.unpin_all"}, "unpinAllGeneralForumTopicMessages", nil, `true`},
		{GroupInput{Operation: "pins.pin", MessageID: 28, DisableNotification: boolPointer(true)}, "pinChatMessage", map[string]any{"message_id": float64(28), "disable_notification": true}, `true`},
		{GroupInput{Operation: "pins.unpin", MessageID: 28}, "unpinChatMessage", map[string]any{"message_id": float64(28)}, `true`},
		{GroupInput{Operation: "pins.unpin_all"}, "unpinAllChatMessages", nil, `true`},
	} {
		t.Run(tc.in.Operation, func(t *testing.T) {
			var calls int
			c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/bot"+groupSimulationToken+"/"+tc.method || r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect Bot API method")
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				want := map[string]any{}
				for k, v := range base {
					want[k] = v
				}
				for k, v := range tc.fields {
					want[k] = v
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("payload mismatch got=%v want=%v", got, want)
				}
				fmt.Fprintf(w, `{"ok":true,"result":%s}`, tc.result)
			})
			in := tc.in
			in.ChatID = chat
			out, err := c.ExecuteGroup(t.Context(), in)
			if err != nil || out["operation"] != in.Operation || out["chat_id"] != chat || calls != 1 {
				t.Fatalf("unexpected execution: %v %v %d", out, err, calls)
			}
		})
	}
}

func TestGroupRejectsInvalidInputsBeforeTransport(t *testing.T) {
	c, _ := NewWithHTTPClient(Config{AccountID: "simulation", BotToken: groupSimulationToken, Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached provider")
		return nil, nil
	})})
	for _, in := range []GroupInput{
		{Operation: "info", ChatID: 42}, {Operation: "info"}, {Operation: "info", ChatID: -(1 << 52)},
		{Operation: "create", ChatID: -12}, {Operation: "delete", ChatID: -12}, {Operation: "bot.getToken", ChatID: -12},
		{Operation: "member", ChatID: -12}, {Operation: "members.ban", ChatID: -12, UserID: -7},
		{Operation: "title", ChatID: -12, Title: " "}, {Operation: "title", ChatID: -12, Title: strings.Repeat("💬", 129)},
		{Operation: "description", ChatID: -12, Description: strings.Repeat("💬", 256)},
		{Operation: "permissions", ChatID: -12}, {Operation: "permissions", ChatID: -12, Permissions: map[string]bool{"is_owner": true}},
		{Operation: "members.promote", ChatID: -12, UserID: 1, AdministratorRights: map[string]bool{"bot_token": true}},
		{Operation: "members.restrict", ChatID: -12, UserID: 1, Permissions: map[string]bool{"can_send_messages": false}, UntilDate: -1},
		{Operation: "invite.create", ChatID: -12, Name: strings.Repeat("x", 33)},
		{Operation: "invite.create", ChatID: -12, MemberLimit: 100000},
		{Operation: "invite.create", ChatID: -12, MemberLimit: 5, CreatesJoinRequest: boolPointer(true)},
		{Operation: "invite.edit", ChatID: -12, InviteLink: "https://attacker.example/+abc"},
		{Operation: "invite.revoke", ChatID: -12, InviteLink: "https://t.me@attacker.example/+abc"},
		{Operation: "topics.create", ChatID: -12, Name: strings.Repeat("x", 129)},
		{Operation: "topics.edit", ChatID: -12, Name: "Valid"}, {Operation: "topics.close", ChatID: -12, ThreadID: -1},
		{Operation: "pins.pin", ChatID: -12},
		{Operation: "members.title", ChatID: -12, UserID: 27, CustomTitle: "Admin🌟"},
		{Operation: "members.title", ChatID: -12, UserID: 27, CustomTitle: strings.Repeat("A", 17)},
		{Operation: "members.ban_sender", ChatID: -12, SenderChatID: 27},
		{Operation: "members.unban_sender", ChatID: -12},
		{Operation: "topics.general.edit", ChatID: -12},
	} {
		t.Run(fmt.Sprintf("%s/%d/%s", in.Operation, in.ChatID, in.Name), func(t *testing.T) {
			if _, err := c.ExecuteGroup(t.Context(), in); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
func TestGroupErrorsDoNotRetryOrExposeProviderSecrets(t *testing.T) {
	for _, tc := range []struct {
		name                string
		status, code, retry int
		migrate             int64
	}{
		{"bad request", 400, 400, 0, 0}, {"missing administrator rights", 403, 403, 0, 0}, {"rate limited", 429, 429, 7, 0}, {"migrated group", 400, 400, 0, -1009876543210}, {"provider unavailable", 503, 503, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"%s","parameters":{"retry_after":%d,"migrate_to_chat_id":%d}}`, tc.code, groupSimulationToken, tc.retry, tc.migrate)
			})
			_, err := c.ExecuteGroup(t.Context(), GroupInput{Operation: "members.ban", ChatID: -120, UserID: 91})
			var pe *APIError
			if !errors.As(err, &pe) || pe.StatusCode != tc.status || pe.ErrorCode != tc.code || pe.RetryAfter != tc.retry || pe.MigrateToChatID != tc.migrate || calls != 1 || strings.Contains(err.Error(), groupSimulationToken) {
				t.Fatalf("incorrect failure/retry: %v calls=%d", err, calls)
			}
		})
	}
}
func TestGroupRejectsFalseOrMalformedSuccess(t *testing.T) {
	for _, tc := range []struct{ op, result string }{
		{"leave", "false"}, {"leave", "{}"}, {"leave", "null"}, {"info", `{"id":-999,"type":"supergroup"}`}, {"count", "-1"}, {"administrators", "null"}, {"administrators", `[{"status":"administrator"}]`}, {"invite.create", `{"invite_link":"https://example.org/+link"}`}, {"topics.create", `{"message_thread_id":0}`},
	} {
		t.Run(tc.op+tc.result, func(t *testing.T) {
			c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"ok":true,"result":%s}`, tc.result) })
			if _, err := c.ExecuteGroup(t.Context(), GroupInput{Operation: tc.op, ChatID: -120, Name: "Topic"}); err == nil {
				t.Fatal("unconfirmed result accepted")
			}
		})
	}
}

func TestGroupRedirectAndCancellation(t *testing.T) {
	var escaped atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1) }))
	defer target.Close()
	c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
		io.WriteString(w, `{"ok":false,"error_code":302}`)
	})
	if _, err := c.ExecuteGroup(t.Context(), GroupInput{Operation: "info", ChatID: -120}); err == nil || escaped.Load() != 0 {
		t.Fatal("redirect was followed or accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ExecuteGroup(ctx, GroupInput{Operation: "leave", ChatID: -120}); err == nil {
		t.Fatal("cancelled operation accepted")
	}
}
