package live

import (
	"context"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/gorilla/websocket"
)

func TestSocketAuthenticationInvalidationAndRevocation(t *testing.T) {
	var revoked atomic.Bool
	session := func(r *http.Request) (auth.User, error) {
		c, err := r.Cookie("session")
		if err != nil || revoked.Load() {
			return auth.User{}, errors.New("expired")
		}
		return auth.User{Role: c.Value}, nil
	}
	s := New("http://example.invalid", session)
	defer s.Close()
	for _, tc := range []struct {
		role, origin string
		status       int
	}{{"", "http://example.invalid", 401}, {"member", "http://example.invalid", 403}, {"admin", "http://foreign.invalid", 403}} {
		r := httptest.NewRequest("GET", "http://example.invalid/socket.io/?EIO=4&transport=websocket", nil)
		r.Header.Set("Origin", tc.origin)
		if tc.role != "" {
			r.AddCookie(&http.Cookie{Name: "session", Value: tc.role})
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal("wrong status", w.Code)
		}
	}
	server := httptest.NewServer(s)
	defer server.Close()
	s.origin = server.URL
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/socket.io/?EIO=4&transport=websocket", http.Header{"Origin": {server.URL}, "Cookie": {"session=admin"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil || !strings.HasPrefix(string(raw), "0{") {
		t.Fatal("engine handshake", string(raw), err)
	}
	if err = conn.WriteMessage(websocket.TextMessage, []byte("40")); err != nil {
		t.Fatal(err)
	}
	_, raw, err = conn.ReadMessage()
	if err != nil || !strings.HasPrefix(string(raw), "40") {
		t.Fatal("socket handshake", string(raw), err)
	}
	// The namespace acknowledgement precedes the connection callback.
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		n := len(s.clients)
		s.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not connected")
		}
		time.Sleep(time.Millisecond)
	}
	s.invalidate(t.Context(), "account-a")
	_, raw, err = conn.ReadMessage()
	if err != nil || string(raw) != `42["hub:invalidate",{"account":"account-a"}]` {
		t.Fatal("invalidation", string(raw), err)
	}
	revoked.Store(true)
	s.invalidate(t.Context(), "account-b")
	_, raw, _ = conn.ReadMessage()
	if strings.Contains(string(raw), "account-b") {
		t.Fatal("revoked client received data")
	}
}

func TestDatabaseNotificationsAreCommitScoped(t *testing.T) {
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database")
	}
	if cfg.Database == "channel_hub" || cfg.Database == "relay_hub_sim" {
		t.Fatal("protected database")
	}
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	defer conn.Close(context.Background())
	schema := fmt.Sprintf("relay_live_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	if _, err = conn.Exec(t.Context(), "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	s := New("http://example.invalid", func(*http.Request) (auth.User, error) { return auth.User{Role: "admin"}, nil })
	defer s.Close()
	server := httptest.NewServer(s)
	defer server.Close()
	s.origin = server.URL
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/socket.io/?EIO=4&transport=websocket", http.Header{"Origin": {server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	ws.ReadMessage()
	ws.WriteMessage(websocket.TextMessage, []byte("40"))
	ws.ReadMessage()
	// Start listening after the socket's connection callback has registered it.
	for {
		s.mu.Lock()
		n := len(s.clients)
		s.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx, cfg) }()
	defer func() { cancel(); <-done }()
	_, raw, err := ws.ReadMessage()
	if err != nil || !strings.Contains(string(raw), "hub:invalidate") {
		t.Fatal("listen startup", err)
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO relay_channel_events(account,channel,direction,provider_id,payload) VALUES('private-a','Email','inbound','local','{}')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, raw, err = ws.ReadMessage()
	if err != nil || !strings.Contains(string(raw), `"account":"private-a"`) {
		t.Fatal("commit notification missing", string(raw), err)
	}
	// A burst of separate commits reaches browsers as few coalesced invalidations.
	for i := range 40 {
		if _, err = conn.Exec(t.Context(), `INSERT INTO relay_channel_events(account,channel,direction,provider_id,payload) VALUES('private-a','Email','inbound',$1,'{}')`, fmt.Sprint("burst-", i)); err != nil {
			t.Fatal(err)
		}
	}
	received := 0
	ws.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	for {
		if _, raw, err = ws.ReadMessage(); err != nil {
			break
		}
		if strings.Contains(string(raw), "hub:invalidate") {
			received++
		}
	}
	if received < 1 || received > 4 {
		t.Fatalf("40 commits produced %d invalidations", received)
	}
}
