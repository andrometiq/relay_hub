// Package live publishes account invalidations over authenticated Socket.IO.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/jackc/pgx/v5"
	engine "github.com/zishang520/socket.io/servers/engine/v3"
	"github.com/zishang520/socket.io/servers/engine/v3/transports"
	socket "github.com/zishang520/socket.io/servers/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"
)

type Server struct {
	io      *socket.Server
	handler http.Handler
	session func(*http.Request) (auth.User, error)
	origin  string
	mu      sync.Mutex
	clients map[*socket.Socket]*http.Request
}

func New(origin string, session func(*http.Request) (auth.User, error)) *Server {
	opts := socket.DefaultServerOptions()
	opts.SetPath("/socket.io")
	opts.SetServeClient(false)
	opts.SetTransports(types.NewSet[transports.TransportCtor](engine.WebSocket))
	s := &Server{io: socket.NewServer(nil, opts), session: session, origin: origin, clients: make(map[*socket.Socket]*http.Request)}
	s.io.On("connection", func(args ...any) {
		client := args[0].(*socket.Socket)
		request := client.Request().Request().Clone(context.Background())
		s.mu.Lock()
		s.clients[client] = request
		s.mu.Unlock()
		client.On("disconnect", func(...any) { s.mu.Lock(); delete(s.clients, client); s.mu.Unlock() })
	})
	s.handler = s.io.ServeHandler(nil)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin, _ := url.Parse(s.origin)
	if r.Method != http.MethodGet || r.Host != origin.Host || r.Header.Get("Origin") != s.origin {
		http.Error(w, "invalid origin", 403)
		return
	}
	user, err := s.session(r)
	if err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	// Operational endpoints are administrator-only; members cannot subscribe.
	if user.Role != "admin" {
		http.Error(w, "forbidden", 403)
		return
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Server) Close() { s.io.Close(nil) }

func (s *Server) invalidate(ctx context.Context, account string) {
	s.mu.Lock()
	clients := make(map[*socket.Socket]*http.Request, len(s.clients))
	for client, request := range s.clients {
		clients[client] = request
	}
	s.mu.Unlock()
	for client, request := range clients {
		if ctx.Err() != nil {
			return
		}
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		user, err := s.session(request.Clone(check))
		cancel()
		if err != nil || user.Role != "admin" {
			client.Disconnect(true)
			continue
		}
		client.Emit("hub:invalidate", map[string]string{"account": account})
	}
}

// Run uses commit notifications, never a polling query. Reconnection invalidates
// the view because notifications during an interrupted LISTEN are not durable.
func (s *Server) Run(ctx context.Context, config *pgx.ConnConfig) {
	for ctx.Err() == nil {
		s.listen(ctx, config)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) listen(ctx context.Context, config *pgx.ConnConfig) {
	conn, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return
	}
	defer conn.Close(context.Background())
	var schema string
	if conn.QueryRow(ctx, "SELECT current_schema()").Scan(&schema) != nil {
		return
	}
	if _, err = conn.Exec(ctx, "LISTEN relay_hub_changes"); err != nil {
		return
	}
	s.invalidate(ctx, "")
	// Coalesce bursts (bulk sends change rows hundreds of times per second)
	// into at most one invalidation per account per interval.
	pending := map[string]bool{}
	var flushAt time.Time
	for {
		wait, cancel := ctx, context.CancelFunc(func() {})
		if len(pending) > 0 {
			wait, cancel = context.WithDeadline(ctx, flushAt)
		}
		notification, err := conn.WaitForNotification(wait)
		flush := err != nil && ctx.Err() == nil && errors.Is(wait.Err(), context.DeadlineExceeded)
		cancel()
		if flush {
			for account := range pending {
				s.invalidate(ctx, account)
			}
			clear(pending)
			continue
		}
		if err != nil {
			return
		}
		var change struct {
			Schema  string `json:"schema"`
			Account string `json:"account"`
		}
		if json.Unmarshal([]byte(notification.Payload), &change) == nil && change.Schema == schema {
			if len(pending) == 0 {
				flushAt = time.Now().Add(invalidationInterval)
			}
			pending[change.Account] = true
		}
	}
}

const invalidationInterval = 500 * time.Millisecond
