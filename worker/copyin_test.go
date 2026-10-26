package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/filestore"
)

func newCopyInTestWorker(t *testing.T) (*worker, string, string) {
	t.Helper()
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	stagingDir := filepath.Join(root, "staging")
	if err := os.Mkdir(storeDir, 0o755); err != nil {
		t.Fatalf("Mkdir store: %v", err)
	}
	if err := os.Mkdir(stagingDir, 0o711); err != nil {
		t.Fatalf("Mkdir staging: %v", err)
	}
	id := "ABCDEFGH"
	if err := os.WriteFile(filepath.Join(storeDir, id), []byte("input"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return &worker{
		fs:               filestore.NewFileLocalStore(storeDir),
		copyInStagingDir: stagingDir,
	}, id, stagingDir
}

func TestPrepareCopyInLinksCachedFileAndDeduplicatesWithinRequest(t *testing.T) {
	w, id, stagingDir := newCopyInTestWorker(t)
	session := newCopyInSession(stagingDir)

	files, symlinks, err := w.prepareCopyIn(map[string]CmdFile{
		"input": &CachedFile{FileID: id},
	}, nil, session)
	if err != nil {
		t.Fatalf("prepareCopyIn: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no copied files, got %v", files)
	}
	target := symlinks["input"]
	if !strings.HasPrefix(target, copyInMountPath+"/") {
		t.Fatalf("unexpected container target %q", target)
	}

	_, secondSymlinks, err := w.prepareCopyIn(map[string]CmdFile{
		"answer": &CachedFile{FileID: id},
	}, nil, session)
	if err != nil {
		t.Fatalf("second prepareCopyIn: %v", err)
	}
	if secondSymlinks["answer"] != target {
		t.Fatalf("same cached file was not deduplicated: %q != %q", secondSymlinks["answer"], target)
	}

	hostTarget := filepath.Join(stagingDir, strings.TrimPrefix(target, copyInMountPath+"/"))
	if _, err := os.Stat(hostTarget); err != nil {
		t.Fatalf("staging link missing: %v", err)
	}
	session.close()
	if _, err := os.Stat(hostTarget); !os.IsNotExist(err) {
		t.Fatalf("staging link survived session cleanup: %v", err)
	}
}

func TestPrepareCopyInForceCopyReturnsOpenedSource(t *testing.T) {
	w, id, stagingDir := newCopyInTestWorker(t)
	session := newCopyInSession(stagingDir)
	defer session.close()

	files, symlinks, err := w.prepareCopyIn(map[string]CmdFile{
		"prog": &CachedFile{FileID: id, ForceCopy: true},
	}, nil, session)
	if err != nil {
		t.Fatalf("prepareCopyIn: %v", err)
	}
	if len(symlinks) != 0 {
		t.Fatalf("forceCopy unexpectedly created symlinks: %v", symlinks)
	}
	if _, ok := files["prog"].(*envexec.FileOpened); !ok {
		t.Fatalf("forceCopy source is %T, want *envexec.FileOpened", files["prog"])
	}
	if session.hostDir != "" {
		t.Fatalf("forceCopy unexpectedly created staging dir %q", session.hostDir)
	}
}

func TestPrepareCopyInPreservesRequestedSymlinkConflict(t *testing.T) {
	w, id, stagingDir := newCopyInTestWorker(t)
	session := newCopyInSession(stagingDir)
	defer session.close()

	files, symlinks, err := w.prepareCopyIn(
		map[string]CmdFile{"same": &CachedFile{FileID: id}},
		map[string]string{"same": "/tmp/target"},
		session,
	)
	if err != nil {
		t.Fatalf("prepareCopyIn: %v", err)
	}
	if files["same"] == nil {
		t.Fatal("expected ordinary copy to preserve the later symlink conflict")
	}
	if symlinks["same"] != "/tmp/target" {
		t.Fatalf("requested symlink changed to %q", symlinks["same"])
	}
}

func TestPrepareCopyInPreservesRequestedSymlinkHierarchyConflict(t *testing.T) {
	for _, tc := range []struct {
		name        string
		copyInPath  string
		symlinkPath string
	}{
		{name: "symlink ancestor", copyInPath: "dir/file", symlinkPath: "dir"},
		{name: "copyIn ancestor", copyInPath: "dir", symlinkPath: "dir/file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, id, stagingDir := newCopyInTestWorker(t)
			session := newCopyInSession(stagingDir)
			defer session.close()

			files, symlinks, err := w.prepareCopyIn(
				map[string]CmdFile{tc.copyInPath: &CachedFile{FileID: id}},
				map[string]string{tc.symlinkPath: "/tmp/target"},
				session,
			)
			if err != nil {
				t.Fatalf("prepareCopyIn: %v", err)
			}
			if files[tc.copyInPath] == nil {
				t.Fatal("expected ordinary copy for hierarchy conflict")
			}
			if symlinks[tc.symlinkPath] != "/tmp/target" {
				t.Fatalf("requested symlink changed to %q", symlinks[tc.symlinkPath])
			}
		})
	}
}
