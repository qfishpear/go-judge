package filestore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/criyle/go-judge/envexec"
)

func TestFileLocalStoreRejectsTraversalIDs(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileLocalStore(dir)

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	if _, file := fs.Get("../../" + filepath.Base(outside)); file != nil {
		t.Fatalf("expected traversal id to be rejected")
	}
	if ok := fs.Remove("../../" + filepath.Base(outside)); ok {
		t.Fatalf("expected traversal id remove to fail")
	}
	if _, err := fs.PrepareCopyIn("../../"+filepath.Base(outside), ""); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("expected traversal copy-in id to be rejected, got %v", err)
	}
}

func TestFileLocalStoreAcceptsGeneratedLikeIDs(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileLocalStore(dir)

	id := "ABCDEFGH"
	path := filepath.Join(dir, id)
	if err := os.WriteFile(path, []byte("ok"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	name, file := fs.Get(id)
	if file == nil {
		t.Fatalf("expected file to be returned")
	}
	if name != id {
		t.Fatalf("unexpected name %q", name)
	}
}

func TestFileLocalStoreRemoveReportsDeleteFailure(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileLocalStore(dir)

	id := "ABCDEFGH"
	path := filepath.Join(dir, id)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	if ok := fs.Remove(id); ok {
		t.Fatalf("expected remove to report failure for non-empty directory")
	}
}

func TestFileLocalStorePrepareCopyInHardLinksUnderLock(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileLocalStore(dir)
	id := "ABCDEFGH"
	source := filepath.Join(dir, id)
	if err := os.WriteFile(source, []byte("shared"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	target := filepath.Join(dir, "linked")
	result, err := fs.PrepareCopyIn(id, target)
	if err != nil {
		t.Fatalf("PrepareCopyIn error: %v", err)
	}
	if !result.Linked || result.File != nil {
		t.Fatalf("expected linked result, got %+v", result)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatalf("Stat source: %v", err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatal("source and target do not share an inode")
	}

	if !fs.Remove(id) {
		t.Fatal("Remove failed")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "shared" {
		t.Fatalf("hard link did not survive source removal: content=%q err=%v", got, err)
	}
}

func TestFileLocalStorePrepareCopyInOpensPrivateCopySource(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileLocalStore(dir)
	id := "ABCDEFGH"
	if err := os.WriteFile(filepath.Join(dir, id), []byte("copy"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	result, err := fs.PrepareCopyIn(id, "")
	if err != nil {
		t.Fatalf("PrepareCopyIn error: %v", err)
	}
	if result.Linked || result.File == nil {
		t.Fatalf("expected opened fallback, got %+v", result)
	}
	reader, err := envexec.FileToReader(result.File)
	if err != nil {
		t.Fatalf("FileToReader error: %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "copy" {
		t.Fatalf("unexpected opened content %q: %v", got, err)
	}
}

func TestFileLocalStorePrepareCopyInFallsBackWhenLinkFails(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileLocalStore(dir)
	id := "ABCDEFGH"
	if err := os.WriteFile(filepath.Join(dir, id), []byte("fallback"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	result, err := fs.PrepareCopyIn(id, filepath.Join(dir, "missing", "link"))
	if err != nil {
		t.Fatalf("PrepareCopyIn error: %v", err)
	}
	if result.Linked || result.File == nil || result.FallbackReason == CopyInFallbackNone {
		t.Fatalf("expected fallback result, got %+v", result)
	}
	reader, err := envexec.FileToReader(result.File)
	if err != nil {
		t.Fatalf("FileToReader error: %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "fallback" {
		t.Fatalf("unexpected fallback content %q: %v", got, err)
	}
}
