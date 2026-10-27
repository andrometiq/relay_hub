package whatsapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGroupManagementChecksOwnership(t *testing.T) {
	for _, tc := range []struct {
		in           Input
		method, path string
	}{
		{Input{Operation: "groups.info", ID: "group-1"}, "GET", "/group-1"},
		{Input{Operation: "groups.update", ID: "group-1", Subject: "Orders", Description: "Team"}, "POST", "/group-1"},
		{Input{Operation: "groups.delete", ID: "group-1"}, "DELETE", "/group-1"},
		{Input{Operation: "groups.invite.create", ID: "group-1"}, "POST", "/group-1/invite_link"},
		{Input{Operation: "groups.invite.get", ID: "group-1"}, "GET", "/group-1/invite_link"},
		{Input{Operation: "groups.invite.reset", ID: "group-1"}, "POST", "/group-1/invite_link"},
		{Input{Operation: "groups.participants.remove", ID: "group-1", Participants: []string{"919999999999"}}, "DELETE", "/group-1/participants"},
		{Input{Operation: "groups.requests.list", ID: "group-1"}, "GET", "/group-1/join_requests"},
		{Input{Operation: "groups.requests.approve", ID: "group-1", JoinRequests: []string{"request-1"}}, "POST", "/group-1/join_requests"},
		{Input{Operation: "groups.requests.reject", ID: "group-1", JoinRequests: []string{"request-1"}}, "DELETE", "/group-1/join_requests"},
	} {
		t.Run(tc.in.Operation, func(t *testing.T) {
			mutations := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v25.0/111/groups" {
					w.Write([]byte(`{"data":{"groups":[{"id":"group-1"}]}}`))
					return
				}
				mutations++
				if r.Method != tc.method || r.URL.Path != "/v25.0"+tc.path {
					t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
				}
				if strings.Contains(tc.in.Operation, "participants") {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					people, ok := body["participants"].([]any)
					if !ok || len(people) != 1 || people[0].(map[string]any)["user"] != "919999999999" {
						t.Error("invalid participant payload")
					}
				}
				w.Write([]byte(`{"success":true}`))
			}))
			defer provider.Close()
			svc, _ := New([]Account{testAccount()})
			svc.base = provider.URL
			if _, err := svc.Execute(context.Background(), testAccount(), tc.in); err != nil {
				t.Fatal(err)
			}
			tc.in.ID = "foreign-group"
			if _, err := svc.Execute(context.Background(), testAccount(), tc.in); err == nil {
				t.Fatal("foreign group accepted")
			}
			if mutations != 1 {
				t.Fatal("foreign group reached mutation endpoint")
			}
		})
	}
}
func TestMediaValidationAndPayload(t *testing.T) {
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["document"].(map[string]any)["filename"] != "invoice.pdf" {
			t.Error("missing filename")
		}
		w.Write([]byte(`{"messages":[{"id":"wamid.media"}]}`))
	}))
	defer provider.Close()
	svc, _ := New([]Account{testAccount()})
	svc.base = provider.URL
	in := Input{Operation: "messages.media", To: "919999999999", MediaType: "document", MediaURL: "https://example.com/invoice.pdf", Filename: "invoice.pdf"}
	if _, err := svc.Execute(context.Background(), testAccount(), in); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"http://example.com/file", "https://user:secret@example.com/file", "file:///etc/passwd"} {
		in.MediaURL = link
		if _, err := svc.Execute(context.Background(), testAccount(), in); err == nil {
			t.Fatal("unsafe media accepted")
		}
	}
	if requests != 1 {
		t.Fatal("invalid input reached provider")
	}
}
