package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/criyle/go-judge/envexec"
)

func TestValidWorkspacePathCases(t *testing.T) {
	f, err := os.Open("testdata/path-cases.txt")
	if err != nil {
		t.Fatalf("open cases: %v", err)
	}
	defer f.Close()
	line := 0
	for scan := bufio.NewScanner(f); scan.Scan(); {
		line++
		text := scan.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		expect := text[0] == '1'
		path := ""
		if len(text) > 1 {
			raw, err := hex.DecodeString(strings.TrimPrefix(text[1:], " "))
			if err != nil {
				t.Fatalf("line %d: %v", line, err)
			}
			path = string(raw)
		}
		if validWorkspacePath(path) != expect {
			t.Errorf("line %d: validWorkspacePath(%q) = %v, want %v", line, path, !expect, expect)
		}
	}
}

func TestValidWorkspacePathLengths(t *testing.T) {
	seg := strings.Repeat("a", 255)
	if !validWorkspacePath(seg) {
		t.Error("segment of 255 should be valid")
	}
	if validWorkspacePath(seg + "a") {
		t.Error("segment of 256 should be invalid")
	}
	total := strings.Repeat(seg[:170]+"/", 2) + seg[:170]
	if len(total) != 512 {
		t.Fatalf("constructed length %d", len(total))
	}
	if !validWorkspacePath(total) {
		t.Error("total of 512 should be valid")
	}
	if validWorkspacePath(total + "a") {
		t.Error("total of 513 should be invalid")
	}
}

func TestCheckWorkspace(t *testing.T) {
	valid := func() *Request {
		return &Request{Cmd: []Cmd{{
			WorkspaceDiff: &WorkspaceDiff{MaxFileCount: 1, MaxTotalSize: 1},
		}}}
	}
	configured := &worker{}

	if err := configured.checkWorkspace(&Request{Cmd: []Cmd{{}}}); err != nil {
		t.Fatalf("request without workspaceDiff: %v", err)
	}
	if err := configured.checkWorkspace(valid()); err != nil {
		t.Fatalf("valid request: %v", err)
	}

	cases := []struct {
		name string
		edit func(*Request)
	}{
		{"two commands", func(r *Request) { r.Cmd = append(r.Cmd, Cmd{}) }},
		{"pipe mapping", func(r *Request) { r.PipeMapping = []PipeMap{{}} }},
		{"shm mapping", func(r *Request) { r.ShmMapping = []ShmMap{{}} }},
		{"zero file count", func(r *Request) { r.Cmd[0].WorkspaceDiff.MaxFileCount = 0 }},
		{"zero total size", func(r *Request) { r.Cmd[0].WorkspaceDiff.MaxTotalSize = 0 }},
		{"symlinks", func(r *Request) { r.Cmd[0].Symlinks = map[string]string{"a": "b"} }},
		{"copy out", func(r *Request) { r.Cmd[0].CopyOut = []CmdCopyOutFile{{Name: "a"}} }},
		{"collector not a pipe", func(r *Request) {
			r.Cmd[0].Files = []CmdFile{&Collector{Name: "stdout"}}
		}},
		{"copyOutCached without collector", func(r *Request) {
			r.Cmd[0].CopyOutCached = []CmdCopyOutFile{{Name: "stdout"}}
		}},
		{"copyIn not cached", func(r *Request) {
			r.Cmd[0].CopyIn = map[string]CmdFile{"a": &MemoryFile{}}
		}},
		{"copyIn without forceCopy", func(r *Request) {
			r.Cmd[0].CopyIn = map[string]CmdFile{"a": &CachedFile{FileID: "x"}}
		}},
		{"copyIn path invalid", func(r *Request) {
			r.Cmd[0].CopyIn = map[string]CmdFile{"/a": &CachedFile{FileID: "x", ForceCopy: true}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid()
			tc.edit(req)
			if !errors.Is(configured.checkWorkspace(req), ErrWorkspaceInvalid) {
				t.Fatalf("expected ErrWorkspaceInvalid, got %v", configured.checkWorkspace(req))
			}
		})
	}

	withCollector := valid()
	withCollector.Cmd[0].Files = []CmdFile{&Collector{Name: "stdout", Pipe: true}}
	withCollector.Cmd[0].CopyOutCached = []CmdCopyOutFile{{Name: "stdout"}}
	withCollector.Cmd[0].CopyIn = map[string]CmdFile{"a/b": &CachedFile{FileID: "x", ForceCopy: true}}
	if err := configured.checkWorkspace(withCollector); err != nil {
		t.Fatalf("request with pipe collector and copyIn: %v", err)
	}
}

func TestFinishSkipsDiffWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Result{Status: envexec.StatusAccepted, RunTime: time.Second}
	(&workspaceSession{}).finish(ctx, nil, envexec.Result{}, &res)
	if res.Status != envexec.StatusAccepted || res.WorkspaceDiff != nil {
		t.Fatalf("cancelled finish changed the result: %+v", res)
	}
}

func TestNewWorkspaceSessionNilWithoutDiff(t *testing.T) {
	if (&worker{}).newWorkspaceSession(nil) != nil {
		t.Fatal("expected nil session")
	}
}

func TestDiffWalk(t *testing.T) {
	var same, other [32]byte
	same[0] = 1
	other[0] = 2
	before := map[string][32]byte{
		"keep":   same,
		"edited": same,
		"gone":   same,
		"was":    same,
		"link":   same,
	}
	walked := &walkResult{
		status: WorkspaceStatusOK,
		files: map[string][32]byte{
			"keep":   same,
			"edited": other,
			"new":    other,
			"b":      other,
		},
		changed: []WorkspaceChanged{
			{Path: "new", FileID: "id-new", Size: 3},
			{Path: "b", FileID: "id-b", Size: 4},
			{Path: "edited", FileID: "id-edited", Size: 2},
		},
		skipped: []WorkspaceSkipped{
			{Path: []byte("was"), Reason: WorkspaceReasonNotRegular},
			{Path: []byte("c"), Reason: WorkspaceReasonNotRegular},
			{Path: []byte("link"), Reason: WorkspaceReasonNotRegular},
			{Path: []byte("a"), Reason: WorkspaceReasonInvalidPath},
		},
	}
	diff := diffWalk(before, walked, 10)
	if diff.Status != WorkspaceStatusOK {
		t.Fatalf("status %s", diff.Status)
	}
	if len(diff.Changed) != 3 || diff.Changed[0].Path != "b" || diff.Changed[1].Path != "edited" || diff.Changed[2].Path != "new" {
		t.Fatalf("changed %+v", diff.Changed)
	}
	if strings.Join(diff.Removed, ",") != "gone,link,was" {
		t.Fatalf("removed %v", diff.Removed)
	}
	if len(diff.Skipped) != 4 ||
		!bytes.Equal(diff.Skipped[0].Path, []byte("a")) || diff.Skipped[0].Reason != WorkspaceReasonInvalidPath ||
		!bytes.Equal(diff.Skipped[1].Path, []byte("c")) || diff.Skipped[1].Reason != WorkspaceReasonNotRegular ||
		!bytes.Equal(diff.Skipped[2].Path, []byte("link")) || diff.Skipped[2].Reason != WorkspaceReasonNotRegular ||
		!bytes.Equal(diff.Skipped[3].Path, []byte("was")) || diff.Skipped[3].Reason != WorkspaceReasonNotRegular {
		t.Fatalf("skipped %+v", diff.Skipped)
	}
	if diff.SkippedTotal != 4 {
		t.Fatalf("total %d", diff.SkippedTotal)
	}
	cut := diffWalk(before, walked, 2)
	if len(cut.Skipped) != 2 || !bytes.Equal(cut.Skipped[0].Path, []byte("a")) || !bytes.Equal(cut.Skipped[1].Path, []byte("c")) || cut.SkippedTotal != 4 {
		t.Fatalf("truncated %+v total %d", cut.Skipped, cut.SkippedTotal)
	}

	limited := diffWalk(before, &walkResult{status: WorkspaceStatusTooLarge}, 10)
	if limited.Status != WorkspaceStatusTooLarge || limited.Changed != nil || limited.Removed != nil || limited.Skipped != nil {
		t.Fatalf("limited %+v", limited)
	}
}
