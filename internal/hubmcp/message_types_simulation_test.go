package hubmcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func TestMessageTypesAuthenticatedSimulation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, dsn := simulationDatabase(t, ctx)
	store := &auth.Store{DB: db}
	if err := store.Bootstrap(ctx, "types-admin", "synthetic-password-message-types"); err != nil {
		t.Fatal(err)
	}
	session, err := store.Login(ctx, "types-admin", "synthetic-password-message-types", "simulation")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(account, label string, scopes ...string) string {
		t.Helper()
		if e := store.Grant(ctx, admin, admin.ID, account, true); e != nil {
			t.Fatal(e)
		}
		token, e := store.Issue(ctx, admin, label, account, scopes, 1)
		if e != nil {
			t.Fatal(e)
		}
		return token
	}
	send := issue("types-a", "send", "messages:send")
	read := issue("types-a", "read", "messages:read")
	other := issue("types-b", "other", "messages:send", "messages:read")
	// A bounded binary fixture tests exact transport, not browser image decoding.
	webp := make([]byte, 20)
	copy(webp, "RIFF")
	binary.LittleEndian.PutUint32(webp[4:8], 12)
	copy(webp[8:], "WEBPVP8L")
	var mu sync.Mutex
	type request struct {
		Path string
		Body map[string]any
	}
	var wire []request
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if r.Method == "POST" && json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		wire = append(wire, request{r.URL.Path, body})
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			if body["file_id"] != "received_sticker" {
				t.Error("unowned file reached provider")
			}
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"received_sticker","file_size":%d,"file_path":"stickers/file.webp"}}`, len(webp))
		case strings.HasPrefix(r.URL.Path, "/file/bot") || strings.HasPrefix(r.URL.Path, "/whatsapp_business/attachments/"):
			w.Header().Set("Content-Type", "image/webp")
			w.Write(webp)
		case r.URL.Path == "/v25.0/777":
			if r.URL.Query().Get("phone_number_id") != "111" {
				t.Error("missing account phone ownership")
			}
			fmt.Fprintf(w, `{"id":"777","mime_type":"image/webp","file_size":%d,"url":"https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=777"}`, len(webp))
		case r.URL.Path == "/v25.0/111/messages":
			if r.Header.Get("Authorization") != "Bearer synthetic-wa-types-a" {
				t.Error("wrong WhatsApp account token")
			}
			io.WriteString(w, `{"messages":[{"id":"wamid.types"}]}`)
		case strings.HasSuffix(r.URL.Path, "/setMessageReaction"):
			io.WriteString(w, `{"ok":true,"result":true}`)
		case strings.HasPrefix(r.URL.Path, "/bot123456:"):
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":71,"chat":{"id":%.0f}}}`, body["chat_id"])
		default:
			t.Error("unexpected fixture route", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer provider.Close()
	endpoint, _ := url.Parse(provider.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || (r.URL.Host != "api.telegram.org" && r.URL.Host != "graph.facebook.com" && r.URL.Host != "lookaside.fbsbx.com") {
			return nil, fmt.Errorf("unexpected provider origin")
		}
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = endpoint.Scheme, endpoint.Host
		copy.Host = endpoint.Host
		return transport.RoundTrip(copy)
	})}
	registry := &channels.Registry{Telegram: map[string]*telegram.Client{}}
	var accounts []whatsapp.Account
	for i, account := range []string{"types-a", "types-b"} {
		bot, e := telegram.NewWithHTTPClient(telegram.Config{AccountID: account, BotToken: "123456:" + strings.Repeat(string(rune('a'+i)), 24), Enabled: true}, client)
		if e != nil {
			t.Fatal(e)
		}
		registry.Telegram[account] = bot
		accounts = append(accounts, whatsapp.Account{ID: account, Site: "https://" + account + ".example", AccessToken: "synthetic-wa-" + account, PhoneID: fmt.Sprint(111 + i), WABAID: fmt.Sprint(222 + i), Version: "v25.0"})
	}
	wa, err := whatsapp.NewWithHTTPClient(accounts, client)
	if err != nil {
		t.Fatal(err)
	}
	wa.Events = whatsapp.PostgresEvents{URL: dsn}
	channelStore := channels.Store{DB: db}
	if err = channelStore.Save(ctx, channels.Event{Account: "types-a", Channel: "Telegram", Direction: "inbound", ProviderID: "1", Sender: "-1001", OccurredAt: time.Now(), Payload: json.RawMessage(`{"message":{"sticker":{"file_id":"received_sticker"}}}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO relay_whatsapp_incoming(account,provider_id,sender,payload) VALUES('types-a','wamid.sticker','919999999999','{"type":"sticker","sticker":{"id":"777"}}')`); err != nil {
		t.Fatal(err)
	}
	h, err := NewAuthenticatedChannels(db.Ping, wa, store.Authenticate, registry, channelStore)
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, tool string, args map[string]any, rejected bool) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var result struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool           `json:"isError"`
				Data    map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || (result.Result.IsError || len(result.Error) > 0) != rejected {
			t.Fatalf("unexpected %s response %d: %s", tool, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "synthetic-wa-") || strings.Contains(w.Body.String(), "/bot123456:") || strings.Contains(w.Body.String(), "lookaside.fbsbx.com") {
			t.Fatal("provider credential/URL exposed")
		}
		return result.Result.Data
	}
	t.Run("typed-Telegram-replies-and-clear-reaction", func(t *testing.T) {
		for _, args := range []map[string]any{
			{"operation": "text", "text": "வணக்கம்"},
			{"operation": "sticker", "sticker": "received_sticker"},
			{"operation": "location", "latitude": 12.97, "longitude": 77.59},
			{"operation": "contact", "phone_number": "+919999999999", "first_name": "சக்தி"},
		} {
			args["chat_id"] = -1001
			args["message_thread_id"] = 8
			args["reply_to"] = 9
			call(read, "telegram.message", args, true)
			call(send, "telegram.message", args, false)
		}
		for _, reactions := range []any{[]map[string]any{{"type": "emoji", "emoji": "👍"}}, []map[string]any{}} {
			call(send, "telegram.message", map[string]any{"operation": "reaction", "chat_id": -1001, "message_id": 9, "reaction": reactions}, false)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, r := range wire[:4] {
			if r.Body["chat_id"] != float64(-1001) || r.Body["message_thread_id"] != float64(8) || r.Body["reply_parameters"].(map[string]any)["message_id"] != float64(9) {
				t.Fatal("typed reply lost routing", r)
			}
		}
		if reactions, ok := wire[5].Body["reaction"].([]any); !ok || len(reactions) != 0 {
			t.Fatal("clear reaction omitted empty array")
		}
	})
	t.Run("WhatsApp-location-contact-and-reaction-replies", func(t *testing.T) {
		for _, args := range []map[string]any{
			{"operation": "messages.location", "location": map[string]any{"latitude": 12.97, "longitude": 77.59, "name": "Office", "address": "Bengaluru"}},
			{"operation": "messages.contacts", "contacts": []any{map[string]any{"name": map[string]any{"formatted_name": "சக்தி", "first_name": "சக்தி"}, "phones": []any{map[string]any{"phone": "+919999999999"}}}}},
			{"operation": "messages.react", "emoji": "👍"},
			{"operation": "messages.react", "emoji": ""},
		} {
			args["to"] = "919999999999"
			args["reply_to"] = "wamid.original"
			call(read, "whatsapp.execute", args, true)
			call(send, "whatsapp.execute", args, false)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, r := range wire[6:8] {
			if r.Body["context"].(map[string]any)["message_id"] != "wamid.original" || r.Body["to"] != "919999999999" {
				t.Fatal("WhatsApp quote/destination lost")
			}
		}
		if wire[9].Body["reaction"].(map[string]any)["emoji"] != "" {
			t.Fatal("WhatsApp clear reaction lost")
		}
	})
	t.Run("sticker-media-is-scoped-and-bytes-match", func(t *testing.T) {
		for _, tc := range []struct{ tool, key, id string }{{"telegram.media", "file_id", "received_sticker"}, {"whatsapp.media", "media_id", "777"}} {
			args := map[string]any{tc.key: tc.id}
			mu.Lock()
			before := len(wire)
			mu.Unlock()
			call(send, tc.tool, args, true)
			call(other, tc.tool, args, true)
			call(read, tc.tool, map[string]any{tc.key: "unknown"}, true)
			mu.Lock()
			after := len(wire)
			mu.Unlock()
			if after != before {
				t.Fatal("denied media made provider request")
			}
			data := call(read, tc.tool, args, false)
			decoded, e := base64.StdEncoding.DecodeString(data["content_base64"].(string))
			if e != nil || !bytes.Equal(decoded, webp) || data["content_type"] != "image/webp" {
				t.Fatal("sticker payload changed")
			}
		}
	})
}

func TestMediaEnvelopeDoesNotDuplicateLargePayload(t *testing.T) {
	data := bytes.Repeat([]byte{1}, 2<<20)
	r, content, err := mediaResult(data, "image/webp", "sticker.webp")
	if err != nil {
		t.Fatal(err)
	}
	r.StructuredContent = content
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 3<<20 {
		t.Fatal("MCP duplicated large binary payload", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(content["content_base64"].(string))
	if !reflect.DeepEqual(data, decoded) {
		t.Fatal("bytes changed")
	}
}
