package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// The fixtures implement the documented Graph Groups contract. They are not
// evidence that a particular business account is eligible for the live API.
// Sources: https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/reference
// https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/groups-messaging
func TestGroupLifecycleSimulation(t *testing.T) {
	type group struct {
		subject, description, mode, invite string
		members, requests                  map[string]string
	}
	groups := map[string]*group{}
	sends := []map[string]any{}
	mutations, inviteVersion := 0, 1
	var expected map[string]any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Error("wrong account authorization")
			w.WriteHeader(403)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v25.0/")
		var body map[string]any
		if r.Body != nil && r.Method != "GET" {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		if r.Method != "GET" {
			mutations++
			if !reflect.DeepEqual(body, expected) {
				t.Errorf("payload mismatch\n got %#v\nwant %#v", body, expected)
			}
		}
		respond := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if path == "111/groups" {
			if r.Method == "POST" {
				groups["group-1"] = &group{subject: body["subject"].(string), description: body["description"].(string), mode: body["join_approval_mode"].(string), invite: "https://chat.whatsapp.com/synthetic-1", members: map[string]string{}, requests: map[string]string{"request-a": "919999999991", "request-b": "919999999992"}}
				respond(map[string]any{"request_id": "create-request"})
				return
			}
			data := []any{}
			for id, g := range groups {
				data = append(data, map[string]any{"id": id, "subject": g.subject})
			}
			respond(map[string]any{"data": map[string]any{"groups": data}})
			return
		}
		if path == "111/messages" {
			sends = append(sends, body)
			respond(map[string]any{"messages": []any{map[string]any{"id": fmt.Sprintf("wamid.group.%d", len(sends))}}})
			return
		}
		parts := strings.Split(path, "/")
		g := groups[parts[0]]
		if g == nil {
			w.WriteHeader(404)
			respond(map[string]any{"error": map[string]any{"code": 100}})
			return
		}
		if len(parts) == 1 {
			switch r.Method {
			case "GET":
				if r.URL.Query().Get("fields") != "id,subject,description,participants,join_approval_mode" {
					t.Error("metadata projection absent")
				}
				respond(map[string]any{"id": parts[0], "subject": g.subject, "description": g.description, "join_approval_mode": g.mode, "participants": g.members})
			case "POST":
				g.subject = body["subject"].(string)
				g.description = body["description"].(string)
				respond(map[string]any{"request_id": "update-request"})
			case "DELETE":
				delete(groups, parts[0])
				respond(map[string]any{"request_id": "delete-request"})
			default:
				t.Error("unsupported metadata method")
				w.WriteHeader(405)
			}
			return
		}
		switch parts[1] {
		case "invite_link":
			if r.Method == "POST" {
				inviteVersion++
				g.invite = fmt.Sprintf("https://chat.whatsapp.com/synthetic-%d", inviteVersion)
			} else if r.Method != "GET" {
				t.Error("unsupported invite method")
			}
			respond(map[string]any{"invite_link": g.invite})
		case "join_requests":
			if r.Method == "GET" {
				if r.URL.Query().Get("limit") != "50" {
					t.Error("unbounded join requests")
				}
				rows := []any{}
				for id, phone := range g.requests {
					rows = append(rows, map[string]any{"join_request_id": id, "wa_id": phone})
				}
				respond(map[string]any{"data": rows})
				return
			}
			ids := body["join_requests"].([]any)
			for _, value := range ids {
				id := value.(string)
				if r.Method == "POST" {
					g.members[g.requests[id]] = id
				}
				delete(g.requests, id)
			}
			respond(map[string]any{"success": true})
		case "participants":
			if r.Method != "DELETE" {
				t.Error("unsupported participant-add request")
			}
			for _, value := range body["participants"].([]any) {
				delete(g.members, value.(map[string]any)["user"].(string))
			}
			respond(map[string]any{"success": true})
		default:
			t.Errorf("unexpected path %s", path)
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	s, _ := New([]Account{testAccount()})
	s.base = provider.URL
	call := func(in Input, want map[string]any) map[string]any {
		t.Helper()
		expected = want
		result, err := s.Execute(context.Background(), testAccount(), in)
		if err != nil {
			t.Fatal(in.Operation, err)
		}
		return result
	}
	payload := func(kv ...any) map[string]any {
		m := map[string]any{"messaging_product": "whatsapp"}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	t.Run("create_requires_join_approval", func(t *testing.T) {
		call(Input{Operation: "groups.create", Subject: " Orders ", Description: "Customer support"}, payload("subject", "Orders", "description", "Customer support", "join_approval_mode", "approval_required"))
		if groups["group-1"].mode != "approval_required" {
			t.Fatal("approval lost")
		}
	})
	t.Run("list_and_info", func(t *testing.T) {
		call(Input{Operation: "groups.list"}, nil)
		r := call(Input{Operation: "groups.info", ID: "group-1"}, nil)
		if r["subject"] != "Orders" {
			t.Fatal("created group not readable")
		}
	})
	t.Run("update", func(t *testing.T) {
		call(Input{Operation: "groups.update", ID: "group-1", Subject: "Revised", Description: "தமிழ் support"}, payload("subject", "Revised", "description", "தமிழ் support"))
		if groups["group-1"].description != "தமிழ் support" {
			t.Fatal("unicode lost")
		}
	})
	t.Run("read_and_reset_invite", func(t *testing.T) {
		old := call(Input{Operation: "groups.invite.get", ID: "group-1"}, nil)["invite_link"]
		fresh := call(Input{Operation: "groups.invite.reset", ID: "group-1"}, payload())["invite_link"]
		if old == fresh || call(Input{Operation: "groups.invite.get", ID: "group-1"}, nil)["invite_link"] != fresh {
			t.Fatal("invite reset not observable")
		}
	})
	t.Run("approve_reject_and_remove_members", func(t *testing.T) {
		call(Input{Operation: "groups.requests.list", ID: "group-1"}, nil)
		call(Input{Operation: "groups.requests.approve", ID: "group-1", JoinRequests: []string{"request-a"}}, payload("join_requests", []any{"request-a"}))
		call(Input{Operation: "groups.requests.reject", ID: "group-1", JoinRequests: []string{"request-b"}}, payload("join_requests", []any{"request-b"}))
		if len(groups["group-1"].requests) != 0 || len(groups["group-1"].members) != 1 {
			t.Fatal("join request state mismatch")
		}
		call(Input{Operation: "groups.participants.remove", ID: "group-1", Participants: []string{"919999999991"}}, payload("participants", []any{map[string]any{"user": "919999999991"}}))
		if len(groups["group-1"].members) != 0 {
			t.Fatal("participant remains")
		}
	})
	t.Run("text_media_template_pin_unpin", func(t *testing.T) {
		call(Input{Operation: "groups.send", To: "group-1", Body: "வணக்கம் team"}, payload("recipient_type", "group", "to", "group-1", "type", "text", "text", map[string]any{"body": "வணக்கம் team"}))
		for _, kind := range []string{"image", "video", "audio", "document", "sticker"} {
			in := Input{Operation: "groups.media", To: "group-1", MediaType: kind, MediaID: "123456"}
			media := map[string]any{"id": "123456"}
			if kind == "document" {
				in.Caption = "Invoice"
				in.Filename = "invoice.pdf"
				media["caption"] = "Invoice"
				media["filename"] = "invoice.pdf"
			}
			call(in, payload("recipient_type", "group", "to", "group-1", "type", kind, kind, media))
		}
		call(Input{Operation: "groups.template", To: "group-1", Name: "order_update", Language: "en_US"}, payload("recipient_type", "group", "to", "group-1", "type", "template", "template", map[string]any{"name": "order_update", "language": map[string]any{"code": "en_US"}, "components": []any{}}))
		call(Input{Operation: "groups.pin", To: "group-1", ReplyTo: "wamid.group.1", Action: "pin", ExpirationDays: 3}, payload("recipient_type", "group", "to", "group-1", "type", "pin", "pin", map[string]any{"type": "pin", "message_id": "wamid.group.1", "expiration_days": float64(3)}))
		call(Input{Operation: "groups.pin", To: "group-1", ReplyTo: "wamid.group.1", Action: "unpin"}, payload("recipient_type", "group", "to", "group-1", "type", "pin", "pin", map[string]any{"type": "unpin", "message_id": "wamid.group.1"}))
		if len(sends) != 9 {
			t.Fatalf("wrong send count %d", len(sends))
		}
	})
	t.Run("delete_then_reject_stale_group", func(t *testing.T) {
		call(Input{Operation: "groups.delete", ID: "group-1"}, payload())
		before := mutations
		_, err := s.Execute(context.Background(), testAccount(), Input{Operation: "groups.send", To: "group-1", Body: "stale"})
		if err == nil || mutations != before {
			t.Fatal("deleted group accepted")
		}
	})
}

func TestGroupPaginationAndProviderFailureSimulation(t *testing.T) {
	for _, status := range []int{200, 403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			listings, mutations := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/groups") {
					listings++
					if r.URL.Query().Get("after") == "" {
						fmt.Fprint(w, `{"data":{"groups":[{"id":"first-group"}]},"paging":{"cursors":{"after":"page-2"},"next":"https://evil.invalid/?access_token=secret"}}`)
					} else {
						if r.URL.Query().Get("after") != "page-2" {
							t.Error("bad cursor")
						}
						fmt.Fprint(w, `{"data":{"groups":[{"id":"second-group"}]}}`)
					}
					return
				}
				mutations++
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, `{"messages":[{"id":"wamid.group"}]}`)
				} else {
					fmt.Fprint(w, `{"error":{"message":"private-provider-token"}}`)
				}
			}))
			defer server.Close()
			s, _ := New([]Account{testAccount()})
			s.base = server.URL
			_, err := s.Execute(context.Background(), testAccount(), Input{Operation: "groups.send", To: "second-group", Body: "paginated"})
			if listings != 2 || mutations != 1 {
				t.Fatalf("pagination/retry counts %d %d", listings, mutations)
			}
			if status == 200 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var pe *ProviderError
				if !errors.As(err, &pe) || pe.Status != status || strings.Contains(err.Error(), "private-provider-token") {
					t.Fatalf("unsafe provider result %v", err)
				}
			}
		})
	}
}

func TestGroupValidationSimulation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("invalid operation reached network") }))
	defer server.Close()
	s, _ := New([]Account{testAccount()})
	s.base = server.URL
	for _, op := range []string{"groups.participants.add", "groups.invite.revoke"} {
		if _, err := s.Execute(context.Background(), testAccount(), Input{Operation: op, ID: "group-1"}); err == nil {
			t.Error("unsupported operation accepted", op)
		}
	}
	disabled := testAccount()
	disabled.Groups = false
	for _, op := range []string{"groups.create", "groups.list", "groups.info", "groups.update", "groups.delete", "groups.invite.get", "groups.invite.reset", "groups.participants.remove", "groups.requests.list", "groups.requests.approve", "groups.requests.reject", "groups.send", "groups.media", "groups.template", "groups.pin"} {
		if _, err := s.Execute(context.Background(), disabled, Input{Operation: op, ID: "group-1", To: "group-1", Subject: "Group", Body: "test"}); err == nil {
			t.Error("disabled group accepted", op)
		}
	}
	// Queue validation uses exactly the production payload builder, without any I/O.
	invalid := []Input{{Operation: "groups.create", Subject: "Group", JoinApprovalMode: "invalid"}, {Operation: "groups.create", Subject: strings.Repeat("界", 129)}, {Operation: "groups.media", To: "group-1", MediaType: "document", MediaURL: "file:///secret"}, {Operation: "groups.template", To: "group-1", Name: "Bad Name", Language: "en"}, {Operation: "groups.pin", To: "group-1", ReplyTo: "wamid.a", Action: "pin", ExpirationDays: 31}, {Operation: "groups.pin", To: "group-1", ReplyTo: "wamid.a", Action: "unpin", ExpirationDays: 1}, {Operation: "groups.send", To: "../group", Body: "unsafe"}, {Operation: "groups.send", To: "group-1", Body: strings.Repeat("界", 4097)}, {Operation: "groups.requests.approve", ID: "group-1"}, {Operation: "groups.participants.remove", ID: "group-1", Participants: []string{"invalid"}}, {Operation: "groups.list", After: strings.Repeat("a", 2049)}}
	for _, in := range invalid {
		if _, err := s.execute(context.Background(), testAccount(), in, true); err == nil {
			t.Errorf("invalid group input accepted %#v", in)
		}
	}
	if calls != 0 {
		t.Fatal("validation performed network I/O")
	}
}

func TestGroupPartialResultsRemainExplicit(t *testing.T) {
	// A 2xx response can contain both successes and failures. Relay must return
	// every per-item result, without inventing an all-success status or retrying.
	for _, tc := range []struct {
		operation, response string
		in                  Input
	}{
		{"approve", `{"messaging_product":"whatsapp","approved_join_requests":["request-a"],"failed_join_requests":[{"join_request_id":"request-b","errors":[{"code":131203,"message":"Participant has not accepted terms"}]}],"errors":[]}`, Input{Operation: "groups.requests.approve", ID: "group-1", JoinRequests: []string{"request-a", "request-b"}}},
		{"reject", `{"messaging_product":"whatsapp","rejected_join_requests":["request-a"],"failed_join_requests":[{"join_request_id":"request-b","errors":[{"code":100,"message":"Request already handled"}]}]}`, Input{Operation: "groups.requests.reject", ID: "group-1", JoinRequests: []string{"request-a", "request-b"}}},
		{"remove", `{"messaging_product":"whatsapp","removed_participants":[{"user":"919999999991"}],"failed_participants":[{"user":"919999999992","errors":[{"code":100,"message":"Not a member"}]}]}`, Input{Operation: "groups.participants.remove", ID: "group-1", Participants: []string{"919999999991", "919999999992"}}},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			submissions := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/groups") {
					fmt.Fprint(w, `{"data":{"groups":[{"id":"group-1"}]}}`)
					return
				}
				submissions++
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			s, _ := New([]Account{testAccount()})
			s.base = server.URL
			result, err := s.Execute(context.Background(), testAccount(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			var expected map[string]any
			json.Unmarshal([]byte(tc.response), &expected)
			if !reflect.DeepEqual(result, expected) || submissions != 1 {
				t.Fatalf("partial response changed or retried: %#v", result)
			}
		})
	}
}

func TestGroupJoinModesAndRequestPagination(t *testing.T) {
	for _, mode := range []string{"approval_required", "auto_approve"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["join_approval_mode"] != mode {
					t.Error("wrong join mode")
				}
				fmt.Fprint(w, `{"request_id":"create-request"}`)
			}))
			defer server.Close()
			s, _ := New([]Account{testAccount()})
			s.base = server.URL
			if _, err := s.Execute(context.Background(), testAccount(), Input{Operation: "groups.create", Subject: "Mode test", JoinApprovalMode: mode}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("creation retried")
			}
		})
	}
	t.Run("join-request-cursor", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/groups") {
				fmt.Fprint(w, `{"data":{"groups":[{"id":"group-1"}]}}`)
				return
			}
			if r.Method != "GET" || r.URL.Query().Get("after") != "opaque+/cursor=" || r.URL.Query().Get("limit") != "50" {
				t.Error("join-request cursor or limit lost")
			}
			fmt.Fprint(w, `{"data":[{"join_request_id":"request-c","wa_id":"919999999993"}],"paging":{"cursors":{"after":"next-cursor"},"next":"https://graph.facebook.com/?access_token=SECRET"}}`)
		}))
		defer server.Close()
		s, _ := New([]Account{testAccount()})
		s.base = server.URL
		result, err := s.Execute(context.Background(), testAccount(), Input{Operation: "groups.requests.list", ID: "group-1", After: "opaque+/cursor="})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "SECRET") || !strings.Contains(string(raw), "next-cursor") {
			t.Fatal("paging redaction/cursor failure")
		}
	})
	t.Run("repeated-ownership-cursor-terminates", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if !strings.HasSuffix(r.URL.Path, "/groups") {
				t.Error("foreign group reached mutation")
			}
			fmt.Fprint(w, `{"data":{"groups":[]},"paging":{"cursors":{"after":"repeated"}}}`)
		}))
		defer server.Close()
		s, _ := New([]Account{testAccount()})
		s.base = server.URL
		if _, err := s.Execute(context.Background(), testAccount(), Input{Operation: "groups.send", To: "foreign", Body: "no"}); err == nil || calls != 2 {
			t.Fatalf("cursor loop not bounded %d %v", calls, err)
		}
	})
}
