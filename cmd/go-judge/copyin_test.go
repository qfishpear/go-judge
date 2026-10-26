//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestNewCopyInStagingCleansStaleEntriesAndDisablesListing(t *testing.T) {
	oldLogger := logger
	logger = zap.NewNop()
	t.Cleanup(func() { logger = oldLogger })

	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stagingDir := storeDir + ".copyin-links"
	if err := os.Mkdir(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "stale"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, cleanup := newCopyInStaging(storeDir)
	if got != stagingDir || cleanup == nil {
		t.Fatalf("newCopyInStaging() path = %q, cleanup nil = %v", got, cleanup == nil)
	}
	t.Cleanup(func() { _ = cleanup() })
	if _, err := os.Stat(filepath.Join(stagingDir, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale entry was not removed: %v", err)
	}
	info, err := os.Stat(stagingDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o711 {
		t.Fatalf("staging permissions = %#o, want 0711", got)
	}
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("hard-link probe leaked into file store: %v", entries)
	}
}
