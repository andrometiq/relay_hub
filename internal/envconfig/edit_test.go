package envconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetReplacesAndAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("# keep\nRELAY_ADDR=127.0.0.1:1\nDATABASE_URL=\n"), 0644)
	if e := Set(p, "DATABASE_URL", "postgresql://u:p@h/db"); e != nil {
		t.Fatal(e)
	}
	if e := Set(p, "RELAY_PUBLIC_ORIGIN", "https://relay.example.com"); e != nil {
		t.Fatal(e)
	}
	data, _ := os.ReadFile(p)
	want := "# keep\nRELAY_ADDR=127.0.0.1:1\nDATABASE_URL=postgresql://u:p@h/db\nRELAY_PUBLIC_ORIGIN=https://relay.example.com\n"
	if string(data) != want {
		t.Fatalf("unexpected file %q", data)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe mode")
	}
}

func TestSetRejectsInvalidInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("RELAY_ADDR=x\n"), 0600)
	for _, c := range [][2]string{{"PATH", "x"}, {"RELAY_ADDR", "a\nb"}, {"RELAY_ADDR", "'open"}} {
		if Set(p, c[0], c[1]) == nil {
			t.Fatalf("accepted %q", c)
		}
	}
	data, _ := os.ReadFile(p)
	if string(data) != "RELAY_ADDR=x\n" {
		t.Fatal("changed file on invalid input")
	}
}

func TestFillSecretsKeepsExistingValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("RELAY_CREDENTIAL_KEY=existing\nRELAY_MCP_TOKEN=\n"), 0600)
	t.Setenv("RELAY_EMAIL_WEBHOOK_SECRET", "fromenv")
	added, e := FillSecrets(p)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(added, ",") != "RELAY_MCP_TOKEN,RELAY_TELEGRAM_WEBHOOK_SECRET,RELAY_EMAIL_WEBHOOK_SECRET" {
		t.Fatalf("unexpected keys %v", added)
	}
	data, _ := os.ReadFile(p)
	values, _ := Parse(string(data))
	if values["RELAY_CREDENTIAL_KEY"] != "existing" || values["RELAY_EMAIL_WEBHOOK_SECRET"] != "fromenv" || len(values["RELAY_MCP_TOKEN"]) != 64 {
		t.Fatal("secret values not preserved or generated")
	}
	again, _ := FillSecrets(p)
	second, _ := os.ReadFile(p)
	if len(again) != 0 || string(second) != string(data) {
		t.Fatal("second run changed the file")
	}
}
