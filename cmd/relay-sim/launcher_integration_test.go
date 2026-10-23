package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLauncherPostgres(t *testing.T) {
	dsn := os.Getenv("RELAY_SIM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires relay_hub_sim and RELAY_SIM_TEST_DATABASE_URL")
	}
	if err := simulationDSN(dsn); err != nil {
		t.Fatal(err)
	}
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
		command = exec.CommandContext(ctx, binary, "-test.run=^TestLauncherProcess$", "--", "--addr", hubAddr, "--meta-addr", metaAddr, "--core-origin", core.URL, mode)
		command.Env = append(os.Environ(), "RELAY_SIM_CHILD=1", "DATABASE_URL="+dsn, "RELAY_SIM_CONTROL_TOKEN="+controlToken)
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
			t.Fatalf("control %s status %d", path, res.StatusCode)
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
	defer lock.Unlock()
	if fmt.Sprint(batches) != "[500 500 251]" {
		t.Fatalf("successful receipt batches %v", batches)
	}
}
