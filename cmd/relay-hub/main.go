package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/andrometiq/relay_hub/internal/envconfig"
	"github.com/andrometiq/relay_hub/internal/hubmcp"
	"github.com/jackc/pgx/v5"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func connect(ctx context.Context) (*pgx.Conn, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, errors.New("database connection failed; check configuration and availability")
	}
	return conn, nil
}
func check(ctx context.Context, ready bool) error {
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if ready {
		return database.Ready(ctx, conn)
	}
	return database.CheckVersion(ctx, conn)
}
func run() error {
	if err := envconfig.Load(".env"); err != nil {
		return err
	}
	if len(os.Args) != 2 {
		return errors.New("usage: relay-hub doctor|migrate|serve")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch os.Args[1] {
	case "doctor":
		if err := check(ctx, false); err != nil {
			return err
		}
		fmt.Println("PostgreSQL 18 connection: OK")
		return nil
	case "migrate":
		conn, err := connect(ctx)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		if err = database.Migrate(ctx, conn); err != nil {
			return err
		}
		fmt.Println("Database schema is up to date")
		return nil
	case "serve":
		if err := check(ctx, true); err != nil {
			return err
		}
		ui := os.Getenv("RELAY_UI_DIR")
		if ui == "" {
			ui = "frontend/dist"
		}
		if _, err := os.Stat(filepath.Join(ui, "index.html")); err != nil {
			return errors.New("UI build missing; run build-ui")
		}
		addr := os.Getenv("RELAY_ADDR")
		if addr == "" {
			addr = "127.0.0.1:18081"
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("foundation server binds only to a loopback IP until authentication is implemented")
		}
		handler, err := hubmcp.New(os.Getenv("RELAY_MCP_TOKEN"), func(c context.Context) error { return check(c, true) })
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.Handle("POST /mcp", handler)
		mux.Handle("GET /mcp", handler)
		mux.Handle("DELETE /mcp", handler)
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "relay_hub"})
		})
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
			c, stop := context.WithTimeout(r.Context(), 3*time.Second)
			defer stop()
			w.Header().Set("Content-Type", "application/json")
			if err := check(c, true); err != nil {
				w.WriteHeader(503)
				json.NewEncoder(w).Encode(map[string]string{"status": "not_ready"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ready", "database": "PostgreSQL 18", "instance": os.Getenv("RELAY_INSTANCE")})
		})
		mux.Handle("GET /", http.FileServer(http.Dir(ui)))
		server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
		shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-shutdown.Done()
			c, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			server.Shutdown(c)
		}()
		log.Printf("Relay Hub listening on %s", addr)
		err = server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	default:
		return errors.New("unknown command")
	}
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
