//go:build linux

package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/criyle/go-judge/envexec"
	"golang.org/x/sys/unix"
)

const workspaceDirentBuf = 64 * 1024

// walkWorkspace hashes every regular file under /w. spec nil is the baseline
// pass: file-count and size limits are not applied and nothing is stored. The
// entry cap applies to both passes. Otherwise files whose hash differs from
// before are stored and listed in changed. A walk that hits a limit returns
// only its status; anything it stored is removed.
func (w *worker) walkWorkspace(ctx context.Context, env envexec.Environment, before map[string][32]byte, spec *WorkspaceDiff) (*walkResult, error) {
	root, err := w.openWorkspace(env)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	wk := &walker{
		ctx:        ctx,
		before:     before,
		maxFiles:   ^uint32(0),
		maxEntries: workspaceEntryCap,
		maxSize:    ^uint64(0),
	}
	if spec != nil {
		wk.maxFiles, wk.maxSize, wk.store = spec.MaxFileCount, spec.MaxTotalSize, w.storeWorkspace
	}
	res, err := wk.run(root)
	if err != nil || res.status != WorkspaceStatusOK {
		w.dropChanged(res.changed)
	}
	if err != nil {
		return nil, err
	}
	if res.status != WorkspaceStatusOK {
		return &walkResult{status: res.status}, nil
	}
	return res, nil
}

func (w *worker) openWorkspace(env envexec.Environment) (*os.File, error) {
	if w.containerWorkDir == "" {
		return nil, errors.New("workspace: container work dir is not set")
	}
	opened, err := env.Open([]envexec.OpenParam{{
		Path:    w.containerWorkDir,
		OpenDir: true,
	}})
	if err != nil {
		return nil, fmt.Errorf("workspace: open work dir: %w", err)
	}
	if len(opened) != 1 {
		return nil, errors.New("workspace: open work dir: empty result")
	}
	if res := opened[0]; res.Err != nil {
		if res.File != nil {
			res.File.Close()
		}
		return nil, fmt.Errorf("workspace: open work dir: %w", res.Err)
	}
	if opened[0].File == nil {
		return nil, errors.New("workspace: open work dir: empty result")
	}
	return opened[0].File, nil
}

func (w *worker) storeWorkspace(path string, f *os.File, size uint64) (string, error) {
	if w.fs == nil {
		return "", errors.New("workspace: file store is not configured")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tmp, err := w.fs.New()
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(f, int64(size)+1))
	if err != nil {
		return "", fmt.Errorf("workspace: store %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("workspace: store %q: %w", path, err)
	}
	if uint64(n) != size {
		return "", fmt.Errorf("workspace: %q size %d does not match stat %d", path, n, size)
	}
	id, err := w.fs.Add(path, tmp.Name())
	if err != nil {
		return "", err
	}
	keep = true
	return id, nil
}

// walker is one pass over /w.
//
// maxEntries counts every entry (regular files, directories, and skipped
// ones) and is the walk-time fuse. maxFiles counts regular files only,
// including ones whose content did not change.
//
// An entry whose path fails validWorkspacePath is skipped and not entered.
// Every path below it would be invalid too. So rel stays within 512 bytes plus
// one name, and the walk holds at most 256 directory fds.
//
// Openat2 with RESOLVE_NO_XDEV refuses every mount point, bind mounts
// included, so other filesystems surface as EXDEV.
type walker struct {
	ctx        context.Context
	before     map[string][32]byte
	maxFiles   uint32
	maxEntries uint32
	maxSize    uint64
	store      func(path string, f *os.File, size uint64) (string, error)

	entries uint32
	files   uint32
	total   uint64
	rel     []byte
	buf     []byte
	res     walkResult
}

func (wk *walker) run(root *os.File) (*walkResult, error) {
	wk.buf = make([]byte, workspaceDirentBuf)
	wk.res = walkResult{status: WorkspaceStatusOK, files: map[string][32]byte{}}
	err := wk.walk(int(root.Fd()))
	return &wk.res, err
}

func (wk *walker) abort(status WorkspaceStatus) {
	if wk.res.status == WorkspaceStatusOK {
		wk.res.status = status
	}
}

func (wk *walker) skip(reason WorkspaceReason) {
	wk.res.skipped = append(wk.res.skipped, WorkspaceSkipped{
		Path:   append([]byte(nil), wk.rel...),
		Reason: reason,
	})
}

func (wk *walker) walk(dirfd int) error {
	for {
		n, err := unix.Getdents(dirfd, wk.buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("workspace: list directory: %w", err)
		}
		if n == 0 {
			return nil
		}
		// ParseDirent copies the names, so the recursion may reuse buf.
		_, _, names := unix.ParseDirent(wk.buf[:n], -1, nil)
		for _, name := range names {
			if wk.res.status != WorkspaceStatusOK {
				return nil
			}
			if err := wk.ctx.Err(); err != nil {
				return err
			}
			if err := wk.entry(dirfd, name); err != nil {
				return err
			}
		}
	}
}

func (wk *walker) entry(dirfd int, name string) error {
	old := len(wk.rel)
	if old > 0 {
		wk.rel = append(wk.rel, '/')
	}
	wk.rel = append(wk.rel, name...)
	defer func() { wk.rel = wk.rel[:old] }()

	if wk.entries >= wk.maxEntries {
		wk.abort(WorkspaceStatusTooManyFiles)
		return nil
	}
	wk.entries++
	path := string(wk.rel)
	if !validWorkspacePath(path) {
		wk.skip(WorkspaceReasonInvalidPath)
		return nil
	}
	var info unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("workspace: stat %s: %w", path, err)
	}
	switch info.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return wk.dir(dirfd, name, path)
	case unix.S_IFREG:
		return wk.file(dirfd, name, path)
	default:
		wk.skip(WorkspaceReasonNotRegular)
		return nil
	}
}

func (wk *walker) dir(dirfd int, name, path string) error {
	child, err := openChild(dirfd, name, true)
	if errors.Is(err, unix.EXDEV) {
		wk.skip(WorkspaceReasonOtherFilesystem)
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: open directory %s: %w", path, err)
	}
	defer child.Close()
	// before holds only regular files, so this file became a directory.
	if _, ok := wk.before[path]; ok {
		wk.skip(WorkspaceReasonNotRegular)
	}
	return wk.walk(int(child.Fd()))
}

func (wk *walker) file(dirfd int, name, path string) error {
	f, err := openChild(dirfd, name, false)
	if errors.Is(err, unix.EXDEV) {
		wk.skip(WorkspaceReasonOtherFilesystem)
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: open %s: %w", path, err)
	}
	defer f.Close()
	var info unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &info); err != nil {
		return fmt.Errorf("workspace: stat %s: %w", path, err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG {
		wk.skip(WorkspaceReasonNotRegular)
		return nil
	}
	if wk.files >= wk.maxFiles {
		wk.abort(WorkspaceStatusTooManyFiles)
		return nil
	}
	wk.files++
	size := uint64(info.Size)
	if size > wk.maxSize-wk.total {
		wk.abort(WorkspaceStatusTooLarge)
		return nil
	}
	wk.total += size
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, int64(size)+1))
	if err != nil {
		return fmt.Errorf("workspace: read %s: %w", path, err)
	}
	if uint64(n) != size {
		return fmt.Errorf("workspace: %s size %d does not match stat %d", path, n, size)
	}
	var sum [32]byte
	h.Sum(sum[:0])
	wk.res.files[path] = sum
	if wk.store == nil {
		return nil
	}
	if prev, ok := wk.before[path]; ok && prev == sum {
		return nil
	}
	id, err := wk.store(path, f, size)
	if err != nil {
		return err
	}
	wk.res.changed = append(wk.res.changed, WorkspaceChanged{Path: path, FileID: id, Size: size})
	return nil
}

func openChild(dirfd int, name string, dir bool) (*os.File, error) {
	flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW)
	if dir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat2(dirfd, name, &unix.OpenHow{
		Flags:   flags,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
