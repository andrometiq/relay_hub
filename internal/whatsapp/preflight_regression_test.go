package whatsapp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialPhoneMembershipAndPagination(t *testing.T) {
	for _, owned := range []bool{true, false} {
		a := testAccount()
		a.AppSecret = "secret"
		a.VerifyToken = "verify"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v25.0/111":
				io.WriteString(w, `{"id":"111"}`)
			case "/v25.0/222":
				io.WriteString(w, `{"id":"222"}`)
			case "/v25.0/222/phone_numbers":
				if r.URL.Query().Get("after") == "" {
					io.WriteString(w, `{"data":[{"id":"444"}],"paging":{"cursors":{"after":"second"},"next":"https://graph.facebook.com/222/phone_numbers?after=second"}}`)
				} else if owned {
					io.WriteString(w, `{"data":[{"id":"111"}]}`)
				} else {
					io.WriteString(w, `{"data":[{"id":"555"}]}`)
				}
			default:
				t.Error("unexpected path", r.URL.Path)
			}
		}))
		s, _ := New([]Account{a})
		s.base = server.URL
		err := s.ValidateCredentials(t.Context(), a)
		server.Close()
		if (err == nil) != owned {
			t.Fatal("phone/WABA membership not enforced", err)
		}
	}
}

func TestFlowPreflightRetryDoesNotRetryAmbiguousSubmission(t *testing.T) {
	for _, kind := range []string{"timeout", "503", "408", "draft", "post"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := NewWithHTTPClient([]Account{testAccount()}, &http.Client{Transport: mediaTestTransport(func(r *http.Request) (*http.Response, error) {
				if kind == "timeout" || kind == "post" && r.Method == "POST" {
					return nil, errors.New("transport timeout")
				}
				status, body := 200, `{"data":[{"id":"333"}]}`
				if kind == "503" {
					status = 503
					body = `{}`
				} else if kind == "408" {
					status = 408
					body = `{}`
				} else if !strings.HasSuffix(r.URL.Path, "/flows") {
					body = `{"status":"PUBLISHED"}`
					if kind == "draft" {
						body = `{"status":"DRAFT"}`
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})})
			_, err := s.Execute(t.Context(), testAccount(), Input{Operation: "messages.flow", ID: "333", To: "919000000055", Body: "Feedback", FlowCTA: "Open form", FlowScreen: "START", FlowToken: "test-token"})
			state, _, _, _ := sendOutcome(err, 0)
			want := "queued"
			if kind == "draft" {
				want = "rejected"
			}
			if kind == "post" {
				want = "uncertain"
			}
			if state != want {
				t.Fatal("wrong outcome", state, err)
			}
		})
	}
}
