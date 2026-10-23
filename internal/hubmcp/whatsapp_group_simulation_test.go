package hubmcp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
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
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

// Synthetic Graph contract from Meta's group management/messaging references:
// https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/reference
// https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/groups-messaging
// Real auth, MCP, outbox, webhook verification, PostgreSQL and callback workers
// run here. No live provider or business account is contacted.
func TestWhatsAppGroupAuthenticatedSimulation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	db, dsn := simulationDatabase(t, ctx)
	store := &auth.Store{DB: db}
	if err := store.Bootstrap(ctx, "group-admin", "synthetic-group-password-42"); err != nil {
		t.Fatal(err)
	}
	session, err := store.Login(ctx, "group-admin", "synthetic-group-password-42", "groups")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	token := func(account string, scopes []string) string {
		t.Helper()
		if e := store.Grant(ctx, admin, admin.ID, account, true); e != nil {
			t.Fatal(e)
		}
		value, e := store.Issue(ctx, admin, account, account, scopes, 1)
		if e != nil {
			t.Fatal(e)
		}
		return value
	}
	scopes := []string{"messages:send", "messages:read", "groups:read", "groups:write", "deliveries:read"}
	tokenA, tokenB := token("group-a", scopes), token("group-b", scopes)
	readToken := token("group-a", []string{"groups:read"})
	var mu sync.Mutex
	callbacks := map[string][]json.RawMessage{}
	statusCallbacks := map[string][]whatsapp.CompactStatus{}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var batch struct {
			Account  string                   `json:"account"`
			Events   []whatsapp.Event         `json:"events"`
			Statuses []whatsapp.CompactStatus `json:"statuses"`
		}
		if json.Unmarshal(raw, &batch) != nil {
			t.Error("bad callback JSON")
			w.WriteHeader(400)
			return
		}
		secret := strings.Repeat(batch.Account, 8)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if (batch.Account != "group-a" && batch.Account != "group-b") || r.Header.Get("Authorization") != "token key-"+batch.Account+":secret-"+batch.Account || r.Header.Get("X-Relay-Signature") != hex.EncodeToString(mac.Sum(nil)) {
			t.Error("callback account or signature invalid")
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, e := range batch.Events {
			callbacks[batch.Account] = append(callbacks[batch.Account], e.Payload)
		}
		statusCallbacks[batch.Account] = append(statusCallbacks[batch.Account], batch.Statuses...)
		fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(batch.Events)+len(batch.Statuses))
	}))
	defer core.Close()
	accounts := []whatsapp.Account{}
	for i, name := range []string{"group-a", "group-b"} {
		accounts = append(accounts, whatsapp.Account{ID: name, Site: core.URL, AccessToken: "synthetic-" + name, Version: "v25.0", PhoneID: fmt.Sprint(110 + i), WABAID: fmt.Sprint(220 + i), AppSecret: "app-" + name, Groups: true, Templates: true, CoreAPIKey: "key-" + name, CoreAPISecret: "secret-" + name, CallbackSecret: strings.Repeat(name, 8), MessagesPerSecond: 1000})
	}
	// A deterministic noisy JPEG exceeds the old 1 MiB MCP envelope cap.
	photo := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	var seed uint32 = 42
	for i := 0; i < len(photo.Pix); i += 4 {
		for j := 0; j < 3; j++ {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			photo.Pix[i+j] = byte(seed)
		}
		photo.Pix[i+3] = 255
	}
	var photoBytes bytes.Buffer
	if err := jpeg.Encode(&photoBytes, photo, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	photoEncoded := base64.StdEncoding.EncodeToString(photoBytes.Bytes())
	if len(photoEncoded) < 1<<20 {
		t.Fatal("photo fixture must exceed old MCP limit")
	}
	uploaded := false
	sent := map[string][]map[string]any{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		owner := ""
		for _, a := range accounts {
			if r.Header.Get("Authorization") == "Bearer "+a.AccessToken {
				owner = a.ID
			}
		}
		if owner == "" {
			t.Error("provider received unknown credentials")
			w.WriteHeader(401)
			return
		}
		a := accounts[0]
		if owner == "group-b" {
			a = accounts[1]
		}
		if r.URL.Path == "/v25.0/"+a.PhoneID+"/groups" && r.Method == "GET" {
			if r.URL.Query().Get("after") == "" {
				fmt.Fprintf(w, `{"data":{"groups":[{"id":"other-%s"}]},"paging":{"cursors":{"after":"page-2"}}}`, owner)
			} else {
				fmt.Fprintf(w, `{"data":{"groups":[{"id":"room-%s"}]}}`, owner)
			}
			return
		}
		if r.URL.Path == "/v25.0/room-"+owner+"/invite_link" && r.Method == "GET" {
			fmt.Fprint(w, `{"invite_link":"https://chat.whatsapp.com/synthetic"}`)
			return
		}
		if r.URL.Path == "/v25.0/room-"+owner && r.Method == "POST" {
			if err := r.ParseMultipartForm(6 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, _, e := r.FormFile("file")
			if e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			raw, _ := io.ReadAll(file)
			file.Close()
			if !bytes.Equal(raw, photoBytes.Bytes()) || r.FormValue("messaging_product") != "whatsapp" {
				t.Error("multipart photo payload changed")
			}
			mu.Lock()
			uploaded = true
			mu.Unlock()
			fmt.Fprint(w, `{"request_id":"group-photo-request"}`)
			return
		}
		if r.URL.Path != "/v25.0/"+a.PhoneID+"/messages" || r.Method != "POST" {
			t.Errorf("unexpected provider route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["to"] != "room-"+owner || body["recipient_type"] != "group" || body["messaging_product"] != "whatsapp" {
			t.Error("group send crossed an account boundary")
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		sent[owner] = append(sent[owner], body)
		mu.Unlock()
		tracking, _ := body["biz_opaque_callback_data"].(string)
		if !strings.HasPrefix(tracking, "relay-outbox:") {
			t.Error("durable tracking missing")
		}
		fmt.Fprintf(w, `{"messages":[{"id":"wamid.%s"}]}`, strings.TrimPrefix(tracking, "relay-outbox:"))
	}))
	defer provider.Close()
	pURL, _ := url.Parse(provider.URL)
	cURL, _ := url.Parse(core.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 3 * time.Second, Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme == "https" && r.URL.Host == "graph.facebook.com" {
			clone := r.Clone(r.Context())
			clone.URL.Scheme, clone.URL.Host = pURL.Scheme, pURL.Host
			clone.Host = pURL.Host
			return transport.RoundTrip(clone)
		}
		if r.URL.Host == cURL.Host && r.URL.Scheme == cURL.Scheme {
			return transport.RoundTrip(r)
		}
		return nil, fmt.Errorf("external network forbidden")
	})}
	wa, err := whatsapp.NewWithHTTPClient(accounts, client)
	if err != nil {
		t.Fatal(err)
	}
	wa.Events = whatsapp.PostgresEvents{URL: dsn}
	wa.Outbox = &whatsapp.Outbox{URL: dsn, Service: wa, Workers: 1}
	handler, err := NewAuthenticated(db.Ping, wa, store.Authenticate)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("/webhooks/whatsapp/{account}", wa.Webhook)
	hub := httptest.NewServer(mux)
	defer hub.Close()
	call := func(token, tool string, args any, wantError bool) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req, _ := http.NewRequestWithContext(ctx, "POST", hub.URL+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		res, e := hub.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var env struct {
			Result struct {
				IsError bool                       `json:"isError"`
				Data    map[string]json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		if json.NewDecoder(res.Body).Decode(&env) != nil || res.StatusCode != 200 || env.Result.IsError != wantError {
			t.Fatalf("MCP %s HTTP %d error %v want %v", tool, res.StatusCode, env.Result.IsError, wantError)
		}
		return env.Result.Data
	}
	t.Run("scopes_and_account_ownership", func(t *testing.T) {
		call(readToken, "whatsapp.execute", whatsapp.Input{Operation: "groups.invite.get", ID: "room-group-a"}, false)
		call(readToken, "whatsapp.execute", whatsapp.Input{Operation: "groups.invite.reset", ID: "room-group-a"}, true)
		call(tokenB, "whatsapp.execute", whatsapp.Input{Operation: "groups.send", To: "room-group-a", Body: "cross-tenant"}, true)
		call(readToken, "whatsapp.send", map[string]any{"message": whatsapp.Submission{Key: "denied", Message: whatsapp.Input{Operation: "groups.send", To: "room-group-a", Body: "scope denied"}}}, true)
	})
	t.Run("bounded_photo_upload_through_authenticated_MCP", func(t *testing.T) {
		call(readToken, "whatsapp.execute", whatsapp.Input{Operation: "groups.photo", ID: "room-group-a", PhotoBase64: photoEncoded}, true)
		call(tokenA, "whatsapp.execute", whatsapp.Input{Operation: "groups.photo", ID: "room-group-a", PhotoBase64: photoEncoded}, false)
		mu.Lock()
		defer mu.Unlock()
		if !uploaded {
			t.Fatal("photo was not uploaded")
		}
	})
	batch := []whatsapp.Submission{
		{Key: "text", Message: whatsapp.Input{Operation: "groups.send", To: "room-group-a", Body: "Synthetic group text", ReplyTo: "wamid.quoted"}},
		{Key: "media", Message: whatsapp.Input{Operation: "groups.media", To: "room-group-a", MediaType: "document", MediaURL: "https://example.invalid/synthetic.pdf", Filename: "synthetic.pdf"}},
		{Key: "template", Message: whatsapp.Input{Operation: "groups.template", To: "room-group-a", Name: "synthetic_update", Language: "en_US"}},
		{Key: "pin", Message: whatsapp.Input{Operation: "groups.pin", To: "room-group-a", ReplyTo: "wamid.existing", Action: "pin", ExpirationDays: 1}},
	}
	var deliveries []whatsapp.Delivery
	t.Run("durable_batch_replay_and_payload_conflict", func(t *testing.T) {
		first := call(tokenA, "whatsapp.bulk", map[string]any{"messages": batch}, false)
		if json.Unmarshal(first["deliveries"], &deliveries) != nil || len(deliveries) != 4 {
			t.Fatal("bad delivery batch")
		}
		again := call(tokenA, "whatsapp.bulk", map[string]any{"messages": batch}, false)
		if !bytes.Equal(first["deliveries"], again["deliveries"]) {
			t.Fatal("replay duplicated group messages")
		}
		changed := append([]whatsapp.Submission{}, batch...)
		changed[0].Message.Body = "conflicting"
		call(tokenA, "whatsapp.bulk", map[string]any{"messages": changed}, true)
	})
	call(tokenB, "whatsapp.send", map[string]any{"message": whatsapp.Submission{Key: "text", Message: whatsapp.Input{Operation: "groups.send", To: "room-group-b", Body: "Other account group"}}}, false)
	call(tokenB, "whatsapp.send", map[string]any{"message": whatsapp.Submission{Key: "foreign", Message: whatsapp.Input{Operation: "groups.send", To: "room-group-a", Body: "Must never send"}}}, false)
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	wa.Outbox.Run(workerCtx)
	wait := func(label string, fn func() bool) {
		t.Helper()
		until := time.Now().Add(8 * time.Second)
		for time.Now().Before(until) {
			if fn() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("timed out", label)
	}
	wait("group queue", func() bool {
		var n int
		return db.QueryRow(ctx, "SELECT count(*) FROM relay_outbox WHERE state IN ('queued','submitting')").Scan(&n) == nil && n == 0
	})
	stop()
	t.Run("worker_preserves_group_payload_and_denies_foreign_group", func(t *testing.T) {
		mu.Lock()
		defer mu.Unlock()
		if len(sent["group-a"]) != 4 || len(sent["group-b"]) != 1 {
			t.Fatalf("wrong group provider send counts %v", sent)
		}
		kinds := []string{"text", "document", "template", "pin"}
		for i, body := range sent["group-a"] {
			if body["type"] != kinds[i] || body["biz_opaque_callback_data"] != "relay-outbox:"+deliveries[i].ID {
				t.Fatalf("wrong group payload %#v", body)
			}
		}
		if sent["group-a"][0]["context"].(map[string]any)["message_id"] != "wamid.quoted" {
			t.Fatal("reply context lost")
		}
		if sent["group-a"][0]["text"].(map[string]any)["body"] != "Synthetic group text" {
			t.Fatal("text changed")
		}
		if sent["group-a"][1]["document"].(map[string]any)["filename"] != "synthetic.pdf" {
			t.Fatal("media changed")
		}
		var state string
		if err := db.QueryRow(ctx, "SELECT state FROM relay_outbox WHERE account='group-b' AND request_key='foreign'").Scan(&state); err != nil || state != "rejected" {
			t.Fatal("foreign group intent accepted", err)
		}
	})
	wrap := func(a whatsapp.Account, field string, value map[string]any) []byte {
		value["metadata"] = map[string]any{"phone_number_id": a.PhoneID}
		raw, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": a.WABAID, "changes": []any{map[string]any{"field": field, "value": value}}}}})
		return raw
	}
	webhook := func(a whatsapp.Account, raw []byte, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "POST", hub.URL+"/webhooks/whatsapp/"+a.ID, bytes.NewReader(raw))
		mac := hmac.New(sha256.New, []byte(a.AppSecret))
		mac.Write(raw)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		res, e := hub.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("webhook HTTP %d want %d", res.StatusCode, want)
		}
	}
	incoming := []map[string]any{{"from": "919999999991", "group_id": "room-group-a", "id": "wamid.inbound.1", "timestamp": "1755548877", "type": "text", "text": map[string]any{"body": "Group question"}, "context": map[string]any{"id": "wamid.existing"}}, {"from": "919999999992", "group_id": "room-group-a", "id": "wamid.inbound.2", "timestamp": "1755548878", "type": "document", "document": map[string]any{"id": "1234", "filename": "receipt.pdf"}}, {"from": "919999999991", "id": "wamid.direct.1", "timestamp": "1755548879", "type": "text", "text": map[string]any{"body": "Private question"}}}
	payloads := [][]byte{}
	for _, m := range incoming {
		raw := wrap(accounts[0], "messages", map[string]any{"messages": []any{m}})
		payloads = append(payloads, raw)
		webhook(accounts[0], raw, 200)
		webhook(accounts[0], raw, 200)
	}
	for field, kind := range map[string]string{"group_lifecycle_update": "group_create", "group_participants_update": "group_participants_add", "group_settings_update": "group_settings_update", "group_status_update": "group_suspend"} {
		raw := wrap(accounts[0], field, map[string]any{"groups": []any{map[string]any{"group_id": "room-group-a", "type": kind, "timestamp": "1755548880"}}})
		payloads = append(payloads, raw)
		webhook(accounts[0], raw, 200)
	}
	// Group receipts identify each participant and can arrive out of order.
	for _, state := range []string{"read", "delivered"} {
		statuses := []any{}
		for _, participant := range []string{"919999999991", "919999999992"} {
			statuses = append(statuses, map[string]any{"id": "wamid." + deliveries[0].ID, "status": state, "timestamp": "1755548890", "recipient_type": "group", "recipient_id": "room-group-a", "recipient_participant_id": participant})
		}
		raw := wrap(accounts[0], "messages", map[string]any{"statuses": statuses})
		payloads = append(payloads, raw)
		webhook(accounts[0], raw, 200)
		webhook(accounts[0], raw, 200)
	}
	t.Run("group_receipts_retain_participants_and_do_not_regress", func(t *testing.T) {
		var status string
		if err := db.QueryRow(ctx, "SELECT status FROM relay_whatsapp_status WHERE account=$1 AND provider_id=$2", accounts[0].ID, "wamid."+deliveries[0].ID).Scan(&status); err != nil || status != "read" {
			t.Fatal("group best-reported status regressed", err)
		}
	})
	webhook(accounts[1], payloads[0], 403)
	t.Run("group_history_separates_participants_and_direct_messages", func(t *testing.T) {
		data := call(tokenA, "whatsapp.execute", whatsapp.Input{Operation: "messages.history", To: "room-group-a"}, false)
		var rows []whatsapp.IncomingMessage
		if json.Unmarshal(data["messages"], &rows) != nil || len(rows) != 2 {
			t.Fatal("group history missing messages")
		}
		for i, row := range rows {
			var actual map[string]any
			json.Unmarshal(row.Payload, &actual)
			if !reflect.DeepEqual(actual, incoming[i]) {
				t.Fatalf("group participant/context lost: %#v", actual)
			}
		}
		private := call(tokenA, "whatsapp.execute", whatsapp.Input{Operation: "messages.history", To: "919999999991"}, false)
		json.Unmarshal(private["messages"], &rows)
		if len(rows) != 1 || rows[0].ProviderID != "wamid.direct.1" {
			t.Fatal("group messages leaked into DM history")
		}
		other := call(tokenB, "whatsapp.execute", whatsapp.Input{Operation: "messages.history", To: "room-group-a"}, false)
		json.Unmarshal(other["messages"], &rows)
		if len(rows) != 0 {
			t.Fatal("cross account group history")
		}
		var count int
		db.QueryRow(ctx, "SELECT count(*) FROM relay_whatsapp_incoming").Scan(&count)
		if count != 3 {
			t.Fatalf("lifecycle projected into messages or replay duplicated: %d", count)
		}
	})
	callbacksCtx, stopCallbacks := context.WithCancel(ctx)
	defer stopCallbacks()
	(&whatsapp.CallbackDispatcher{URL: dsn, Service: wa}).Run(callbacksCtx)
	wait("all group callbacks", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(callbacks["group-a"]) == len(payloads)-2 && len(statusCallbacks["group-a"]) == 4
	})
	t.Run("signed_callbacks_preserve_group_and_lifecycle_events_exactly", func(t *testing.T) {
		mu.Lock()
		defer mu.Unlock()
		if len(callbacks["group-b"]) != 0 || len(statusCallbacks["group-b"]) != 0 {
			t.Fatal("cross account callback")
		}
		seen := map[string]bool{}
		for _, row := range statusCallbacks["group-a"] {
			key := row.Participant + ":" + row.Status
			if row.Recipient != "room-group-a" || row.RecipientType != "group" || row.MessageID != "wamid."+deliveries[0].ID || seen[key] {
				t.Fatalf("invalid compact group receipt: %#v", row)
			}
			seen[key] = true
		}
		for _, participant := range []string{"919999999991", "919999999992"} {
			for _, status := range []string{"read", "delivered"} {
				if !seen[participant+":"+status] {
					t.Fatal("participant receipt missing")
				}
			}
		}
		remaining := map[string]int{}
		canonical := func(raw []byte) string {
			var data any
			json.Unmarshal(raw, &data)
			encoded, _ := json.Marshal(data)
			return string(encoded)
		}
		for _, raw := range payloads[:len(payloads)-2] {
			remaining[canonical(raw)]++
		}
		for _, raw := range callbacks["group-a"] {
			remaining[canonical(raw)]--
		}
		for _, count := range remaining {
			if count != 0 {
				t.Fatal("callback dropped, changed or duplicated a group or participant status payload")
			}
		}
	})
}
