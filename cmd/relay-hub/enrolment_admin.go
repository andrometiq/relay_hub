package main

import (
	"context"
	"net/http"
	"strconv"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/onboarding"
)

// enrolmentBackend is what the admin API needs from self-service onboarding.
type enrolmentBackend interface {
	ListTokens(context.Context) ([]onboarding.EnrolmentToken, error)
	CreateToken(context.Context, auth.User, onboarding.TokenInput) (onboarding.EnrolmentToken, string, error)
	RevokeToken(context.Context, auth.User, int64) error
	SiteSignups(context.Context, string) ([]onboarding.Session, error)
	SiteEnrolment(context.Context, string) (map[string]any, error)
}

// enrolmentAdmin serves enrolment tokens and site signups to Hub admins.
type enrolmentAdmin struct {
	backend enrolmentBackend
	session func(*http.Request) (auth.User, error)
}

func (a enrolmentAdmin) register(mux *http.ServeMux, protect func(http.Handler) http.Handler, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.Handle("GET /api/enrolment-tokens", protect(admin(a.list)))
	mux.Handle("POST /api/enrolment-tokens", protect(admin(a.create)))
	mux.Handle("POST /api/enrolment-tokens/{id}/revoke", protect(admin(a.revoke)))
	mux.Handle("GET /api/sites/{site}/signups", protect(admin(a.signups)))
}

func (a enrolmentAdmin) list(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.backend.ListTokens(r.Context())
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"tokens": tokens, "csrf": csrf(r)})
}

// create returns the secret once. A setup code comes with the text to send.
func (a enrolmentAdmin) create(w http.ResponseWriter, r *http.Request) {
	var in onboarding.TokenInput
	if !decodeBody(w, r, &in) {
		return
	}
	user, err := a.session(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token, secret, err := a.backend.CreateToken(r.Context(), user, in)
	if err != nil {
		storeFailure(w, err)
		return
	}
	result := map[string]any{"token": token, "secret": secret, "csrf": csrf(r)}
	if token.Kind == "setup_code" {
		result["message"] = "Open Relay on your site, choose Get started and enter this setup code: " + secret
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, result)
}

func (a enrolmentAdmin) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "invalid token", http.StatusBadRequest)
		return
	}
	user, err := a.session(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err = a.backend.RevokeToken(r.Context(), user, id); err != nil {
		storeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a enrolmentAdmin) signups(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	enrolment, err := a.backend.SiteEnrolment(r.Context(), site)
	if err != nil {
		storeFailure(w, err)
		return
	}
	signups, err := a.backend.SiteSignups(r.Context(), site)
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"enrolment": enrolment, "signups": signups, "csrf": csrf(r)})
}
