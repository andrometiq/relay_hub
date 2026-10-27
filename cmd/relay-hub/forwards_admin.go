package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/andrometiq/relay_hub/internal/forward"
)

var forwardProviders = map[string]string{"whatsapp": "WhatsApp", "telegram": "Telegram", "email": "Email"}

// forwardsAdmin manages the URLs that get a copy of a container's, bot's or
// mailbox's verified webhooks, and lists the results.
type forwardsAdmin struct {
	store forward.Store
}

func (f forwardsAdmin) register(mux *http.ServeMux, protect func(http.Handler) http.Handler, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.Handle("GET /api/forwards/{provider}/{key}", protect(admin(f.list)))
	mux.Handle("POST /api/forwards/{provider}/{key}", protect(admin(f.add)))
	mux.Handle("DELETE /api/forwards/{provider}/{key}/{id}", protect(admin(f.remove)))
}

func forwardScope(r *http.Request) forward.Scope {
	return forward.Scope{Provider: forwardProviders[r.PathValue("provider")], Key: r.PathValue("key")}
}

func forwardFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, forward.ErrInvalidScope):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, forward.ErrInvalidURL), errors.Is(err, forward.ErrTooMany):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		storeFailure(w, err)
	}
}

func (f forwardsAdmin) list(w http.ResponseWriter, r *http.Request) {
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		before = value
	}
	scope := forwardScope(r)
	targets, err := f.store.Targets(r.Context(), scope)
	if err != nil {
		forwardFailure(w, err)
		return
	}
	records, counts, err := f.store.Records(r.Context(), scope, before, 50)
	if err != nil {
		forwardFailure(w, err)
		return
	}
	next := ""
	if len(records) == 50 {
		next = strconv.FormatInt(records[len(records)-1].ID, 10)
	}
	writeJSON(w, map[string]any{"targets": targets, "records": records, "counts": counts, "next": next, "csrf": csrf(r)})
}

func (f forwardsAdmin) add(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	target, err := f.store.AddTarget(r.Context(), forwardScope(r), in.URL)
	if err != nil {
		forwardFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, target)
}

func (f forwardsAdmin) remove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid target", http.StatusBadRequest)
		return
	}
	if err = f.store.RemoveTarget(r.Context(), forwardScope(r), id); err != nil {
		forwardFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
