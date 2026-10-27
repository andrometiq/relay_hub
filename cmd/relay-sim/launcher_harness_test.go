package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// launcher runs relay-sim in a child process on free loopback ports and
// drives it through its control API and the Hub MCP endpoint.
type launcher struct {
	t                      *testing.T
	ctx                    context.Context
	dsn, core              string
	hubAddr, metaAddr      string
	smtpAddr, imapAddr     string
	stateDir, controlToken string
	command                *exec.Cmd
	logs                   bytes.Buffer
}

func newLauncher(t *testing.T, ctx context.Context, dsn, core string) *launcher {
	free := func() string {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
		return l.Addr().String()
	}
	l := &launcher{t: t, ctx: ctx, dsn: dsn, core: core, hubAddr: free(), metaAddr: free(), smtpAddr: free(), imapAddr: free(), stateDir: t.TempDir(), controlToken: strings.Repeat("test-control-", 4)}
	t.Cleanup(func() {
		l.stop()
		if t.Failed() {
			t.Log(l.logs.String())
		}
	})
	return l
}

func (l *launcher) stop() {
	if l.command != nil {
		l.command.Process.Signal(os.Interrupt)
		l.command.Wait()
		l.command = nil
	}
}

func (l *launcher) start(resume bool) {
	binary, e := os.Executable()
	if e != nil {
		l.t.Fatal(e)
	}
	mode := "--migrate"
	if resume {
		mode = "--resume"
	}
	l.command = exec.CommandContext(l.ctx, binary, "-test.run=^TestLauncherProcess$", "--", "--addr", l.hubAddr, "--meta-addr", l.metaAddr, "--core-origin", l.core, "--smtp-addr", l.smtpAddr, "--imap-addr", l.imapAddr, "--state-dir", l.stateDir, mode)
	l.command.Env = append(os.Environ(), "RELAY_SIM_CHILD=1", "DATABASE_URL="+l.dsn, "RELAY_SIM_CONTROL_TOKEN="+l.controlToken, "RELAY_CREDENTIAL_KEY="+strings.Repeat("a", 64), "RELAY_BULK_UPDATE_WINDOW=1s")
	l.command.Stdout = &l.logs
	l.command.Stderr = &l.logs
	if e = l.command.Start(); e != nil {
		l.t.Fatal(e)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, e := http.Get("http://" + l.hubAddr + "/readyz")
		if e == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	l.t.Fatal("launcher did not become ready")
}

// call posts body (or GETs when nil) to a control path and decodes the reply.
func (l *launcher) call(path string, body any) map[string]any {
	l.t.Helper()
	var reader io.Reader
	method := "GET"
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
		method = "POST"
	}
	req, _ := http.NewRequestWithContext(l.ctx, method, "http://"+l.metaAddr+"/_sim/"+path, reader)
	req.Header.Set("Authorization", "Bearer "+l.controlToken)
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		l.t.Fatal(e)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		l.t.Fatalf("control %s status %d: %s", path, res.StatusCode, body)
	}
	var result map[string]any
	if e = json.NewDecoder(res.Body).Decode(&result); e != nil {
		l.t.Fatal(e)
	}
	return result
}

// wait polls the simulation state until condition holds.
func (l *launcher) wait(condition func(map[string]any) bool) map[string]any {
	l.t.Helper()
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		s := l.call("state", nil)
		if condition(s) {
			return s
		}
		time.Sleep(40 * time.Millisecond)
	}
	l.t.Fatal("timed out waiting for durable launcher state")
	return nil
}

// mcp calls one Hub MCP tool and fails the test on a tool error.
func (l *launcher) mcp(token, name string, args any, want int) map[string]any {
	l.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	res := l.mcpPost(token, raw)
	defer res.Body.Close()
	if res.StatusCode != want {
		l.t.Fatalf("MCP status %d want %d", res.StatusCode, want)
	}
	if want != 200 {
		return nil
	}
	var result map[string]any
	json.NewDecoder(res.Body).Decode(&result)
	if result["error"] != nil {
		l.t.Fatalf("MCP %s: %v", name, result["error"])
	}
	if v, ok := result["result"].(map[string]any); ok && v["isError"] == true {
		l.t.Fatalf("MCP %s: %v", name, v)
	}
	return result
}

// mcpError calls one Hub MCP tool that must fail and returns its result.
func (l *launcher) mcpError(token, name string, args any) string {
	l.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	res := l.mcpPost(token, raw)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	var result struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &result) != nil || (result.Error == nil && !result.Result.IsError) {
		l.t.Fatalf("MCP %s did not fail: %s", name, body)
	}
	return string(body)
}

func (l *launcher) mcpPost(token string, raw []byte) *http.Response {
	req, _ := http.NewRequestWithContext(l.ctx, "POST", "http://"+l.hubAddr+"/mcp", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		l.t.Fatal(e)
	}
	return res
}
