package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
)

func TestConnectionDiagnosticsSurviveStalledProviders(t *testing.T) {
	if accountConnectionTimeout >= httpWriteTimeout {
		t.Fatal("provider budget must leave time to write the response")
	}
	for _, tc := range []struct {
		name      string
		smtpDelay time.Duration
		imap      bool
	}{
		{"stalled SMTP greeting", 0, false},
		{"stalled IMAP TLS", 0, true},
		{"slow SMTP then stalled IMAP", 12 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			imapReached := make(chan struct{}, 1)
			provider := func(imap bool) string {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { listener.Close() })
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(httpWriteTimeout + time.Second))
					if imap {
						imapReached <- struct{}{}
					}
					if !imap && tc.imap {
						// End SMTP after consuming part of the shared budget.
						select {
						case <-time.After(tc.smtpDelay):
						case <-t.Context().Done():
						}
						return
					}
					// No greeting/TLS response. Wait for the client deadline to close.
					_, _ = io.Copy(io.Discard, conn)
				}()
				return listener.Addr().String()
			}
			smtpAddress := provider(false)
			imapAddress := ""
			cfg := relayemail.Config{AccountID: "local", Host: "smtp.invalid", Port: 587, From: "local@example.invalid", Enabled: true}
			if tc.imap {
				imapAddress = provider(true)
				cfg.IMAPHost = "imap.invalid"
				cfg.IMAPPort = 993
				cfg.IMAPUsername = "local"
				cfg.IMAPPassword = "local-only"
			}
			client, err := relayemail.NewWithTransport(cfg, x509.NewCertPool(), func(ctx context.Context, network, address string) (net.Conn, error) {
				switch address {
				case "smtp.invalid:587":
					return (&net.Dialer{}).DialContext(ctx, network, smtpAddress)
				case "imap.invalid:993":
					return (&net.Dialer{}).DialContext(ctx, network, imapAddress)
				default:
					return nil, errors.New("external dialing denied")
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			registry := &channels.Registry{Email: map[string]*relayemail.Client{"local": client}}
			mux := http.NewServeMux()
			mux.Handle("POST /api/accounts/{account}/{channel}/connection", accountConnection(registry, func(*http.Request) (auth.User, error) { return auth.User{Role: "admin"}, nil }, "http://hub.invalid", "", "", nil))
			server := httptest.NewUnstartedServer(mux)
			server.Config.WriteTimeout = httpWriteTimeout
			server.Config.ReadTimeout = 10 * time.Second
			server.Start()
			defer server.Close()
			req, err := http.NewRequest("POST", server.URL+"/api/accounts/local/email/connection", strings.NewReader(`{"action":"test"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: auth.Cookie, Value: "local-session"})
			req.Header.Set("Origin", "http://hub.invalid")
			req.Header.Set("X-Relay-CSRF", auth.CSRF("local-session"))
			started := time.Now()
			httpClient := server.Client()
			httpClient.Timeout = httpWriteTimeout + time.Second
			response, err := httpClient.Do(req)
			if err != nil {
				t.Fatalf("diagnostics lost on HTTP connection: %v", err)
			}
			defer response.Body.Close()
			var result channels.EmailConnection
			if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			wantIMAP := "not_configured"
			if tc.imap {
				wantIMAP = "failed"
				select {
				case <-imapReached:
				default:
					t.Fatal("IMAP provider was never checked")
				}
			}
			if response.StatusCode != 200 || result.SMTP != "failed" || result.IMAP != wantIMAP {
				t.Fatalf("missing diagnostic result: %d %+v", response.StatusCode, result)
			}
			if elapsed := time.Since(started); elapsed >= httpWriteTimeout || elapsed < accountConnectionTimeout-time.Second {
				t.Fatalf("unexpected combined check duration: %s", elapsed)
			}
		})
	}
}
