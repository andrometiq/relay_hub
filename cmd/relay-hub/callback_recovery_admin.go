package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"github.com/andrometiq/relay_hub/internal/auth"
	"io"
	"net/http"
	"strconv"
)

func callbackRetryAdmin(retry func(context.Context, string, string, int64) error, session func(*http.Request) (auth.User, error), origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := session(r)
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		if u.Role != "admin" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		cookie, err := r.Cookie(auth.Cookie)
		if err != nil || r.Header.Get("Origin") != origin || !hmac.Equal([]byte(r.Header.Get("X-Relay-CSRF")), []byte(auth.CSRF(cookie.Value))) {
			http.Error(w, "invalid request origin", 403)
			return
		}
		var in struct {
			Account string `json:"account"`
			Channel string `json:"channel"`
			ID      string `json:"id"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
		id, err := strconv.ParseInt(in.ID, 10, 64)
		if err != nil || id < 1 || !deliveryAccount.MatchString(in.Account) || (in.Channel != "Telegram" && in.Channel != "Email") {
			http.Error(w, "invalid callback", 400)
			return
		}
		if err = retry(r.Context(), in.Account, in.Channel, id); err != nil {
			http.Error(w, "Callback remains quarantined. Resolve the payload size or delivery failure before retrying.", 409)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
