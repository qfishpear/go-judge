package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/criyle/go-judge/envexec"
)

// ErrWorkspaceInvalid is returned when workspaceDiff is set on a request this
// server will not run. The user command is not started.
var ErrWorkspaceInvalid = errors.New("invalid workspace request")

// ErrWorkspaceStreaming is returned when workspaceDiff is set on a streaming
// exec. Streaming has no single user command whose /w can be diffed.
var ErrWorkspaceStreaming = fmt.Errorf("%w: workspaceDiff is not supported on streaming exec", ErrWorkspaceInvalid)

type WorkspaceStatus string

const (
	WorkspaceStatusOK           WorkspaceStatus = "OK"
	WorkspaceStatusTooManyFiles WorkspaceStatus = "TOO_MANY_FILES"
	WorkspaceStatusTooLarge     WorkspaceStatus = "TOO_LARGE"
)

type WorkspaceReason string

const (
	WorkspaceReasonNotRegular      WorkspaceReason = "NOT_REGULAR"
	WorkspaceReasonInvalidPath     WorkspaceReason = "INVALID_PATH"
	WorkspaceReasonOtherFilesystem WorkspaceReason = "OTHER_FILESYSTEM"
)

// workspaceEntryCap bounds every directory entry visited under /w: regular
// files, directories, and skipped entries. It matches nr_inodes on the /w
// tmpfs in deploy/go_judge_mount.yaml. The root inode is not visited, so a
// tree that fits in that tmpfs stays under the cap. Raising nr_inodes without
// raising this cap is what makes the fuse trip.
const workspaceEntryCap = 4096

// WorkspaceDiff is the per-command request to diff /w after the command.
// MaxFileCount counts regular files only, including ones whose content did
// not change. Directories and skipped entries do not count toward it.
type WorkspaceDiff struct {
	MaxFileCount uint32
	MaxTotalSize uint64
	MaxSkipped   uint32
}

// WorkspaceChanged is one regular file whose content differs from copyIn.
type WorkspaceChanged struct {
	Path   string
	FileID string
	Size   uint64
}

// WorkspaceSkipped is a path the diff did not hash. Path is raw bytes and may
// not be UTF-8.
type WorkspaceSkipped struct {
	Path   []byte
	Reason WorkspaceReason
}

// WorkspaceDiffResult is the diff of /w against the copyIn set.
type WorkspaceDiffResult struct {
	Status       WorkspaceStatus
	Changed      []WorkspaceChanged
	Removed      []string
	Skipped      []WorkspaceSkipped
	SkippedTotal uint32
}

// checkWorkspace rejects a workspace diff this server cannot run. A request
// with no workspaceDiff is always accepted.
func (w *worker) checkWorkspace(req *Request) error {
	n := 0
	for i := range req.Cmd {
		if req.Cmd[i].WorkspaceDiff != nil {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	if len(req.Cmd) != 1 {
		return fmt.Errorf("%w: requires exactly one command", ErrWorkspaceInvalid)
	}
	if len(req.PipeMapping) != 0 {
		return fmt.Errorf("%w: pipeMapping must be empty", ErrWorkspaceInvalid)
	}
	if len(req.ShmMapping) != 0 {
		return fmt.Errorf("%w: shmMapping must be empty", ErrWorkspaceInvalid)
	}
	c := &req.Cmd[0]
	if c.WorkspaceDiff.MaxFileCount == 0 {
		return fmt.Errorf("%w: maxFileCount must be positive", ErrWorkspaceInvalid)
	}
	if c.WorkspaceDiff.MaxTotalSize == 0 {
		return fmt.Errorf("%w: maxTotalSize must be positive", ErrWorkspaceInvalid)
	}
	if len(c.Symlinks) != 0 {
		return fmt.Errorf("%w: symlinks must be empty", ErrWorkspaceInvalid)
	}
	if len(c.CopyOut) != 0 {
		return fmt.Errorf("%w: copyOut must be empty", ErrWorkspaceInvalid)
	}
	collectors := map[string]bool{}
	for i, f := range c.Files {
		if f == nil {
			continue
		}
		switch t := f.(type) {
		case *Collector:
			if !t.Pipe {
				return fmt.Errorf("%w: collector %q must be a pipe", ErrWorkspaceInvalid, t.Name)
			}
			collectors[t.Name] = true
		case *MemoryFile, *LocalFile, *CachedFile:
		default:
			return fmt.Errorf("%w: files[%d] type %T is not supported", ErrWorkspaceInvalid, i, f)
		}
	}
	for _, f := range c.CopyOutCached {
		if !collectors[f.Name] {
			return fmt.Errorf("%w: copyOutCached %q is not a pipe collector", ErrWorkspaceInvalid, f.Name)
		}
	}
	for path, f := range c.CopyIn {
		cached, ok := f.(*CachedFile)
		if !ok || cached == nil || !cached.ForceCopy {
			return fmt.Errorf("%w: copyIn %q must be a cached file with forceCopy", ErrWorkspaceInvalid, path)
		}
		if !validWorkspacePath(path) {
			return fmt.Errorf("%w: copyIn path %q is invalid", ErrWorkspaceInvalid, path)
		}
	}
	return nil
}

// validWorkspacePath is the path rule shared with the workspace walker.
// A path is relative, slash-separated, and at most 512 bytes. Segments are
// non-empty, not "." or "..", and at most 255 bytes. The bytes must be valid
// UTF-8 with no NUL or other C0 control. DEL is allowed. Equivalent Unicode
// sequences are not folded together.
func validWorkspacePath(p string) bool {
	if p == "" || len(p) > 512 || p[0] == '/' || p[len(p)-1] == '/' {
		return false
	}
	if !utf8.ValidString(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 {
			return false
		}
	}
	start := 0
	for i := 0; i <= len(p); i++ {
		if i != len(p) && p[i] != '/' {
			continue
		}
		seg := p[start:i]
		if seg == "" || len(seg) > 255 || seg == "." || seg == ".." {
			return false
		}
		start = i + 1
	}
	return true
}

// workspaceSession carries one command's workspace diff: the baseline taken
// after copyIn, and the post-run diff.
type workspaceSession struct {
	w      *worker
	spec   WorkspaceDiff
	before map[string][32]byte
}

// newWorkspaceSession returns nil when the command did not ask for a diff, so
// callers can skip the workspace path entirely.
func (w *worker) newWorkspaceSession(spec *WorkspaceDiff) *workspaceSession {
	if spec == nil {
		return nil
	}
	return &workspaceSession{w: w, spec: *spec}
}

// baseline hashes /w after copyIn and before the user command. maxFileCount
// and maxTotalSize apply only to the pass after the command. The entry cap
// applies here too: exceeding it returns an error, AfterCopyIn fails, and the
// command is not started.
func (s *workspaceSession) baseline(ctx context.Context, env envexec.Environment) error {
	walked, err := s.w.walkWorkspace(ctx, env, nil, nil)
	if err != nil {
		return err
	}
	if walked.status != WorkspaceStatusOK {
		return fmt.Errorf("workspace: baseline: %s", walked.status)
	}
	s.before = walked.files
	return nil
}

// finish diffs /w. The walk's own time is not added to RunTime.
func (s *workspaceSession) finish(ctx context.Context, env envexec.Environment, result envexec.Result, res *Result) {
	if !workspaceDiffWanted(ctx, result) {
		return
	}
	walked, err := s.w.walkWorkspace(ctx, env, s.before, &s.spec)
	if err != nil {
		if ctx.Err() == nil {
			res.Status = envexec.StatusInternalError
			res.Error = err.Error()
		}
		return
	}
	if ctx.Err() != nil {
		s.w.dropChanged(walked.changed)
		return
	}
	res.WorkspaceDiff = diffWalk(s.before, walked, s.spec.MaxSkipped)
}

// workspaceDiffWanted is false when the command never produced a stable /w:
// the caller cancelled, the run is already an internal error, or copyIn /
// symlink failed before the user program ran. A later file error, including a
// full pipe collector, still gets a diff.
func workspaceDiffWanted(ctx context.Context, result envexec.Result) bool {
	if ctx.Err() != nil || result.Status == envexec.StatusInternalError {
		return false
	}
	for _, fe := range result.FileError {
		switch fe.Type {
		case envexec.ErrCopyInOpenFile, envexec.ErrCopyInCreateDir, envexec.ErrCopyInCreateFile, envexec.ErrCopyInCopyContent, envexec.ErrSymlink:
			return false
		}
	}
	return true
}

// walkResult is one pass over /w. files maps each hashed path to its sum.
// changed is filled only by a storing pass.
type walkResult struct {
	status  WorkspaceStatus
	files   map[string][32]byte
	changed []WorkspaceChanged
	skipped []WorkspaceSkipped
}

func (w *worker) dropChanged(changed []WorkspaceChanged) {
	for _, c := range changed {
		w.fs.Remove(c.FileID)
	}
}

// diffWalk assembles the diff of one walk. A limited walk produces only its
// status: changed, removed, and skipped stay empty.
func diffWalk(before map[string][32]byte, walked *walkResult, maxSkipped uint32) *WorkspaceDiffResult {
	diff := &WorkspaceDiffResult{Status: walked.status}
	if walked.status != WorkspaceStatusOK {
		return diff
	}
	diff.Changed = slices.SortedFunc(slices.Values(walked.changed), func(a, b WorkspaceChanged) int {
		return strings.Compare(a.Path, b.Path)
	})
	for path := range before {
		if _, ok := walked.files[path]; !ok {
			diff.Removed = append(diff.Removed, path)
		}
	}
	slices.Sort(diff.Removed)
	skipped := slices.SortedFunc(slices.Values(walked.skipped), func(a, b WorkspaceSkipped) int {
		return bytes.Compare(a.Path, b.Path)
	})
	// maxSkipped truncates the returned list. Skipped entries are not bounded by maxFileCount.
	diff.SkippedTotal = uint32(len(skipped))
	diff.Skipped = skipped[:min(len(skipped), int(maxSkipped))]
	return diff
}
