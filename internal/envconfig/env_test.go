package envconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLiteralAndQuotedValues(t *testing.T) {
	v, e := Parse("# comment\nRELAY_MCP_TOKEN='$(touch /tmp/not-executed)'\nDATABASE_URL=\"host=some path\"\n")
	if e != nil {
		t.Fatal(e)
	}
	if v["RELAY_MCP_TOKEN"] != "$(touch /tmp/not-executed)" || v["DATABASE_URL"] != "host=some path" {
		t.Fatal("values changed")
	}
}
func TestInvalidConfig(t *testing.T) {
	for _, s := range []string{"PATH=bad", "RELAY_ADDR=x\nRELAY_ADDR=y", "RELAY_ADDR='oops", "invalid"} {
		if _, e := Parse(s); e == nil {
			t.Fatalf("accepted malformed config %q", s)
		}
	}
}
func TestEnsurePreservesConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if e := Ensure(p); e != nil {
		t.Fatal(e)
	}
	first, _ := os.ReadFile(p)
	Ensure(p)
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Fatal("replaced existing configuration")
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe mode")
	}
	values, e := Parse(string(first))
	if e != nil || len(values["RELAY_MCP_TOKEN"]) != 64 {
		t.Fatal("missing generated token")
	}
}
func TestEnvironmentTakesPrecedence(t *testing.T) {
	t.Setenv("RELAY_ADDR", "127.0.0.1:9999")
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("RELAY_ADDR=127.0.0.1:1111"), 0600)
	if e := Load(p); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("RELAY_ADDR") != "127.0.0.1:9999" {
		t.Fatal("overrode environment")
	}
}

func TestRejectInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "65536", "oops", "-1"} {
		t.Setenv("RELAY_PG_PORT", port)
		p := filepath.Join(t.TempDir(), ".env")
		os.WriteFile(p, []byte("# empty"), 0600)
		if Load(p) == nil {
			t.Fatalf("accepted invalid port %s", port)
		}
	}
}
