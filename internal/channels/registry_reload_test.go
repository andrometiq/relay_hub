package channels

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/andrometiq/relay_hub/internal/telegram"
)

type registrySimulationTransport func(*http.Request) (*http.Response, error)

func (f registrySimulationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func registryTestClient(t *testing.T, label string, calls *atomic.Int32) *telegram.Client {
	t.Helper()
	token := "123456:" + strings.Repeat(label, 24)
	c, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: "a", BotToken: token, Enabled: true}, &http.Client{Transport: registrySimulationTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "api.telegram.org" || !strings.HasPrefix(r.URL.Path, "/bot"+token+"/") {
			t.Error("incorrect account credential")
		}
		result := `{"id":-100123,"type":"supergroup"}`
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			result = `{"message_id":22}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestRegistryReloadUpdatesCapturedHandlers(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	original := &Registry{Telegram: map[string]*telegram.Client{"a": registryTestClient(t, "a", &oldCalls)}}
	// These method values capture the same pointer as MCP handlers at startup.
	send, group, secret := original.SendTelegramToThread, original.TelegramGroup, original.TelegramWebhookSecret
	if err := send(t.Context(), "a", -100123, "before", 22, 77); err != nil {
		t.Fatal(err)
	}
	replacement := &Registry{Telegram: map[string]*telegram.Client{"a": registryTestClient(t, "b", &newCalls)}}
	if err := original.Replace(replacement); err != nil {
		t.Fatal(err)
	}
	if err := send(t.Context(), "a", -100123, "after", 22, 77); err != nil {
		t.Fatal(err)
	}
	if _, err := group(t.Context(), "a", telegram.GroupInput{Operation: "info", ChatID: -100123}); err != nil {
		t.Fatal(err)
	}
	if oldCalls.Load() != 1 || newCalls.Load() != 2 {
		t.Fatalf("stale credentials used after reload: old=%d new=%d", oldCalls.Load(), newCalls.Load())
	}
	if err := original.Replace(&Registry{}); err != nil {
		t.Fatal(err)
	}
	if err := send(t.Context(), "a", -100123, "disabled", 22, 77); err == nil {
		t.Fatal("disabled account sent through captured method")
	}
	if _, err := group(t.Context(), "a", telegram.GroupInput{Operation: "info", ChatID: -100123}); err == nil {
		t.Fatal("disabled account read group")
	}
	if _, err := secret("a", strings.Repeat("master", 8)); err == nil {
		t.Fatal("disabled account retained webhook registration")
	}
	if err := original.TelegramWebhook(t.Context(), Store{}, "a", strings.Repeat("master", 8), []byte("invalid"), []byte(`{}`)); err == nil {
		t.Fatal("disabled account accepted webhook")
	}
	if original.Status() != (Status{}) || oldCalls.Load() != 1 || newCalls.Load() != 2 {
		t.Fatal("disabled snapshot retained clients")
	}
}
func TestRegistrySnapshotsDoNotAliasOrResetAfterReplacement(t *testing.T) {
	var calls atomic.Int32
	next := &Registry{Telegram: map[string]*telegram.Client{"a": registryTestClient(t, "a", &calls)}}
	original := &Registry{}
	// Replacing before first request must not let initialization restore old maps.
	if err := original.Replace(next); err != nil {
		t.Fatal(err)
	}
	delete(next.Telegram, "a") // published snapshots are independent of source maps
	if err := next.Replace(&Registry{}); err != nil {
		t.Fatal(err)
	}
	if original.Status().Telegram != 1 {
		t.Fatal("replacement/source map mutated another registry snapshot")
	}
	if err := original.Replace(nil); err == nil {
		t.Fatal("nil replacement accepted")
	}
	if err := original.SendTelegram(t.Context(), "a", -100123, "retained", 0); err != nil {
		t.Fatal("failed reload discarded active client", err)
	}
}
func TestRegistryConcurrentReloadAndOperations(t *testing.T) {
	var calls atomic.Int32
	versions := []*Registry{
		{Telegram: map[string]*telegram.Client{"a": registryTestClient(t, "a", &calls)}},
		{Telegram: map[string]*telegram.Client{"a": registryTestClient(t, "b", &calls)}},
		{},
	}
	r := &Registry{}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 400; i++ {
			if err := r.Replace(versions[i%len(versions)]); err != nil {
				t.Error(err)
			}
		}
	})
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				if err := r.SendTelegramToThread(t.Context(), "a", -100123, "during reload", 22, 77); err != nil && err.Error() != "Telegram account is not configured" {
					t.Error(err)
				}
				if _, err := r.TelegramGroup(t.Context(), "a", telegram.GroupInput{Operation: "info", ChatID: -100123}); err != nil && err.Error() != "Telegram account is not configured" {
					t.Error(err)
				}
				_, _ = r.TelegramWebhookSecret("a", strings.Repeat("master", 8))
				_ = r.Status()
				// Invalid secret must always fail before any database operation, regardless
				// of the snapshot used by the concurrent request.
				if err := r.TelegramWebhook(t.Context(), Store{}, "a", strings.Repeat("master", 8), []byte("bad"), []byte(`{}`)); err == nil {
					t.Error("invalid secret accepted during reload")
				}
				if err := r.EmailWebhook(t.Context(), Store{}, "a", "test", []byte("bad"), []byte(`{}`)); err == nil {
					t.Error("invalid email webhook accepted")
				}
			}
		})
	}
	wg.Wait()
	if err := r.Replace(&Registry{}); err != nil {
		t.Fatal(err)
	}
	count := calls.Load()
	if err := r.SendTelegram(t.Context(), "a", -100123, "after final disable", 0); err == nil || calls.Load() != count {
		t.Fatal("post-reload request used a stale snapshot")
	}
}
