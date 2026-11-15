//go:build linux

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWalkHashesRegularFilesAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a"), "hi")
	mkdir(t, filepath.Join(root, "sub"))
	writeFile(t, filepath.Join(root, "sub", "b"), "yo")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	res := walkDir(t, root, unlimited())
	if res.status != WorkspaceStatusOK {
		t.Fatalf("status %s", res.status)
	}
	if len(res.files) != 2 || res.files["a"] != sha256.Sum256([]byte("hi")) || res.files["sub/b"] != sha256.Sum256([]byte("yo")) {
		t.Fatalf("files: %+v", res.files)
	}
	if len(res.skipped) != 1 || res.skipped[0].Reason != WorkspaceReasonNotRegular || string(res.skipped[0].Path) != "link" {
		t.Fatalf("skipped: %+v", res.skipped)
	}
}

func TestWalkInvalidDirectoryIsNotEntered(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "\xff")
	mkdir(t, dir)
	writeFile(t, filepath.Join(dir, "a"), strings.Repeat("a", 10))

	wk := &walker{ctx: context.Background(), maxFiles: ^uint32(0), maxEntries: ^uint32(0), maxSize: 5}
	res := walkDir(t, root, wk)
	if res.status != WorkspaceStatusOK || len(res.files) != 0 {
		t.Fatalf("status %s files %+v", res.status, res.files)
	}
	if len(res.skipped) != 1 || res.skipped[0].Reason != WorkspaceReasonInvalidPath || !bytes.Equal(res.skipped[0].Path, []byte{0xff}) {
		t.Fatalf("skipped: %+v", res.skipped)
	}
}

func TestWalkLongPathIsNotEntered(t *testing.T) {
	root := t.TempDir()
	name := strings.Repeat("a", 255)
	dir := root
	for range 4 {
		dir = filepath.Join(dir, name)
		mkdir(t, dir)
	}
	writeFile(t, filepath.Join(dir, "f"), "x")

	res := walkDir(t, root, unlimited())
	want := strings.Join([]string{name, name, name}, "/")
	if len(res.skipped) != 1 || string(res.skipped[0].Path) != want || res.skipped[0].Reason != WorkspaceReasonInvalidPath {
		t.Fatalf("skipped: %d entries", len(res.skipped))
	}
}

func TestWalkLimits(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "d"))
	if err := os.Symlink("x", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "f"), "12345")

	cases := []struct {
		name       string
		maxFiles   uint32
		maxEntries uint32
		maxSize    uint64
		want       WorkspaceStatus
	}{
		{"directories and skips do not consume the file cap", 1, 10, 5, WorkspaceStatusOK},
		{"entry cap", 10, 2, 5, WorkspaceStatusTooManyFiles},
		{"size", 10, 10, 4, WorkspaceStatusTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := walkDir(t, root, &walker{
				ctx:        context.Background(),
				maxFiles:   tc.maxFiles,
				maxEntries: tc.maxEntries,
				maxSize:    tc.maxSize,
			})
			if res.status != tc.want {
				t.Fatalf("status %s, want %s", res.status, tc.want)
			}
		})
	}
}

func TestWalkFileCapCountsEveryRegularFile(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "d"))
	mkdir(t, filepath.Join(root, "d", "nested"))
	writeFile(t, filepath.Join(root, "d", "nested", "a"), "a")
	writeFile(t, filepath.Join(root, "b"), "b")
	if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	within := walkDir(t, root, &walker{ctx: context.Background(), maxFiles: 2, maxEntries: 5, maxSize: ^uint64(0)})
	if within.status != WorkspaceStatusOK || len(within.files) != 2 {
		t.Fatalf("status %s files %d", within.status, len(within.files))
	}
	writeFile(t, filepath.Join(root, "c"), "c")
	over := walkDir(t, root, &walker{ctx: context.Background(), maxFiles: 2, maxEntries: 100, maxSize: ^uint64(0)})
	if over.status != WorkspaceStatusTooManyFiles {
		t.Fatalf("status %s", over.status)
	}
}

func TestWalkStoresOnlyChangedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "same"), "same")
	writeFile(t, filepath.Join(root, "edited"), "new")
	mkdir(t, filepath.Join(root, "was"))
	before := map[string][32]byte{
		"same":   sha256.Sum256([]byte("same")),
		"edited": sha256.Sum256([]byte("old")),
		"was":    sha256.Sum256([]byte("file")),
	}
	var stored []string
	wk := unlimited()
	wk.before = before
	wk.store = func(path string, _ *os.File, _ uint64) (string, error) {
		stored = append(stored, path)
		return "id-" + path, nil
	}

	res := walkDir(t, root, wk)
	if len(stored) != 1 || stored[0] != "edited" {
		t.Fatalf("stored %v", stored)
	}
	if len(res.changed) != 1 || res.changed[0] != (WorkspaceChanged{Path: "edited", FileID: "id-edited", Size: 3}) {
		t.Fatalf("changed %+v", res.changed)
	}
	if len(res.skipped) != 1 || string(res.skipped[0].Path) != "was" || res.skipped[0].Reason != WorkspaceReasonNotRegular {
		t.Fatalf("skipped %+v", res.skipped)
	}
}

func TestWalkOtherFilesystem(t *testing.T) {
	root := t.TempDir()
	mountPoint := filepath.Join(root, "mnt")
	mkdir(t, mountPoint)
	if err := unix.Mount("tmpfs", mountPoint, "tmpfs", 0, "size=1m"); err != nil {
		t.Skipf("tmpfs mount: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(mountPoint, 0) })
	writeFile(t, filepath.Join(mountPoint, "secret"), "nope")
	bind := filepath.Join(root, "bind")
	writeFile(t, bind, "")
	if err := unix.Mount(filepath.Join(mountPoint, "secret"), bind, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(bind, 0) })
	writeFile(t, filepath.Join(root, "ok"), "yes")

	res := walkDir(t, root, unlimited())
	if len(res.files) != 1 || res.files["ok"] != sha256.Sum256([]byte("yes")) {
		t.Fatalf("files: %+v", res.files)
	}
	reasons := map[string]WorkspaceReason{}
	for _, s := range res.skipped {
		reasons[string(s.Path)] = s.Reason
	}
	if len(reasons) != 2 || reasons["mnt"] != WorkspaceReasonOtherFilesystem || reasons["bind"] != WorkspaceReasonOtherFilesystem {
		t.Fatalf("skipped: %+v", res.skipped)
	}
}

func TestWalkCancelled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a"), "x")
	f := openDir(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wk := unlimited()
	wk.ctx = ctx
	if _, err := wk.run(f); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestWalkMode000(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to read past mode 000")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("hidden"), 0000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0755) })
	res := walkDir(t, root, unlimited())
	if res.files["secret"] != sha256.Sum256([]byte("hidden")) {
		t.Fatalf("secret: %+v", res.files)
	}
}

func unlimited() *walker {
	return &walker{
		ctx:        context.Background(),
		maxFiles:   ^uint32(0),
		maxEntries: ^uint32(0),
		maxSize:    ^uint64(0),
	}
}

func walkDir(t *testing.T, root string, wk *walker) *walkResult {
	t.Helper()
	res, err := wk.run(openDir(t, root))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func openDir(t *testing.T, root string) *os.File {
	t.Helper()
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
