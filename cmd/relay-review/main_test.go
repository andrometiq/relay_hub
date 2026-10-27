package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"
)

func TestReviewProxyAllowsSlowHubDiagnostic(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(16 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"connected":true}`)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	gateway := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
	gateway.Config.WriteTimeout = reviewWriteTimeout
	gateway.Start()
	defer gateway.Close()
	client := http.Client{Timeout: 25 * time.Second}
	resp, err := client.Get(gateway.URL + "/hub/api/accounts/local/email/connection")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || string(raw) != `{"connected":true}` {
		t.Fatal("diagnostic truncated", resp.StatusCode, string(raw), err)
	}
}
