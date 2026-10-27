package main

import (
	"strings"
	"testing"
)

func TestProviderIDsShareRunLabelWithoutReusingIdentity(t *testing.T) {
	t.Setenv("RELAY_SIM_RUN_LABEL", "R0425-0051-0a")
	seen := map[string]bool{}
	for range 1000 {
		id := providerID("wamid.sim")
		if !strings.HasPrefix(id, "wamid.sim.r0425-0051-0a.") || seen[id] {
			t.Fatalf("unexpected or duplicate provider ID: %s", id)
		}
		seen[id] = true
	}
}
