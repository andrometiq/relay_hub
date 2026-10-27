package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// A provider acknowledges acceptance only after its state has reached disk.
type providerState[T any] struct {
	mu    sync.Mutex
	path  string
	value T
}

func openProviderState[T any](path string, initial T) (*providerState[T], error) {
	s := &providerState[T]{path: path, value: initial}
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &s.value)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, errors.New("cannot load provider state")
	}
	return s, nil
}

func (s *providerState[T]) change(fn func(*T) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(s.value)
	if err != nil {
		return err
	}
	var next T
	if err = json.Unmarshal(raw, &next); err != nil {
		return err
	}
	if err = fn(&next); err != nil {
		return err
	}
	raw, err = json.Marshal(next)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".provider-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	// Publish the renamed state even if syncing the directory fails.
	s.value = next
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *providerState[T]) snapshot() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.value)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
