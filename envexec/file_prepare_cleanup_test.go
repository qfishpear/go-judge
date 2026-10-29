package envexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareCmdFdFailureRemovesPipeBuffer(t *testing.T) {
	dir := t.TempDir()
	newFile := func() (*os.File, error) {
		return os.CreateTemp(dir, "pipe")
	}
	_, _, err := prepareCmdFd(&Cmd{
		Files: []File{
			&FileCollector{Name: "stdout", Limit: 1024, Pipe: true},
			&FileInput{Path: filepath.Join(dir, "missing")},
		},
	}, 2, newFile)
	if err == nil {
		t.Fatal("expected prepare to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("store dir left %v", names)
	}
}
