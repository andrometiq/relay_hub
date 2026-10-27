package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderStateFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := openProviderState(path, struct{ Count int }{})
	if err != nil {
		t.Fatal(err)
	}
	if err = state.change(func(s *struct{ Count int }) error { s.Count++; return nil }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state permissions", err)
	}
	state.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if err = state.change(func(s *struct{ Count int }) error { s.Count++; return nil }); err == nil {
		t.Fatal("acknowledged unsaved state")
	}
	if state.snapshot().Count != 1 {
		t.Fatal("failed write changed live state")
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = openProviderState(path, struct{ Count int }{}); err == nil {
		t.Fatal("corrupt state silently reset")
	}
}
