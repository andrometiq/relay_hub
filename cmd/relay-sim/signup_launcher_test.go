package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// enrolToken is a fixed test enrolment token.
var enrolToken = "relay_enrol_" + strings.Repeat("ab", 32)

// controlStatus posts to a control path and returns only the status.
func (l *launcher) controlStatus(path string, body any) int {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(l.ctx, "POST", "http://"+l.metaAddr+"/_sim/"+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+l.controlToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestSignupContainerControl(t *testing.T) {
	dsn := isolatedLauncherDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	core := httptest.NewServer(http.NotFoundHandler())
	defer core.Close()
	l := newLauncher(t, ctx, dsn, core.URL)
	l.start(false)
	if status := l.controlStatus("signup-container", map[string]any{"token": "short", "origin_pattern": "http://*.localhost:8001"}); status != 400 {
		t.Fatal("weak token accepted", status)
	}
	if status := l.controlStatus("signup-container", map[string]any{"token": enrolToken}); status != 400 {
		t.Fatal("missing origin pattern accepted", status)
	}
	if got := l.call("signup-container", map[string]any{"token": enrolToken, "origin_pattern": "http://*.localhost:8001"}); got["signup"] != true {
		t.Fatal(got)
	}
	code := "relay_enrol_" + strings.Repeat("cd", 32)
	l.call("signup-container", map[string]any{"token": code, "origin_pattern": "http://*.localhost:8001", "kind": "setup_code", "max_sites": 1, "expires_in": -60})
	l.call("signup-container", map[string]any{"token": enrolToken, "origin_pattern": "http://shop.localhost:8001"})
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var signup bool
	var config string
	if err = conn.QueryRow(ctx, "SELECT signup FROM relay_containers WHERE container_id='sim-app'").Scan(&signup); err != nil || !signup {
		t.Fatal("sim-app is not the signup container", err)
	}
	if err = conn.QueryRow(ctx, "SELECT count(*)::text FROM relay_enrolment_tokens WHERE token_hash NOT LIKE 'relay_enrol_%'").Scan(&config); err != nil || config != "2" {
		t.Fatal("tokens must be stored hashed, once each", config, err)
	}
	var pattern string
	var expired bool
	if err = conn.QueryRow(ctx, "SELECT origin_pattern, expires_at < now() FROM relay_enrolment_tokens WHERE kind='bench'").Scan(&pattern, &expired); err != nil || pattern != "http://shop.localhost:8001" || expired {
		t.Fatal("bench token update", pattern, expired, err)
	}
	if err = conn.QueryRow(ctx, "SELECT expires_at < now() FROM relay_enrolment_tokens WHERE kind='setup_code' AND max_sites=1").Scan(&expired); err != nil || !expired {
		t.Fatal("expired setup code", err)
	}
	state := l.call("es/state", nil)
	if len(state["phones"].([]any)) != 0 {
		t.Fatal("fresh signup state expected")
	}
}
