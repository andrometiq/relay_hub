package telegram

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type simulationTransport func(*http.Request) (*http.Response, error)

func (f simulationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSimulatedSendResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"accepted", `{"ok":true,"result":{"message_id":99}}`, 200, true},
		{"application rejected", `{"ok":false,"description":"rejected"}`, 200, false},
		{"invalid json", `bad`, 200, false},
		{"missing receipt", `{"ok":true}`, 200, false},
		{"throttled", `{"ok":false,"parameters":{"retry_after":3}}`, 429, false},
		{"oversized", strings.Repeat("x", 1048577), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Chat  int64  `json:"chat_id"`
					Text  string `json:"text"`
					Reply struct {
						ID int64 `json:"message_id"`
					} `json:"reply_parameters"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload.Chat != 42 || payload.Text != "வணக்கம்" || payload.Reply.ID != 7 {
					t.Error("wrong send payload")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, _ := New(Config{AccountID: "simulation", BotToken: "123456:abcdefghijklmnopqrstuvwxyzABCDE", Enabled: true})
			client.http.Transport = simulationTransport(func(r *http.Request) (*http.Response, error) {
				clone := r.Clone(r.Context())
				u := *r.URL
				clone.URL = &u
				clone.URL.Scheme = "http"
				clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return http.DefaultTransport.RoundTrip(clone)
			})
			err := client.SendMessage(t.Context(), 42, "வணக்கம்", 7)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}
func TestTransportErrorDoesNotExposeToken(t *testing.T) {
	client, _ := New(Config{AccountID: "simulation", BotToken: "123456:abcdefghijklmnopqrstuvwxyzABCDE", Enabled: true})
	client.http.Transport = simulationTransport(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })
	err := client.SendMessage(t.Context(), 42, "hello", 0)
	if err == nil || strings.Contains(err.Error(), client.cfg.BotToken) {
		t.Fatal("unsafe transport error")
	}
}
