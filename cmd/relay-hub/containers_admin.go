package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// directoryAdmin serves the admin API for containers, channels and sites.
// Every change reloads the runtime, so webhooks, sends and callbacks use it at once.
type directoryAdmin struct {
	store   *containers.Store
	session func(*http.Request) (auth.User, error)
	origin  string
	// verifyWhatsApp checks a number with Meta before it is stored.
	verifyWhatsApp  func(context.Context, whatsapp.Account) error
	whatsappAccount func(key string) (whatsapp.Account, bool)
	reload          func(context.Context) error
}

func (a directoryAdmin) register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	routes := map[string]http.HandlerFunc{
		"GET /api/containers":                                                     a.listContainers,
		"POST /api/containers":                                                    a.createContainer,
		"GET /api/containers/{container}":                                         a.getContainer,
		"PUT /api/containers/{container}":                                         a.updateContainer,
		"GET /api/containers/{container}/whatsapp/webhook":                        a.containerWebhook,
		"POST /api/containers/{container}/channels":                               a.createChannel,
		"PUT /api/containers/{container}/channels/{channel}/{key}":                a.updateChannel,
		"POST /api/channels/whatsapp/{key}/connection":                            a.whatsappConnection,
		"DELETE /api/containers/{container}/old-address":                          a.turnOffOldAddress,
		"DELETE /api/containers/{container}/channels/{channel}/{key}/old-address": a.turnOffOldAddress,
		"GET /api/sites":                 a.listSites,
		"POST /api/sites":                a.createSite,
		"GET /api/sites/{site}":          a.getSite,
		"PUT /api/sites/{site}":          a.updateSite,
		"PUT /api/sites/{site}/channels": a.assignChannels,
	}
	for pattern, handler := range routes {
		mux.Handle(pattern, protect(a.admin(handler)))
	}
}

// admin allows only Hub admins, and for changes also checks CSRF and origin.
func (a directoryAdmin) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := a.session(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if user.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			cookie, err := r.Cookie(auth.Cookie)
			if err != nil || !hmac.Equal([]byte(r.Header.Get("X-Relay-CSRF")), []byte(auth.CSRF(cookie.Value))) || r.Header.Get("Origin") != a.origin {
				http.Error(w, "invalid request origin", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

// decodeBody reads one bounded JSON object. Unknown fields, such as read-only
// fields the UI echoes back, are ignored.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}

// storeFailure maps store errors: missing 404, owned elsewhere 409, database 503, others 400.
func storeFailure(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, containers.ErrConflict), errors.Is(err, containers.ErrPendingSends):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &pgErr), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// csrf returns the session's CSRF token; the admin UI reads it from any JSON response.
func csrf(r *http.Request) string {
	cookie, err := r.Cookie(auth.Cookie)
	if err != nil {
		return ""
	}
	return auth.CSRF(cookie.Value)
}

func (a directoryAdmin) changed(w http.ResponseWriter, r *http.Request) {
	if err := a.reload(r.Context()); err != nil {
		http.Error(w, "saved, but the runtime reload was rejected: "+err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a directoryAdmin) listContainers(w http.ResponseWriter, r *http.Request) {
	rows, next, err := a.store.ListContainers(r.Context(), r.URL.Query().Get("query"), r.URL.Query().Get("after"), 30)
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"containers": rows, "next": next, "csrf": csrf(r)})
}

func (a directoryAdmin) getContainer(w http.ResponseWriter, r *http.Request) {
	container, err := a.store.GetContainer(r.Context(), r.PathValue("container"))
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, struct {
		containers.Container
		CSRF string `json:"csrf"`
	}{container, csrf(r)})
}

type containerInput struct {
	containers.Container
	WhatsApp *containers.ProviderInput `json:"whatsapp"`
}

func (a directoryAdmin) createContainer(w http.ResponseWriter, r *http.Request) {
	var in containerInput
	if !decodeBody(w, r, &in) {
		return
	}
	a.saveContainer(w, r, in, true)
}

func (a directoryAdmin) updateContainer(w http.ResponseWriter, r *http.Request) {
	var in containerInput
	if !decodeBody(w, r, &in) {
		return
	}
	in.ID = r.PathValue("container")
	a.saveContainer(w, r, in, false)
}

func (a directoryAdmin) saveContainer(w http.ResponseWriter, r *http.Request, in containerInput, create bool) {
	if !create {
		if _, err := a.store.GetContainer(r.Context(), in.ID); err != nil {
			storeFailure(w, err)
			return
		}
	}
	if create && in.ID == "" {
		in.ID = containers.NewContainerID()
	}
	if err := a.store.SaveContainer(r.Context(), in.Container, in.WhatsApp, create); err != nil {
		storeFailure(w, err)
		return
	}
	if !create {
		a.changed(w, r)
		return
	}
	if err := a.reload(r.Context()); err != nil {
		http.Error(w, "saved, but the runtime reload was rejected: "+err.Error(), http.StatusConflict)
		return
	}
	saved, err := a.store.GetContainer(r.Context(), in.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": saved.ID, "webhook_path": saved.WebhookPath, "csrf": csrf(r)})
}

// turnOffOldAddress makes a container, bot or mailbox answer only on its random path.
func (a directoryAdmin) turnOffOldAddress(w http.ResponseWriter, r *http.Request) {
	var channel *containers.ChannelRef
	if r.PathValue("key") != "" {
		channel = &containers.ChannelRef{Channel: r.PathValue("channel"), Key: r.PathValue("key")}
	}
	if err := a.store.TurnOffOldAddress(r.Context(), r.PathValue("container"), channel); err != nil {
		storeFailure(w, err)
		return
	}
	a.changed(w, r)
}

// containerWebhook gives the admin the callback path and verify token to paste in Meta.
func (a directoryAdmin) containerWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("container")
	container, err := a.store.GetContainer(r.Context(), id)
	if err != nil {
		storeFailure(w, err)
		return
	}
	config, _, err := a.store.ProviderCredential(r.Context(), id)
	if err != nil {
		storeFailure(w, err)
		return
	}
	token, _ := config["verify_token"].(string)
	writeJSON(w, map[string]any{"path": container.WebhookPath, "verify_token": token, "csrf": csrf(r)})
}

type channelInput struct {
	containers.Channel
	Secret string `json:"secret"`
	// UseContainerApp removes a WhatsApp number's own app secret and verify token.
	UseContainerApp bool `json:"use_container_app"`
}

func (a directoryAdmin) createChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !decodeBody(w, r, &in) {
		return
	}
	a.saveChannel(w, r, in, true)
}

func (a directoryAdmin) updateChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !decodeBody(w, r, &in) {
		return
	}
	in.Channel.Channel, in.Key = r.PathValue("channel"), r.PathValue("key")
	a.saveChannel(w, r, in, false)
}

func (a directoryAdmin) saveChannel(w http.ResponseWriter, r *http.Request, in channelInput, create bool) {
	in.Container = r.PathValue("container")
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	if in.UseContainerApp && in.Channel.Channel == "WhatsApp" {
		in.ClearSecrets = []string{"app_secret", "verify_token"}
	}
	if _, err := a.store.GetContainer(r.Context(), in.Container); err != nil {
		storeFailure(w, err)
		return
	}
	if !create {
		if _, err := a.store.GetChannel(r.Context(), containers.ChannelRef{Channel: in.Channel.Channel, Key: in.Key}); err != nil {
			storeFailure(w, err)
			return
		}
	}
	if err := a.checkChannel(r.Context(), &in.Channel, in.Secret); err != nil {
		storeFailure(w, err)
		return
	}
	if err := a.store.SaveChannel(r.Context(), in.Channel, in.Secret, create); err != nil {
		storeFailure(w, err)
		return
	}
	a.changed(w, r)
}

// whatsappConnection checks a stored, active number with Meta.
func (a directoryAdmin) whatsappConnection(w http.ResponseWriter, r *http.Request) {
	account, ok := a.whatsappAccount(r.PathValue("key"))
	if !ok {
		http.Error(w, "WhatsApp number is not active", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accountConnectionTimeout)
	defer cancel()
	if err := a.verifyWhatsApp(ctx, account); err != nil {
		http.Error(w, "WhatsApp credentials could not be verified with Meta", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "connected"})
}

// candidate joins the edit with the stored channel or, for a new channel, the container app.
func (a directoryAdmin) candidate(ctx context.Context, ch containers.Channel, secret string) (containers.RuntimeCredential, error) {
	stored, err := a.store.Credential(ctx, ch.Channel, ch.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		stored = containers.RuntimeCredential{Key: ch.Key, Channel: ch.Channel, Container: ch.Container}
		if ch.Channel == "WhatsApp" {
			stored.Config, stored.Secret, err = a.store.ProviderCredential(ctx, ch.Container)
		} else {
			stored.Config, err = map[string]any{}, nil
		}
	}
	if err != nil {
		return stored, err
	}
	if len(ch.ClearSecrets) > 0 {
		provider, _, err := a.store.ProviderCredential(ctx, ch.Container)
		if err != nil {
			return stored, err
		}
		for _, key := range ch.ClearSecrets {
			delete(ch.Config, key)
			stored.Config[key] = provider[key]
		}
	}
	for key, value := range ch.Config {
		if text, ok := value.(string); !ok || text != "" {
			stored.Config[key] = value
		}
	}
	if secret != "" {
		stored.Secret = secret
	}
	return stored, nil
}

// checkChannel validates an enabled channel before save. A WhatsApp number is
// checked with Meta and gets its connection status.
func (a directoryAdmin) checkChannel(ctx context.Context, ch *containers.Channel, secret string) error {
	for _, key := range []string{"connection_status", "verified_at", "webhook_status", "checked_at"} {
		delete(ch.Config, key)
	}
	if !ch.Enabled {
		if ch.Channel == "WhatsApp" {
			ch.Config["connection_status"] = "disabled"
		}
		return nil
	}
	candidate, err := a.candidate(ctx, *ch, secret)
	if err != nil {
		return err
	}
	switch ch.Channel {
	case "WhatsApp":
		if err := validateWhatsAppConfig(candidate.Config); err != nil {
			return err
		}
		account, err := whatsappAccount(candidate)
		if err != nil {
			return err
		}
		if err := a.verifyWhatsApp(ctx, account); err != nil {
			return errors.New("WhatsApp credentials could not be verified with Meta")
		}
		ch.Config["connection_status"] = "connected"
		ch.Config["verified_at"] = time.Now().UTC().Format(time.RFC3339)
	case "Email":
		if err := validateEmailSettings(candidate.Config, candidate.Secret); err != nil {
			return err
		}
		fallthrough
	case "Telegram":
		if _, err := channels.Load([]containers.RuntimeCredential{candidate}); err != nil {
			return errors.New(ch.Channel + " settings are invalid; check required fields and credentials")
		}
	}
	return nil
}

func (a directoryAdmin) listSites(w http.ResponseWriter, r *http.Request) {
	rows, next, err := a.store.ListSites(r.Context(), r.URL.Query().Get("query"), r.URL.Query().Get("after"), 30)
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"sites": rows, "next": next, "csrf": csrf(r)})
}

func (a directoryAdmin) getSite(w http.ResponseWriter, r *http.Request) {
	site, err := a.store.GetSite(r.Context(), r.PathValue("site"))
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, struct {
		containers.Site
		CSRF string `json:"csrf"`
	}{site, csrf(r)})
}

type siteInput struct {
	containers.Site
	Secrets map[string]any `json:"secrets"`
}

func (a directoryAdmin) createSite(w http.ResponseWriter, r *http.Request) {
	var in siteInput
	if !decodeBody(w, r, &in) {
		return
	}
	a.saveSite(w, r, in, true)
}

func (a directoryAdmin) updateSite(w http.ResponseWriter, r *http.Request) {
	var in siteInput
	if !decodeBody(w, r, &in) {
		return
	}
	in.ID = r.PathValue("site")
	if _, err := a.store.GetSite(r.Context(), in.ID); err != nil {
		storeFailure(w, err)
		return
	}
	a.saveSite(w, r, in, false)
}

func (a directoryAdmin) saveSite(w http.ResponseWriter, r *http.Request, in siteInput, create bool) {
	if err := a.store.SaveSite(r.Context(), in.Site, in.Secrets, create); err != nil {
		storeFailure(w, err)
		return
	}
	a.changed(w, r)
}

func (a directoryAdmin) assignChannels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channels []containers.ChannelRef `json:"channels"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := a.store.AssignChannels(r.Context(), r.PathValue("site"), in.Channels); err != nil {
		storeFailure(w, err)
		return
	}
	a.changed(w, r)
}
