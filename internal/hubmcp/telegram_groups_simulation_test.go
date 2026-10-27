package hubmcp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

func TestTelegramGroupMCPFailureMetadata(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		body           string
		retry, migrate int64
		unknown        bool
	}{
		{"rate limit", 429, `{"ok":false,"error_code":429,"description":"secret bot token","parameters":{"retry_after":12}}`, 12, 0, false},
		{"group migration", 400, `{"ok":false,"error_code":400,"description":"private provider detail","parameters":{"migrate_to_chat_id":-100999888}}`, 0, -100999888, false},
		{"transport uncertain", 0, "", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: "a", BotToken: "123456:" + strings.Repeat("a", 24), Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.unknown {
					return nil, errors.New("private token-bearing URL")
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewAuthenticatedChannels(func(context.Context) error { return nil }, nil, func(context.Context, string) (auth.Principal, error) {
				return auth.Principal{Site: "a"}, nil
			}, &channels.Registry{Telegram: map[string]*telegram.Client{"a": client}})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"telegram.groups","arguments":{"operation":"title","chat_id":-100123456,"title":"Simulation"}}}`))
			r.Header.Set("Authorization", "Bearer synthetic")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			r.Header.Set("MCP-Protocol-Version", "2025-06-18")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var response struct {
				Result struct {
					IsError    bool `json:"isError"`
					Structured struct {
						Provider string `json:"provider"`
						Error    struct {
							Retry   int64 `json:"retry_after"`
							Migrate int64 `json:"migrate_to_chat_id"`
							Unknown bool  `json:"outcome_unknown"`
						} `json:"error"`
					} `json:"structuredContent"`
				} `json:"result"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.Result.IsError {
				t.Fatalf("not an MCP tool failure: %s", w.Body.String())
			}
			meta := response.Result.Structured
			if meta.Provider != "Telegram" || meta.Error.Retry != tc.retry || meta.Error.Migrate != tc.migrate || meta.Error.Unknown != tc.unknown {
				t.Fatalf("lost error metadata: %s", w.Body.String())
			}
			if calls != 1 {
				t.Fatalf("mutation replayed %d times", calls)
			}
			for _, secret := range []string{"secret bot token", "private provider detail", "private token-bearing URL"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("provider secrets exposed")
				}
			}
		})
	}
}

func TestTelegramCredentialReloadReachesExistingMCPHandler(t *testing.T) {
	var used []string
	newClient := func(token string) *telegram.Client {
		t.Helper()
		c, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: "a", BotToken: token, Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
			used = append(used, r.URL.Path)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":91}}`))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	oldToken := "123456:" + strings.Repeat("a", 24)
	newToken := "123456:" + strings.Repeat("b", 24)
	registry := &channels.Registry{Telegram: map[string]*telegram.Client{"a": newClient(oldToken)}}
	h, err := NewAuthenticatedChannels(func(context.Context) error { return nil }, nil, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{Site: "a"}, nil
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	call := func(fail bool) {
		t.Helper()
		req := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"telegram.send","arguments":{"chat_id":-1001234,"text":"Synthetic group message","message_thread_id":7}}}`))
		req.Header.Set("Authorization", "Bearer synthetic")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var response struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Result.IsError != fail {
			t.Fatalf("unexpected result: %s", w.Body.String())
		}
	}
	call(false)
	if err := registry.Replace(&channels.Registry{Telegram: map[string]*telegram.Client{"a": newClient(newToken)}}); err != nil {
		t.Fatal(err)
	}
	call(false)
	if err := registry.Replace(&channels.Registry{}); err != nil {
		t.Fatal(err)
	}
	call(true)
	if want := []string{"/bot" + oldToken + "/sendMessage", "/bot" + newToken + "/sendMessage"}; !reflect.DeepEqual(used, want) {
		t.Fatal("MCP used stale credentials after rotation/disable")
	}
}

// The database, token verifier, MCP and callback paths are real. Telegram and
// Core are loopback HTTP fixtures. No bot token or external endpoint is used.
func TestTelegramGroupsAuthenticatedSimulation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, scopedDSN := simulationDatabase(t, ctx)
	authStore := &auth.Store{DB: db}
	if err := authStore.Bootstrap(ctx, "group-admin", "synthetic-group-password-42"); err != nil {
		t.Fatal(err)
	}
	session, err := authStore.Login(ctx, "group-admin", "synthetic-group-password-42", "simulation")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := authStore.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(account, label string) string {
		if _, err := db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES($1,'https://fixture.invalid') ON CONFLICT DO NOTHING`, account); err != nil {
			t.Fatal(err)
		}
		t.Helper()
		if e := authStore.Grant(ctx, admin, admin.ID, account, true); e != nil {
			t.Fatal(e)
		}
		token, e := authStore.Issue(ctx, admin, label, account, 1)
		if e != nil {
			t.Fatal(e)
		}
		return token
	}
	key := issue("group-a", "key")
	revocable := issue("group-a", "revocable")
	other := issue("group-b", "other")
	unconfigured := issue("group-missing", "unconfigured")
	botTokens := map[string]string{"group-a": "123456:" + strings.Repeat("a", 24), "group-b": "654321:" + strings.Repeat("b", 24)}
	type wireRequest struct {
		Account, Method string
		Body            map[string]any
	}
	var mu sync.Mutex
	var requests []wireRequest
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account, method := "", ""
		for a, token := range botTokens {
			if strings.HasPrefix(r.URL.Path, "/bot"+token+"/") {
				account, method = a, strings.TrimPrefix(r.URL.Path, "/bot"+token+"/")
			}
		}
		var body map[string]any
		if account == "" || r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid Telegram fixture request")
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		requests = append(requests, wireRequest{account, method, body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getChat":
			fmt.Fprintf(w, `{"ok":true,"result":{"id":%.0f,"type":"supergroup","title":"Simulation"}}`, body["chat_id"])
		case "sendMessage":
			io.WriteString(w, `{"ok":true,"result":{"message_id":901}}`)
		case "setChatTitle":
			io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			t.Error("unexpected fixture method", method)
			w.WriteHeader(400)
		}
	}))
	defer provider.Close()
	providerURL, _ := url.Parse(provider.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" || r.URL.Scheme != "https" {
			return nil, fmt.Errorf("unexpected simulation destination")
		}
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = providerURL.Scheme, providerURL.Host
		copy.Host = providerURL.Host
		return transport.RoundTrip(copy)
	})}
	registry := &channels.Registry{Telegram: map[string]*telegram.Client{}}
	for a, token := range botTokens {
		client, e := telegram.NewWithHTTPClient(telegram.Config{AccountID: a, BotToken: token, Enabled: true}, httpClient)
		if e != nil {
			t.Fatal(e)
		}
		registry.Telegram[a] = client
	}
	handler, err := NewAuthenticatedChannels(db.Ping, nil, authStore.Authenticate, registry)
	if err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(handler)
	defer hub.Close()
	call := func(token, tool string, args map[string]any, rejected bool) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req, _ := http.NewRequestWithContext(ctx, "POST", hub.URL, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		res, e := hub.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		body, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		if res.StatusCode == 401 && rejected {
			return
		}
		var response struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if res.StatusCode != 200 || json.Unmarshal(body, &response) != nil {
			t.Fatalf("invalid MCP response %d %s", res.StatusCode, body)
		}
		failed := response.Result.IsError || len(response.Error) > 0
		if failed != rejected {
			t.Fatalf("%s rejected=%v want=%v: %s", tool, failed, rejected, body)
		}
		for _, bot := range botTokens {
			if bytes.Contains(body, []byte(bot)) {
				t.Fatal("bot credential exposed")
			}
		}
	}
	group := int64(-100123456789)
	info := map[string]any{"operation": "info", "chat_id": group}
	t.Run("site-key-reads-and-writes", func(t *testing.T) {
		call(key, "telegram.groups", info, false)
		call(key, "telegram.groups", map[string]any{"operation": "title", "chat_id": group, "title": "தமிழ்"}, false)
		call(unconfigured, "telegram.groups", info, true)
	})
	t.Run("same-group-different-bot-account", func(t *testing.T) { call(other, "telegram.groups", info, false) })
	t.Run("topic-reply-keeps-destination", func(t *testing.T) {
		args := map[string]any{"chat_id": group, "text": "குழு பதில்", "reply_to": 55, "message_thread_id": 77}
		call(key, "telegram.send", args, false)
		call(other, "telegram.send", args, false)
	})
	t.Run("caller-cannot-select-account-or-provider", func(t *testing.T) {
		call(key, "telegram.groups", map[string]any{"operation": "info", "chat_id": group, "account": "group-b"}, true)
		call(key, "telegram.groups", map[string]any{"operation": "create", "chat_id": group}, true)
		call(key, "telegram.groups", map[string]any{"operation": "getUpdates", "chat_id": group}, true)
	})
	t.Run("revoked-token-stops-calls", func(t *testing.T) {
		var tokenID int64
		if e := db.QueryRow(ctx, "SELECT id FROM relay_api_tokens WHERE token_hash=$1", auth.Hash(revocable)).Scan(&tokenID); e != nil {
			t.Fatal(e)
		}
		if e := authStore.Revoke(ctx, admin, tokenID); e != nil {
			t.Fatal(e)
		}
		call(revocable, "telegram.groups", map[string]any{"operation": "title", "chat_id": group, "title": "Revoked"}, true)
	})
	mu.Lock()
	want := []wireRequest{
		{"group-a", "getChat", map[string]any{"chat_id": float64(group)}},
		{"group-a", "setChatTitle", map[string]any{"chat_id": float64(group), "title": "தமிழ்"}},
		{"group-b", "getChat", map[string]any{"chat_id": float64(group)}},
		{"group-a", "sendMessage", map[string]any{"chat_id": float64(group), "text": "குழு பதில்", "message_thread_id": float64(77), "reply_parameters": map[string]any{"message_id": float64(55)}}},
		{"group-b", "sendMessage", map[string]any{"chat_id": float64(group), "text": "குழு பதில்", "message_thread_id": float64(77), "reply_parameters": map[string]any{"message_id": float64(55)}}},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Errorf("wire requests differ: got %#v want %#v", requests, want)
	}
	mu.Unlock()

	// Same Telegram update ID in two accounts must be distinct; replay of either
	// must be deduplicated, while provenance survives webhook -> DB -> callback.
	updates := []string{
		`{"update_id":51,"message":{"message_id":55,"date":1700000000,"chat":{"id":-100123456789,"type":"supergroup"},"from":{"id":42},"message_thread_id":77,"text":"குழு கேள்வி","reply_to_message":{"message_id":54}}}`,
		`{"update_id":52,"message":{"message_id":56,"date":1700000001,"chat":{"id":-100123456789,"type":"supergroup"},"from":{"id":42},"new_chat_members":[{"id":43}]}}`,
		`{"update_id":53,"message_reaction":{"chat":{"id":-100123456789,"type":"supergroup"},"message_id":55,"user":{"id":43},"date":1700000002,"old_reaction":[],"new_reaction":[{"type":"emoji","emoji":"👍"}]}}`,
	}
	events := channels.Store{DB: db}
	t.Run("webhook-secret-dedupe-and-account-isolation", func(t *testing.T) {
		master := strings.Repeat("synthetic-master-", 3)
		secretA, e := registry.TelegramWebhookSecret("group-a", master)
		if e != nil {
			t.Fatal(e)
		}
		secretB, e := registry.TelegramWebhookSecret("group-b", master)
		if e != nil || secretA == secretB || secretA == master {
			t.Fatal("webhook secrets are not account-isolated", e)
		}
		if e := registry.TelegramWebhook(ctx, events, "group-b", master, []byte(secretA), []byte(updates[0])); e == nil {
			t.Fatal("another bot's webhook token accepted")
		}
		if e := registry.TelegramWebhook(ctx, events, "group-a", master, []byte(master), []byte(updates[0])); e == nil {
			t.Fatal("shared master accepted as webhook token")
		}
		for _, account := range []string{"group-a", "group-b"} {
			secret, e := registry.TelegramWebhookSecret(account, master)
			if e != nil {
				t.Fatal(e)
			}
			for _, raw := range updates {
				if e := registry.TelegramWebhook(ctx, events, account, master, []byte("wrong"), []byte(raw)); e == nil {
					t.Fatal("invalid webhook secret accepted")
				}
				for i := 0; i < 2; i++ {
					if e := registry.TelegramWebhook(ctx, events, account, master, []byte(secret), []byte(raw)); e != nil {
						t.Fatal(e)
					}
				}
			}
		}
		var count int
		if e := db.QueryRow(ctx, "SELECT count(*) FROM relay_channel_events").Scan(&count); e != nil || count != 6 {
			t.Fatalf("dedupe count=%d err=%v", count, e)
		}
	})
	var received int
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("callback-secret"))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if r.Header.Get("Authorization") != "token key:secret" || r.Header.Get("X-Relay-Signature") != hex.EncodeToString(mac.Sum(nil)) {
			t.Error("callback authentication changed")
		}
		var batch struct {
			Account string `json:"account"`
			Events  []struct {
				Payload struct {
					Channel, Sender, Body string
					Payload               json.RawMessage `json:"payload"`
				} `json:"payload"`
			} `json:"events"`
		}
		if json.Unmarshal(raw, &batch) != nil || batch.Account != "group-a" || len(batch.Events) != 3 {
			t.Error("invalid callback account/batch")
			w.WriteHeader(400)
			return
		}
		for i, event := range batch.Events {
			var actual, expected any
			json.Unmarshal(event.Payload.Payload, &actual)
			json.Unmarshal([]byte(updates[i]), &expected)
			if event.Payload.Channel != "Telegram" || event.Payload.Sender != "-100123456789" || !reflect.DeepEqual(actual, expected) {
				t.Error("group provenance changed in callback")
			}
			if i > 0 && event.Payload.Body != "" {
				t.Error("service/reaction became user text")
			}
		}
		received += len(batch.Events)
		io.WriteString(w, `{"message":{"accepted":3}}`)
	}))
	defer core.Close()
	t.Run("signed-callback-preserves-participant-topic-and-event", func(t *testing.T) {
		conn, e := db.Acquire(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Release()
		dispatcher := channels.CallbackDispatcher{URL: scopedDSN}
		config := channels.RuntimeCallback{Account: "group-a", Site: core.URL, APIKey: "key", APISecret: "secret", CallbackSecret: "callback-secret"}
		after, e := dispatcher.Deliver(ctx, conn.Conn(), config, 0)
		if e != nil || after == 0 {
			t.Fatal("callback failed", e)
		}
		if _, e = dispatcher.Deliver(ctx, conn.Conn(), config, after); e != nil {
			t.Fatal(e)
		}
		if received != 3 {
			t.Fatalf("callback replayed or crossed account: %d", received)
		}
	})
}
