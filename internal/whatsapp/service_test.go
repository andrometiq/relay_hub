package whatsapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testAccount() Account {
	return Account{ID: "site-a", Site: "http://yrp.site:8002", CoreToken: strings.Repeat("a", 64), AccessToken: "provider-secret", Version: "v25.0", PhoneID: "111", WABAID: "222", Templates: true, Groups: true, Calls: true}
}
func TestOperations(t *testing.T) {
	cases := []struct {
		input              Input
		method, path, kind string
	}{
		{Input{Operation: "messages.send", To: "919999999999", Body: "hello", ReplyTo: "wamid.reply"}, "POST", "/v25.0/111/messages", "text"},
		{Input{Operation: "messages.react", To: "919999999999", ReplyTo: "wamid.reply", Emoji: "👍"}, "POST", "/v25.0/111/messages", "reaction"},
		{Input{Operation: "templates.list"}, "GET", "/v25.0/222/message_templates", ""},
		{Input{Operation: "templates.create", Name: "order_update", Language: "en_US", Category: "UTILITY", Components: []map[string]any{{"type": "BODY", "text": "Hello"}}}, "POST", "/v25.0/222/message_templates", ""},
		{Input{Operation: "templates.send", To: "919999999999", Name: "order_update", Language: "en_US"}, "POST", "/v25.0/111/messages", "template"},
		{Input{Operation: "groups.list"}, "GET", "/v25.0/111/groups", ""},
		{Input{Operation: "groups.create", Subject: "Orders"}, "POST", "/v25.0/111/groups", ""},
		{Input{Operation: "groups.send", To: "group-1", Body: "Hello"}, "POST", "/v25.0/111/messages", "text"},
		{Input{Operation: "calls.permission.get", To: "919999999999"}, "GET", "/v25.0/111/call_permissions", ""},
		{Input{Operation: "calls.permission.request", To: "919999999999"}, "POST", "/v25.0/111/messages", "interactive"},
		{Input{Operation: "calls.action", Action: "connect", To: "919999999999", SDPType: "offer", SDP: "v=0\r\n", CallbackData: "relay-call:fixture"}, "POST", "/v25.0/111/calls", ""},
		{Input{Operation: "calls.action", Action: "terminate", ID: "call-1"}, "POST", "/v25.0/111/calls", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input.Operation+tc.input.Action, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.input.Operation == "groups.send" && r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/groups") {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"data":{"groups":[{"id":"group-1"}]}}`))
					return
				}
				if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer provider-secret" {
					t.Errorf("wrong provider request: %s %s", r.Method, r.URL.Path)
				}
				if tc.input.Operation == "calls.action" && tc.input.Action == "connect" {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["biz_opaque_callback_data"] != "relay-call:fixture" {
						t.Error("call recovery tracking missing")
					}
				}
				if tc.kind != "" {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["type"] != tc.kind {
						t.Errorf("wrong message type")
					}
					if tc.input.Operation == "groups.send" && body["recipient_type"] != "group" {
						t.Errorf("missing group recipient")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"data":[],"paging":{"next":"https://example/?access_token=secret","cursors":{"after":"cursor"}}}`))
			}))
			defer server.Close()
			s, e := New([]Account{testAccount()})
			if e != nil {
				t.Fatal(e)
			}
			s.base = server.URL
			result, e := s.Execute(context.Background(), testAccount(), tc.input)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "access_token") {
				t.Fatal("paging URL leaked")
			}
		})
	}
}
func TestValidationAndCapabilityChecks(t *testing.T) {
	s, _ := New([]Account{testAccount()})
	a := testAccount()
	a.Groups = false
	a.Calls = false
	for _, in := range []Input{{Operation: "groups.send", To: "g", Body: "x"}, {Operation: "calls.action", Action: "connect"}, {Operation: "templates.send", To: "../../x"}, {Operation: "unknown"}} {
		if _, e := s.Execute(context.Background(), a, in); e == nil {
			t.Fatal("accepted invalid request")
		}
	}
	if _, ok := s.Authenticate(strings.Repeat("z", 64)); ok {
		t.Fatal("wrong token accepted")
	}
}
func TestProviderErrorsAreRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"provider-secret"}}`))
	}))
	defer server.Close()
	s, _ := New([]Account{testAccount()})
	s.base = server.URL
	_, e := s.Execute(context.Background(), testAccount(), Input{Operation: "templates.list"})
	if e == nil || strings.Contains(e.Error(), "provider-secret") {
		t.Fatal("bad provider error handling")
	}
}

func TestReplaceAccountsIsAtomicOnInvalidConfiguration(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := Account{ID: "a", Site: "https://erp.example.com", AccessToken: "token", PhoneID: "1234567", WABAID: "1234567", Version: "v23.0"}
	if err := s.ReplaceAccounts([]Account{valid}); err != nil {
		t.Fatal(err)
	}
	if got := len(s.AccountsSnapshot()); got != 1 {
		t.Fatalf("got %d accounts", got)
	}
	if err := s.ReplaceAccounts([]Account{{ID: "bad id", Site: "https://erp.example.com"}}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if got := len(s.AccountsSnapshot()); got != 1 {
		t.Fatalf("invalid replacement changed registry: %d", got)
	}
}

func TestTemplateAuthoringParametersPreserved(t *testing.T) {
	a := testAccount()
	s, _ := New([]Account{a})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("locale") != "en_US" {
			t.Errorf("provider errors not requested in English: %s", r.URL.RawQuery)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["parameter_format"] != "named" || body["message_send_ttl_seconds"] != float64(600) {
			t.Errorf("authoring settings lost: %v", body)
		}
		components := body["components"].([]any)
		if len(components) != 3 {
			t.Error("header/body/buttons lost")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"123","status":"PENDING"}`))
	}))
	defer server.Close()
	s.base = server.URL
	input := Input{Operation: "templates.create", Name: "named_update", Language: "en_US", Category: "UTILITY", ParameterFormat: "NAMED", MessageSendTTLSeconds: 600, Components: []map[string]any{{"type": "HEADER", "format": "TEXT", "text": "Update"}, {"type": "BODY", "text": "Hello {{customer}}"}, {"type": "BUTTONS", "buttons": []any{map[string]any{"type": "QUICK_REPLY", "text": "Help"}}}}}
	if _, err := s.Execute(context.Background(), a, input); err != nil {
		t.Fatal(err)
	}
	input.ParameterFormat = "invalid"
	if _, err := s.Execute(context.Background(), a, input); err == nil {
		t.Fatal("invalid parameter format accepted")
	}
	if calls != 1 {
		t.Fatal("invalid authoring reached provider")
	}
}

func TestTemplateUpdateKeepsNamedParameterFormat(t *testing.T) {
	a := testAccount()
	s, _ := New([]Account{a})
	var edit map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[{"id":"123","name":"named_update"}]}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	s.base = server.URL
	input := Input{Operation: "templates.update", ID: "123", Name: "named_update", ParameterFormat: "NAMED", Components: []map[string]any{{"type": "BODY", "text": "Hello {{customer}}, thanks"}}}
	if _, err := s.Execute(context.Background(), a, input); err != nil {
		t.Fatal(err)
	}
	if edit["parameter_format"] != "named" {
		t.Fatalf("named parameter format not sent on edit: %v", edit)
	}
	input.ParameterFormat = "invalid"
	if _, err := s.Execute(context.Background(), a, input); err == nil {
		t.Fatal("invalid parameter format accepted on edit")
	}
}
