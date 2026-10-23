package whatsapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFlowOperationsAndOwnership(t *testing.T) {
	for _, operation := range []string{"flows.list", "flows.get", "flows.assets", "flows.create", "flows.update", "flows.upload", "flows.publish", "flows.deprecate", "flows.delete"} {
		t.Run(operation, func(t *testing.T) {
			mutation := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer provider-secret" {
					t.Error("missing credential")
				}
				if r.Method == "GET" && r.URL.Path == "/v25.0/222/flows" {
					io.WriteString(w, `{"data":[{"id":"333"}]}`)
					return
				}
				mutation++
				if operation == "flows.upload" {
					if err := r.ParseMultipartForm(2 << 20); err != nil {
						t.Fatal(err)
					}
					f, _, err := r.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					raw, _ := io.ReadAll(f)
					if !json.Valid(raw) || r.FormValue("asset_type") != "FLOW_JSON" {
						t.Error("invalid upload")
					}
				}
				io.WriteString(w, `{"success":true}`)
			}))
			defer server.Close()
			s, _ := New([]Account{testAccount()})
			s.base = server.URL
			in := Input{Operation: operation, ID: "333", Name: "Support", Categories: []string{"CUSTOMER_SUPPORT"}, FlowJSON: json.RawMessage(`{"version":"7.3","screens":[]}`)}
			if _, err := s.Execute(context.Background(), testAccount(), in); err != nil {
				t.Fatal(err)
			}
			if operation != "flows.list" && mutation != 1 {
				t.Fatal("operation was not sent exactly once")
			}
			if operation != "flows.list" && operation != "flows.create" {
				mutation = 0
				in.ID = "999"
				if _, err := s.Execute(context.Background(), testAccount(), in); err == nil || mutation != 0 {
					t.Fatal("foreign flow accessed")
				}
			}
		})
	}
}
func TestFlowInvalidInputDoesNotReachProvider(t *testing.T) {
	s, _ := New([]Account{testAccount()})
	s.base = "http://invalid.invalid"
	for _, in := range []Input{{Operation: "flows.create", Name: "Name", Categories: []string{"INVALID"}}, {Operation: "flows.upload", ID: "333", FlowJSON: json.RawMessage(`[]`)}, {Operation: "flows.update", ID: "333", EndpointURI: "http://localhost/"}, {Operation: "flows.get", ID: "../secret"}, {Operation: "flows.list", After: strings.Repeat("a", 2049)}} {
		if _, err := s.Execute(context.Background(), testAccount(), in); err == nil || strings.Contains(err.Error(), "provider unavailable") {
			t.Fatalf("input not rejected locally: %v", err)
		}
	}
}

func TestFlowJSONDownloadKeepsCredentialsOnGraph(t *testing.T) {
	for _, host := range []string{"scontent.xx.fbcdn.net", "evil.invalid", "fbcdn.net.evil.invalid"} {
		downloads := 0
		s, err := NewWithHTTPClient([]Account{testAccount()}, &http.Client{Transport: mediaTestTransport(func(r *http.Request) (*http.Response, error) {
			body := `{"version":"7.3","screens":[]}`
			if r.URL.Host == "graph.facebook.com" {
				if r.Header.Get("Authorization") != "Bearer provider-secret" {
					t.Error("missing Graph credential")
				}
				if strings.HasSuffix(r.URL.Path, "/flows") {
					body = `{"data":[{"id":"333"}]}`
				} else {
					body = `{"data":[{"asset_type":"FLOW_JSON","download_url":"https://` + host + `/flow.json?signature=private"}]}`
				}
			} else {
				downloads++
				if r.Header.Get("Authorization") != "" {
					t.Error("bearer sent to asset CDN")
				}
				if r.URL.Host != "scontent.xx.fbcdn.net" {
					t.Error("untrusted host reached")
				}
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.Execute(context.Background(), testAccount(), Input{Operation: "flows.json", ID: "333"})
		if host == "scontent.xx.fbcdn.net" {
			if err != nil || downloads != 1 || result["flow_json"] == nil {
				t.Fatalf("download failed: %v", err)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "signature") {
				t.Error("signed URL exposed")
			}
		} else if err == nil || downloads != 0 {
			t.Fatal("untrusted download accepted")
		}
	}
}

func TestPublishedFlowSend(t *testing.T) {
	for _, status := range []string{"PUBLISHED", "DRAFT"} {
		sent := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				if strings.HasSuffix(r.URL.Path, "/flows") {
					io.WriteString(w, `{"data":[{"id":"333"}]}`)
				} else {
					io.WriteString(w, `{"status":"`+status+`"}`)
				}
				return
			}
			sent++
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["to"] != "919000000055" || payload["type"] != "interactive" {
				t.Error("wrong form destination/type")
			}
			interactive := payload["interactive"].(map[string]any)
			parameters := interactive["action"].(map[string]any)["parameters"].(map[string]any)
			if parameters["flow_id"] != "333" || parameters["flow_message_version"] != "3" {
				t.Error("wrong Flow parameters")
			}
			io.WriteString(w, `{"messages":[{"id":"wamid.fixture"}]}`)
		}))
		s, _ := New([]Account{testAccount()})
		s.base = server.URL
		_, err := s.Execute(context.Background(), testAccount(), Input{Operation: "messages.flow", ID: "333", To: "919000000055", Body: "Feedback", FlowCTA: "Open form", FlowScreen: "START", FlowToken: "test-token"})
		server.Close()
		if status == "PUBLISHED" {
			if err != nil || sent != 1 {
				t.Fatalf("send failed: %v", err)
			}
		} else if err == nil || sent != 0 {
			t.Fatal("draft Flow sent")
		}
	}
}
