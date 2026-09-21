// Package envconfig loads literal project configuration without shell evaluation.
package envconfig

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var Keys = []string{"RELAY_PG_PORT", "DATABASE_URL", "RELAY_ADDR", "RELAY_MCP_TOKEN", "GOMAXPROCS", "GOFLAGS"}

func Parse(data string) (map[string]string, error) {
	allowed := map[string]bool{}
	for _, k := range Keys {
		allowed[k] = true
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(data))
	line := 0
	for scanner.Scan() {
		line++
		s := strings.TrimSpace(scanner.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || !allowed[k] {
			return nil, fmt.Errorf("invalid .env key at line %d", line)
		}
		if _, exists := values[k]; exists {
			return nil, fmt.Errorf("duplicate .env key at line %d", line)
		}
		if strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'") {
			q := v[0]
			if len(v) < 2 || v[len(v)-1] != q {
				return nil, fmt.Errorf("unclosed .env quote at line %d", line)
			}
			v = v[1 : len(v)-1]
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			return nil, fmt.Errorf("invalid .env value at line %d", line)
		}
		values[k] = v
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("cannot parse .env")
	}
	return values, nil
}
func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read project .env; run ./relay install")
	}
	values, err := Parse(string(data))
	if err != nil {
		return err
	}
	for k, v := range values {
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
	if v := os.Getenv("RELAY_PG_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1024 || n > 65535 {
			return fmt.Errorf("RELAY_PG_PORT must be between 1024 and 65535")
		}
	}
	return nil
}
func Ensure(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect .env")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintf(file, "# Local Relay Hub configuration. Never commit this file.\n# Empty DATABASE_URL selects this checkout's private PostgreSQL cluster.\nDATABASE_URL=\nRELAY_PG_PORT=18432\nRELAY_ADDR=127.0.0.1:18081\nRELAY_MCP_TOKEN=%s\nGOMAXPROCS=2\nGOFLAGS=-p=2\n", hex.EncodeToString(token))
	return err
}
