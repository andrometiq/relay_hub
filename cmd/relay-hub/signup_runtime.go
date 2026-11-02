package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/onboarding"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5/pgxpool"
)

// signupRuntime owns the onboarding service and its own small pool, because
// enrolment holds a connection while it waits for the site's proof.
type signupRuntime struct {
	service *onboarding.Service
	pool    *pgxpool.Pool
}

func (s signupRuntime) close() { s.pool.Close() }

func newSignup(authStore *auth.Store, store *containers.Store, reload func(context.Context) error, metadata *whatsapp.MetadataSync) (signupRuntime, error) {
	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return signupRuntime{}, err
	}
	config.MaxConns, config.MinConns, config.MaxConnIdleTime = 4, 0, time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return signupRuntime{}, err
	}
	cfg := onboarding.Config{DB: pool, Store: store, Auth: authStore, PublicOrigin: os.Getenv("RELAY_PUBLIC_ORIGIN") + os.Getenv("RELAY_PUBLIC_PREFIX"),
		SDKURL: os.Getenv("RELAY_FACEBOOK_SDK_URL"), Reload: reload, Metadata: metadata}
	if dialog := os.Getenv("RELAY_FACEBOOK_DIALOG_ORIGIN"); dialog != "" {
		if err = onboarding.CheckSourceURL(dialog); err != nil {
			pool.Close()
			return signupRuntime{}, errors.New("RELAY_FACEBOOK_DIALOG_ORIGIN must be HTTPS, or HTTP on loopback")
		}
		cfg.DialogOrigin = onboarding.ExactOrigin(dialog)
	}
	service, err := onboarding.New(cfg)
	if err != nil {
		pool.Close()
		return signupRuntime{}, err
	}
	return signupRuntime{service: service, pool: pool}, nil
}
