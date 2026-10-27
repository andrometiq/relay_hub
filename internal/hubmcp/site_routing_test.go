package hubmcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/telegram"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

func callTool(handler http.Handler, name string, arguments map[string]any) (bool, map[string]any, string) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	request := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(string(raw)))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var envelope struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	ok := json.Unmarshal(response.Body.Bytes(), &envelope) == nil && !envelope.Result.IsError
	return ok, envelope.Result.Structured, response.Body.String()
}

// Site A has two numbers and two bots; site B has one number.
func TestSendsNameTheChannelOfTheSite(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		io.WriteString(w, `{"success":true}`)
	}))
	defer provider.Close()
	endpoint, _ := url.Parse(provider.URL)
	client := &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host, copy.Host = endpoint.Scheme, endpoint.Host, endpoint.Host
		return http.DefaultTransport.RoundTrip(copy)
	})}
	number := func(key, site, phone string) whatsapp.Account {
		return whatsapp.Account{ID: key, SiteID: site, Site: "https://" + site + ".example", AccessToken: "provider-secret", Version: "v25.0", PhoneID: phone, WABAID: "222"}
	}
	wa, err := whatsapp.NewWithHTTPClient([]whatsapp.Account{number("sales", "site-a", "111"), number("support", "site-a", "333"), number("pos", "site-b", "555")}, client)
	if err != nil {
		t.Fatal(err)
	}
	bots := map[string]*telegram.Client{}
	for _, key := range []string{"bot-a1", "bot-a2", "bot-b"} {
		bot, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: key, BotToken: "123456:" + strings.Repeat("a", 24), Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			paths = append(paths, "telegram/"+key)
			mu.Unlock()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		bots[key] = bot
	}
	registry := &channels.Registry{Telegram: bots, Bindings: map[string]channels.Binding{
		"Telegram/bot-a1": {Site: "site-a"}, "Telegram/bot-a2": {Site: "site-a"}, "Telegram/bot-b": {Site: "site-b"}}}
	handler := func(site string) http.Handler {
		h, err := NewAuthenticatedChannels(func(context.Context) error { return nil }, wa, func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{UserID: 1, Site: site, Scopes: []string{"messages:send"}}, nil
		}, registry)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	a, b := handler("site-a"), handler("site-b")
	read := map[string]any{"operation": "messages.read", "message_id": "wamid.SYNTHETIC", "typing": true}

	if ok, _, body := callTool(a, "whatsapp.execute", read); ok || !strings.Contains(body, "channel required") {
		t.Fatalf("two numbers without channel: %s", body)
	}
	read["channel"] = "pos"
	if ok, _, body := callTool(a, "whatsapp.execute", read); ok || !strings.Contains(body, "not available to this site") {
		t.Fatalf("foreign number: %s", body)
	}
	read["channel"] = "support"
	if ok, _, body := callTool(a, "whatsapp.execute", read); !ok {
		t.Fatalf("own number: %s", body)
	}
	delete(read, "channel")
	if ok, _, body := callTool(b, "whatsapp.execute", read); !ok {
		t.Fatalf("only number default: %s", body)
	}

	typing := map[string]any{"chat_id": 123456}
	if ok, _, body := callTool(a, "telegram.typing", typing); ok || !strings.Contains(body, "channel required") {
		t.Fatalf("two bots without channel: %s", body)
	}
	typing["channel"] = "bot-b"
	if ok, _, body := callTool(a, "telegram.typing", typing); ok || !strings.Contains(body, "not available to this site") {
		t.Fatalf("foreign bot: %s", body)
	}
	typing["channel"] = "bot-a2"
	if ok, _, body := callTool(a, "telegram.typing", typing); !ok {
		t.Fatalf("own bot: %s", body)
	}
	delete(typing, "channel")
	if ok, _, body := callTool(b, "telegram.typing", typing); !ok {
		t.Fatalf("only bot default: %s", body)
	}

	// Read receipts and typing leave from the number or bot the call named.
	want := []string{"/v25.0/333/messages", "/v25.0/555/messages", "telegram/bot-a2", "telegram/bot-b"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("provider calls: %v", paths)
	}
}

func TestChannelsListShowsOnlyTheSiteChannels(t *testing.T) {
	ctx := context.Background()
	db, _ := simulationDatabase(t, ctx)
	statements := []string{
		`INSERT INTO relay_sites(site_id,origin,enabled) VALUES('site-a','https://a.test',true),('site-b','https://b.test',true)`,
		`INSERT INTO relay_containers(container_id) VALUES('main')`,
		`INSERT INTO relay_channel_credentials(channel,channel_key,container_id,label,address,site_id,enabled,config) VALUES
 ('WhatsApp','sales','main','Sales','111','site-a',true,'{"phone_id":"111","waba_id":"222","display_number":"+91 1","templates":true}'),
 ('WhatsApp','support','main','Support','333','site-a',false,'{"phone_id":"333","waba_id":"222"}'),
 ('Telegram','bot-b','main','Bot','500001','site-b',true,'{"username":"b_bot"}'),
 ('Email','loose','main','Loose','x@example.test',NULL,true,'{}')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	h, err := NewAuthenticatedChannels(db.Ping, nil, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{Site: "site-a", Scopes: []string{"account:read"}}, nil
	}, nil, channels.Store{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	ok, result, body := callTool(h, "channels.list", map[string]any{})
	if !ok {
		t.Fatal(body)
	}
	list := result["channels"].([]any)
	first := list[0].(map[string]any)
	if len(list) != 2 || first["key"] != "sales" || first["display_number"] != "+91 1" || first["waba_id"] != "222" || first["container"] != "main" ||
		first["capabilities"].(map[string]any)["templates"] != true || list[1].(map[string]any)["enabled"] != false || strings.Contains(body, "secret") {
		t.Fatalf("channels: %s", body)
	}
	ok, result, body = callTool(h, "site.get", map[string]any{})
	if !ok || result["account"] != "site-a" || result["site"] != "https://a.test" || len(result["channels"].([]any)) != 2 {
		t.Fatalf("site.get: %s", body)
	}
}
