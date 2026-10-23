package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Runs the launcher itself in a child process, including its signal shutdown.
func TestLauncherProcess(t *testing.T) {
	if os.Getenv("RELAY_SIM_CHILD") != "1" {
		return
	}
	args := flag.Args()
	flag.CommandLine = flag.NewFlagSet("relay-sim", flag.ExitOnError)
	os.Args = append([]string{"relay-sim"}, args...)
	if err := runSimulation(isolatedSimulationDSN); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLauncherPostgres(t *testing.T) {
	dsn := isolatedLauncherDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	secret := strings.Repeat("test-callback-", 4)
	var lock sync.Mutex
	var batches []int
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if r.Header.Get("Authorization") != "token fixture-key:fixture-secret" || !hmac.Equal([]byte(r.Header.Get("X-Relay-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("callback authentication invalid")
			http.Error(w, "unauthorized", 403)
			return
		}
		var body struct {
			Events   []any `json:"events"`
			Statuses []any `json:"statuses"`
		}
		json.Unmarshal(raw, &body)
		if len(body.Statuses) > 0 {
			lock.Lock()
			batches = append(batches, len(body.Statuses))
			lock.Unlock()
		}
		writeJSON(w, map[string]any{"message": map[string]any{"accepted": len(body.Events) + len(body.Statuses)}})
	}))
	defer core.Close()
	free := func() string {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		addr := l.Addr().String()
		l.Close()
		return addr
	}
	hubAddr, metaAddr := free(), free()
	smtpAddr, imapAddr := free(), free()
	stateDir := t.TempDir()
	hubURL, metaURL := "http://"+hubAddr, "http://"+metaAddr
	controlToken := strings.Repeat("test-control-", 4)
	var command *exec.Cmd
	var logs bytes.Buffer
	stop := func() {
		if command != nil {
			command.Process.Signal(os.Interrupt)
			command.Wait()
			command = nil
		}
	}
	defer func() {
		stop()
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	start := func(resume bool) {
		binary, e := os.Executable()
		if e != nil {
			t.Fatal(e)
		}
		mode := "--migrate"
		if resume {
			mode = "--resume"
		}
		command = exec.CommandContext(ctx, binary, "-test.run=^TestLauncherProcess$", "--", "--addr", hubAddr, "--meta-addr", metaAddr, "--core-origin", core.URL, "--smtp-addr", smtpAddr, "--imap-addr", imapAddr, "--state-dir", stateDir, mode)
		command.Env = append(os.Environ(), "RELAY_SIM_CHILD=1", "DATABASE_URL="+dsn, "RELAY_SIM_CONTROL_TOKEN="+controlToken, "RELAY_CREDENTIAL_KEY="+strings.Repeat("a", 64))
		command.Stdout = &logs
		command.Stderr = &logs
		if e = command.Start(); e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			res, e := http.Get(hubURL + "/readyz")
			if e == nil {
				res.Body.Close()
				if res.StatusCode == 200 {
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("launcher did not become ready")
	}
	call := func(path string, body any) map[string]any {
		t.Helper()
		var reader io.Reader
		method := "GET"
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
			method = "POST"
		}
		req, _ := http.NewRequestWithContext(ctx, method, metaURL+"/_sim/"+path, reader)
		req.Header.Set("Authorization", "Bearer "+controlToken)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("control %s status %d: %s", path, res.StatusCode, body)
		}
		var result map[string]any
		if e = json.NewDecoder(res.Body).Decode(&result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	configure := func() string {
		return call("provision", map[string]any{"site": core.URL, "api_key": "fixture-key", "api_secret": "fixture-secret", "callback_secret": secret})["token"].(string)
	}
	wait := func(condition func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.Now().Add(35 * time.Second)
		for time.Now().Before(deadline) {
			s := call("state", nil)
			if condition(s) {
				return s
			}
			time.Sleep(40 * time.Millisecond)
		}
		t.Fatal("timed out waiting for durable launcher state")
		return nil
	}
	start(false)
	token := configure()
	mcp := func(token, name string, args any, want int) map[string]any {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, _ := http.NewRequestWithContext(ctx, "POST", hubURL+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("MCP status %d want %d", res.StatusCode, want)
		}
		if want != 200 {
			return nil
		}
		var result map[string]any
		json.NewDecoder(res.Body).Decode(&result)
		if result["error"] != nil {
			t.Fatalf("MCP %s: %v", name, result["error"])
		}
		if v, ok := result["result"].(map[string]any); ok && v["isError"] == true {
			t.Fatalf("MCP %s: %v", name, v)
		}
		return result
	}
	mcp("invalid-token", "account.get", map[string]any{}, 401)
	binding := mcp(token, "account.get", map[string]any{}, 200)
	if binding["result"].(map[string]any)["structuredContent"].(map[string]any)["site"] != core.URL {
		t.Fatal("account binding unavailable")
	}
	requestKey := fmt.Sprintf("launcher-%d", time.Now().UnixNano())
	args := map[string]any{"message": map[string]any{"key": requestKey, "message": map[string]any{"operation": "messages.send", "to": "919999999999", "body": "Launcher simulation"}}}
	mcp(token, "whatsapp.send", args, 200)
	mcp(token, "whatsapp.send", args, 200)
	wait(func(s map[string]any) bool {
		for _, d := range s["deliveries"].([]any) {
			row := d.(map[string]any)
			if row["request_key"] == requestKey {
				return row["state"] == "submitted"
			}
		}
		return false
	})
	state := call("state", nil)
	posts := 0
	for _, r := range state["records"].([]any) {
		row := r.(map[string]any)
		if row["kind"] == "graph" && row["method"] == "POST" && row["path"] == "/v25.0/110/messages" {
			posts++
		}
	}
	if posts != 1 {
		t.Fatalf("provider posts %d want 1", posts)
	}
	call("faults", map[string]any{"target": "status", "faults": []string{"503", "wrong_ack"}, "hold": true})
	statuses := []any{}
	for i := 0; i < 1251; i++ {
		statuses = append(statuses, map[string]any{"id": fmt.Sprintf("wamid.%s.%d", requestKey, i/3), "status": []string{"sent", "delivered", "read"}[i%3], "recipient_id": "919999999999", "timestamp": fmt.Sprint(time.Now().Unix() + int64(i%3))})
	}
	value := map[string]any{"metadata": map[string]any{"phone_number_id": "110"}, "statuses": statuses}
	payload := map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "220", "changes": []any{map[string]any{"field": "messages", "value": value}}}}}
	if call("emit", map[string]any{"payload": payload})["status"] != float64(200) {
		t.Fatal("signed ingress failed")
	}
	state = wait(func(s map[string]any) bool {
		for _, r := range s["records"].([]any) {
			if r.(map[string]any)["fault"] == "wrong_ack" {
				return true
			}
		}
		return false
	})
	var eventID float64
	for _, e := range state["events"].([]any) {
		row := e.(map[string]any)
		if row["lane"] == "status" && row["id"].(float64) > eventID {
			eventID = row["id"].(float64)
		}
	}
	for _, c := range state["callbacks"].([]any) {
		row := c.(map[string]any)
		if row["event_id"] == eventID && (row["acknowledged"] != false || row["status_offset"] != float64(0)) {
			t.Fatal("failed ack advanced durable offset")
		}
	}
	stop()
	start(true)
	mcp(token, "account.get", map[string]any{}, 200)
	wait(func(s map[string]any) bool {
		for _, c := range s["callbacks"].([]any) {
			row := c.(map[string]any)
			if row["event_id"] == eventID && row["lane"] == "status" {
				return row["acknowledged"] == true
			}
		}
		return false
	})
	lock.Lock()
	if fmt.Sprint(batches) != "[500 500 251]" {
		t.Fatalf("successful receipt batches %v", batches)
	}
	lock.Unlock()
	exerciseChannelLauncher(t, call, mcp, wait, token, stop, func() { start(true) })
}

func isolatedSimulationDSN(dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port != 18433 || cfg.Database != "relay_hub_v1_test" || !strings.HasPrefix(cfg.RuntimeParams["search_path"], "relay_sim_test_") {
		return errors.New("launcher tests require an isolated relay_hub_v1_test schema")
	}
	for _, f := range cfg.Fallbacks {
		if f.Host != cfg.Host || f.Port != cfg.Port {
			return errors.New("nonlocal test fallback")
		}
	}
	return nil
}
func isolatedLauncherDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires RELAY_TEST_DATABASE_URL on local relay_hub_v1_test")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port != 18433 || cfg.Database != "relay_hub_v1_test" {
		t.Fatal("unsafe launcher test database")
	}
	schema := fmt.Sprintf("relay_sim_test_%d", time.Now().UnixNano())
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		conn.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		conn.Close(context.Background())
	})
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema
	}
	if err := isolatedSimulationDSN(dsn); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func exerciseChannelLauncher(t *testing.T, call func(string, any) map[string]any, mcp func(string, string, any, int) map[string]any, wait func(func(map[string]any) bool) map[string]any, token string, stop, start func()) {
	t.Helper()
	telegramArgs := func(key string) map[string]any {
		return map[string]any{"request_key": key, "operation": "text", "chat_id": 42, "text": "Launcher Telegram 125.50", "message_thread_id": 7}
	}
	call("telegram/faults", map[string]any{"faults": []string{"429"}})
	mcp(token, "telegram.message", telegramArgs("launcher-telegram"), 200)
	mcp(token, "telegram.message", telegramArgs("launcher-telegram"), 200)
	wait(func(s map[string]any) bool {
		for _, v := range s["channel_deliveries"].([]any) {
			r := v.(map[string]any)
			if r["request_key"] == "launcher-telegram" {
				return r["state"] == "submitted"
			}
		}
		return false
	})
	providers := call("providers", nil)
	if len(providers["telegram"].(map[string]any)["messages"].([]any)) != 1 {
		t.Fatal("Telegram retry or deduplication failed")
	}
	update := map[string]any{"update_id": 901, "message": map[string]any{"message_id": 7, "date": time.Now().Unix(), "chat": map[string]any{"id": 42, "type": "private"}, "from": map[string]any{"id": 43, "first_name": "Fixture"}, "text": "Authenticated incoming"}}
	if call("telegram/emit", map[string]any{"payload": update, "invalid_signature": true})["status"] != float64(400) {
		t.Fatal("invalid Telegram secret accepted")
	}
	for i := 0; i < 2; i++ {
		if call("telegram/emit", map[string]any{"payload": update})["status"] != float64(200) {
			t.Fatal("Telegram ingress rejected")
		}
	}
	mailArgs := map[string]any{"request_key": "launcher-email", "to": []string{"to@example.invalid"}, "bcc": []string{"hidden@example.invalid"}, "subject": "Launcher email", "body": "Total 125.50"}
	mcp(token, "email.send", mailArgs, 200)
	wait(func(s map[string]any) bool {
		for _, v := range s["channel_deliveries"].([]any) {
			r := v.(map[string]any)
			if r["request_key"] == "launcher-email" {
				return r["state"] == "submitted"
			}
		}
		return false
	})
	raw := "From: sender@example.invalid\r\nTo: inbox@sim.invalid\r\nSubject: Local ingress\r\nMessage-ID: <launcher-in@example.invalid>\r\n\r\nAmount 125.50"
	call("email/emit", map[string]any{"raw": raw, "authentication": "pass", "uid": 5})
	if call("email/emit", map[string]any{"raw": raw, "authentication": "pass", "id": "webhook-email", "webhook": true, "invalid_signature": true})["status"] != float64(400) {
		t.Fatal("invalid email secret accepted")
	}
	if call("email/emit", map[string]any{"raw": raw, "authentication": "pass", "id": "webhook-email", "webhook": true})["status"] != float64(200) {
		t.Fatal("email webhook rejected")
	}
	state := wait(func(s map[string]any) bool {
		if len(s["channel_events"].([]any)) != 3 || len(s["channel_callbacks"].([]any)) != 3 {
			return false
		}
		for _, v := range s["channel_callbacks"].([]any) {
			if v.(map[string]any)["acknowledged"] != true {
				return false
			}
		}
		return true
	})
	for _, v := range state["channel_events"].([]any) {
		row := v.(map[string]any)
		if row["channel"] == "Email" {
			payload := row["payload"].(map[string]any)
			if payload["authentication"].(map[string]any)["status"] != "pass" {
				t.Fatal("email ingress lost trusted authentication")
			}
		}
	}
	before := call("providers", nil)
	sent := before["email"].(map[string]any)["sent"].([]any)
	if len(sent) != 1 {
		t.Fatal("SMTP acceptance missing")
	}
	// Restart with persisted provider identities and Hub delivery journals.
	stop()
	start()
	mcp(token, "telegram.message", telegramArgs("launcher-telegram"), 200)
	mcp(token, "email.send", mailArgs, 200)
	after := call("providers", nil)
	if len(after["telegram"].(map[string]any)["messages"].([]any)) != 1 || len(after["email"].(map[string]any)["sent"].([]any)) != 1 {
		t.Fatal("restart caused a provider resend")
	}
	state = call("state", nil)
	for _, v := range state["channel_deliveries"].([]any) {
		r := v.(map[string]any)
		if r["request_key"] == "launcher-email" {
			id := r["result"].(map[string]any)["message_id"]
			call("email/bounce", map[string]any{"message_id": id, "recipient": "to@example.invalid", "webhook": true, "id": "bounce-1"})
		}
	}
	wait(func(s map[string]any) bool {
		for _, v := range s["records"].([]any) {
			r := v.(map[string]any)
			if r["kind"] == "status" && strings.HasSuffix(r["path"].(string), ".receive_channel_statuses") {
				raw, _ := json.Marshal(r["body"])
				if bytes.Contains(raw, []byte("bounced")) {
					return true
				}
			}
		}
		return false
	})
}
