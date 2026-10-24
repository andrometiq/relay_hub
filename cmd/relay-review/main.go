package main

import (
	"context"
	"github.com/andrometiq/relay_hub/internal/envconfig"
	"github.com/andrometiq/relay_hub/internal/review"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const reviewWriteTimeout = 35 * time.Second

func main() {
	if e := envconfig.Load(".env"); e != nil {
		log.Fatal(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, e := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if e != nil {
		log.Fatal("invalid database configuration")
	}
	cfg.MaxConns = 3
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		log.Fatal("database unavailable")
	}
	defer db.Close()
	h, e := review.New(review.Config{Origin: os.Getenv("RELAY_REVIEW_ORIGIN"), Username: os.Getenv("RELAY_REVIEW_USER"), Salt: os.Getenv("RELAY_REVIEW_SALT"), PasswordHash: os.Getenv("RELAY_REVIEW_PASSWORD_HASH"), HubDir: "frontend/dist", CoreDir: "../frappe_relay_core/frontend/dist", CoreUpstream: os.Getenv("RELAY_REVIEW_CORE_UPSTREAM"), CoreSite: os.Getenv("RELAY_REVIEW_CORE_SITE"), CoreSocket: os.Getenv("RELAY_REVIEW_CORE_SOCKET")}, db)
	if e != nil {
		log.Fatal(e)
	}
	srv := &http.Server{Addr: "127.0.0.1:18084", Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: reviewWriteTimeout, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	log.Print("Relay authenticated UI review listening on 127.0.0.1:18084")
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
}
