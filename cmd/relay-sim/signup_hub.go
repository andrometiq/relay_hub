package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/onboarding"
)

var enrolmentTokenPattern = regexp.MustCompile(`^relay_enrol_[a-f0-9]{64}$`)

// enrolmentToken is one Hub enrolment token that e2e tests create through
// /_sim/signup-container. Kind, limit, expiry and revocation are optional
// so the denial cases can be set up without the Hub admin UI.
type enrolmentToken struct {
	Token         string `json:"token"`
	OriginPattern string `json:"origin_pattern"`
	Kind          string `json:"kind"`
	MaxSites      *int   `json:"max_sites"`
	ExpiresIn     *int   `json:"expires_in"`
	Revoked       bool   `json:"revoked"`
}

func (t *enrolmentToken) validate() error {
	if t.Kind == "" {
		t.Kind = "bench"
	}
	if t.ExpiresIn == nil {
		days := int((30 * 24 * time.Hour).Seconds())
		t.ExpiresIn = &days
	}
	switch {
	case !enrolmentTokenPattern.MatchString(t.Token):
		return errors.New("token must be relay_enrol_ and 64 lowercase hex")
	case t.OriginPattern == "" || len(t.OriginPattern) > 300:
		return errors.New("origin_pattern required")
	case t.Kind != "bench" && t.Kind != "setup_code":
		return errors.New("kind must be bench or setup_code")
	case t.MaxSites != nil && *t.MaxSites < 1:
		return errors.New("max_sites must be positive")
	}
	return nil
}

// signupContainer stores the sim-app container as the signup container,
// with the fake Embedded Signup configuration, and one enrolment token.
func (s *simulation) signupContainer(w http.ResponseWriter, r *http.Request) {
	var in enrolmentToken
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if err := in.validate(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	store, err := containers.New(s.db, s.credentialKey)
	if err != nil {
		http.Error(w, "container store unavailable", 503)
		return
	}
	if err = s.saveSignupContainer(r.Context(), store, in); err != nil {
		http.Error(w, "signup container failed", 503)
		return
	}
	if err = s.activateSites(r.Context(), store); err != nil {
		http.Error(w, "activation failed", 503)
		return
	}
	writeJSON(w, map[string]any{"container": "sim-app", "signup": true, "es_config_id": esConfigID})
}

func (s *simulation) saveSignupContainer(ctx context.Context, store *containers.Store, in enrolmentToken) error {
	app := &containers.ProviderInput{AccessToken: "sim-provider-token", Config: map[string]any{"app_id": esAppID, "graph_version": "v25.0",
		"app_secret": esAppSecret, "verify_token": "sim-verify-token", "es_config_id": esConfigID}}
	if err := store.SaveContainer(ctx, containers.Container{ID: "sim-app", Name: "Simulation app", Enabled: true, Signup: true}, app, false); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO relay_enrolment_tokens(name,token_hash,kind,origin_pattern,max_sites,expires_at,revoked_at)
		VALUES ('simulation',$1,$2,$3,$4,now()+make_interval(secs=>$5),CASE WHEN $6 THEN now() END)
		ON CONFLICT (token_hash) DO UPDATE SET kind=EXCLUDED.kind, origin_pattern=EXCLUDED.origin_pattern, max_sites=EXCLUDED.max_sites,
		expires_at=EXCLUDED.expires_at, revoked_at=EXCLUDED.revoked_at`,
		auth.Hash(in.Token), in.Kind, in.OriginPattern, in.MaxSites, float64(*in.ExpiresIn), in.Revoked)
	return err
}

// onboarding builds the Hub onboarding service against the fake Meta
// address: the SDK and dialog come from there, and Graph calls go through
// the simulation transport.
func (s *simulation) onboarding(authStore *auth.Store, client *http.Client, meta *url.URL) (*onboarding.Service, error) {
	store, err := containers.New(s.db, s.credentialKey)
	if err != nil {
		return nil, err
	}
	return onboarding.New(onboarding.Config{DB: s.db, Store: store, Auth: authStore, Client: client, PublicOrigin: s.hub.String(),
		SDKURL: meta.String() + "/sdk.js", DialogOrigin: onboarding.ExactOrigin(meta.String()),
		Reload: func(ctx context.Context) error { return s.activateSites(ctx, store) }})
}
