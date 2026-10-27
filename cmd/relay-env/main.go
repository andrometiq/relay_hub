package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/andrometiq/relay_hub/internal/envconfig"
)

// Usage: relay-env [init] | missing KEY... | set KEY (value on stdin) | secrets
func main() {
	if len(os.Args) > 1 {
		if err := edit(os.Args[1], os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		if os.Args[1] != "init" {
			return
		}
	}
	if err := envconfig.Load(".env"); err != nil {
		log.Fatal(err)
	}
	for _, k := range envconfig.Keys {
		if v, ok := os.LookupEnv(k); ok {
			fmt.Printf("%s\x00%s\x00", k, v)
		}
	}
}

func edit(command string, args []string) error {
	switch command {
	case "init":
		return envconfig.Ensure(".env")
	case "missing":
		missing, err := envconfig.Missing(".env", args)
		fmt.Println(strings.Join(missing, " "))
		return err
	case "set":
		if len(args) != 1 {
			return fmt.Errorf("usage: relay-env set KEY")
		}
		value, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && value == "" {
			return fmt.Errorf("value required on stdin")
		}
		return envconfig.Set(".env", args[0], strings.TrimRight(value, "\r\n"))
	case "secrets":
		added, err := envconfig.FillSecrets(".env")
		fmt.Println(strings.Join(added, " "))
		return err
	}
	return fmt.Errorf("unknown relay-env command %q", command)
}
