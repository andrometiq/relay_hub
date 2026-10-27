package main

import (
	"crypto/rand"
	"os"
	"strings"
	"time"
)

// Provider acknowledgements need a fresh identity even after a Hub restart.
func providerID(kind string) string {
	label := os.Getenv("RELAY_SIM_RUN_LABEL")
	if label == "" {
		label = time.Now().UTC().Format("R0102-1504")
	}
	return kind + "." + strings.ToLower(label) + "." + rand.Text()[:10]
}
