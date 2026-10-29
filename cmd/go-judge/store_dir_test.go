package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanExplicitFileStoreDir(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "store")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "a"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cleanExplicitFileStoreDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("dir should remain, stat=%v err=%v", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries left: %v", entries)
	}
}

func TestCleanExplicitFileStoreDirRejectsShallowPaths(t *testing.T) {
	for _, dir := range []string{"relative/store", "/", "/tmp", "/dev/shm", "/home/fish"} {
		if err := cleanExplicitFileStoreDir(dir); err == nil {
			t.Fatalf("expected %q to be rejected", dir)
		}
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "a", "b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cleanExplicitFileStoreDir(dir); err != nil {
		t.Fatal(err)
	}
}
