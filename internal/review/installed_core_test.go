package review

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestInstalledCoreRoutesAndTrustedHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "review.test" || r.Header.Get("X-Frappe-Site-Name") != "isolated.test" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Host") != "review.test" || r.Header.Get("X-Forwarded-Prefix") != "" {
			t.Error("untrusted routing headers")
		}
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/method/login" || string(body) != "synthetic-login-body" {
			t.Error("request changed")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	os.WriteFile(dir+"/index.html", []byte("preview"), 0600)
	cfg := Config{Origin: "https://review.test", Username: "review", Salt: strings.Repeat("ab", 16), PasswordHash: strings.Repeat("ab", 32), HubDir: dir, CoreDir: dir, CoreUpstream: upstream.URL, CoreSite: "isolated.test"}
	server, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "https://review.test/api/method/login", strings.NewReader("synthetic-login-body"))
	req.Header.Set("X-Frappe-Site-Name", "production.site")
	req.Header.Set("X-Forwarded-Host", "evil.test")
	req.Header.Set("X-Forwarded-Prefix", "/evil")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != 401 {
		t.Fatal("upstream authentication must remain authoritative", response.Code)
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", "https://review.test/core/", nil))
	if response.Code != 303 || response.Header().Get("Location") != "/relay" {
		t.Fatal("legacy Core link must reach installed app")
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", "https://evil.test/login", nil))
	if response.Code != 403 {
		t.Fatal("unexpected host accepted")
	}
	for _, bad := range []string{"https://remote.test", "http://127.0.0.1/path", "http://user@127.0.0.1"} {
		cfg.CoreUpstream = bad
		if _, err = New(cfg, nil); err == nil {
			t.Fatal("unsafe upstream accepted", bad)
		}
	}
}

func TestInstalledCoreRealtimeUsesIsolatedSocket(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "relay-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	socketPath := socketDir + "/realtime.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket.io/" || r.Header.Get("X-Frappe-Site-Name") != "isolated.test" || r.Host != "review.test" {
			t.Error("invalid realtime routing")
		}
		w.Write([]byte("isolated realtime"))
	})}
	go upstream.Serve(listener)
	defer upstream.Close()
	dir := t.TempDir()
	os.WriteFile(dir+"/index.html", []byte("preview"), 0600)
	server, err := New(Config{Origin: "https://review.test", Username: "review", Salt: strings.Repeat("ab", 16), PasswordHash: strings.Repeat("ab", 32), HubDir: dir, CoreDir: dir, CoreUpstream: "http://127.0.0.1:1", CoreSite: "isolated.test", CoreSocket: socketPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://review.test/socket.io/?EIO=4&transport=polling", nil)
	r.Header.Set("X-Frappe-Site-Name", "another.site")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "isolated realtime" {
		t.Fatal("realtime did not use isolated socket", w.Code)
	}
}
