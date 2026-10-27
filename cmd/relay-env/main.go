package main

import (
	"fmt"
	"github.com/andrometiq/relay_hub/internal/envconfig"
	"log"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		if err := envconfig.Ensure(".env"); err != nil {
			log.Fatal(err)
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
