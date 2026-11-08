package env

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/criyle/go-judge/env/linuxcontainer"
	"github.com/criyle/go-sandbox/container"
	"github.com/criyle/go-sandbox/pkg/mount"
	"golang.org/x/sys/unix"
)

const credUIDSpan = 10000

// quota blocks passed through quotactl are 1 KiB (QIF_DQBLKSIZE).
const quotaBlockSize = 1024

const (
	qGetQuota = 0x800007
	qSetQuota = 0x800008
	qifBlimits = 1
	qifIlimits = 4
)

type instanceBindSpec struct {
	Source     string
	Target     string
	QuotaBytes uint64
	Inodes     uint64
	fs         *os.File
}

// ifDqblk matches struct if_dqblk in linux/quota.h, including tail padding.
type ifDqblk struct {
	Bhardlimit uint64
	Bsoftlimit uint64
	Curspace   uint64
	Ihardlimit uint64
	Isoftlimit uint64
	Curinodes  uint64
	Btime      uint64
	Itime      uint64
	Valid      uint32
	_          uint32
}

func parseByteSize(s string) (uint64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := uint64(1)
	switch s[len(s)-1] {
	case 'k':
		mult = 1 << 10
		s = s[:len(s)-1]
	case 'm':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'g':
		mult = 1 << 30
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func prepareInstanceBinds(binds []instanceBindSpec) ([]instanceBindSpec, error) {
	if len(binds) == 0 {
		return nil, nil
	}
	out := make([]instanceBindSpec, len(binds))
	for i, bind := range binds {
		if err := os.MkdirAll(bind.Source, 0o755); err != nil {
			return nil, fmt.Errorf("instanceBind %s: %w", bind.Source, err)
		}
		if err := removeChildren(bind.Source); err != nil {
			return nil, fmt.Errorf("instanceBind %s: clean: %w", bind.Source, err)
		}
		f, err := os.Open(bind.Source)
		if err != nil {
			return nil, fmt.Errorf("instanceBind %s: %w", bind.Source, err)
		}
		var probe ifDqblk
		if err := quotactlFd(f.Fd(), qcmd(qGetQuota, 0), 0, unsafe.Pointer(&probe)); err != nil {
			f.Close()
			return nil, fmt.Errorf("instanceBind %s: user quota is not enabled on this filesystem: %w", bind.Source, err)
		}
		bind.fs = f
		out[i] = bind
	}
	return out, nil
}

func qcmd(subcmd, typ int) int {
	return (subcmd << 8) | (typ & 0xff)
}

func quotactlFd(fd uintptr, cmd int, id int, addr unsafe.Pointer) error {
	_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, fd, uintptr(cmd), uintptr(id), uintptr(addr), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func (c *credGen) applyQuota(uid uint32) error {
	for _, bind := range c.binds {
		dq := ifDqblk{
			Bhardlimit: (bind.QuotaBytes + quotaBlockSize - 1) / quotaBlockSize,
			Ihardlimit: bind.Inodes,
			Valid:      qifBlimits | qifIlimits,
		}
		if err := quotactlFd(bind.fs.Fd(), qcmd(qSetQuota, 0), int(uid), unsafe.Pointer(&dq)); err != nil {
			return fmt.Errorf("%s: %w", bind.Source, err)
		}
	}
	return nil
}

type trackingBuilder struct {
	mu    sync.Mutex
	base  *container.Builder
	cred  *credGen
	binds []instanceBindSpec
}

func newTrackingBuilder(base *container.Builder, cred *credGen, binds []instanceBindSpec) linuxcontainer.EnvironmentBuilder {
	return &trackingBuilder{base: base, cred: cred, binds: binds}
}

func (b *trackingBuilder) Build() (container.Environment, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	mounts := append([]mount.Mount(nil), b.base.Mounts...)
	var dirs []string
	for _, bind := range b.binds {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			removeAll(dirs)
			return nil, err
		}
		dir := filepath.Join(bind.Source, hex.EncodeToString(token[:]))
		if err := os.Mkdir(dir, 0o777); err != nil {
			removeAll(dirs)
			return nil, fmt.Errorf("instanceBind mkdir %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			os.RemoveAll(dir)
			removeAll(dirs)
			return nil, err
		}
		replaced := false
		for i := range mounts {
			if mounts[i].Target == bind.Target && mounts[i].Source == bind.Source {
				mounts[i].Source = dir
				replaced = true
				break
			}
		}
		if !replaced {
			os.RemoveAll(dir)
			removeAll(dirs)
			return nil, fmt.Errorf("instanceBind target %q is missing from the mount list", bind.Target)
		}
		dirs = append(dirs, dir)
	}

	cp := *b.base
	cp.Mounts = mounts
	env, err := cp.Build()
	uid, tracked := uint32(0), false
	if b.cred != nil {
		uid, tracked = b.cred.takeOutstanding()
	}
	if err != nil {
		if tracked {
			b.cred.release(uid)
		}
		removeAll(dirs)
		return nil, err
	}
	return &trackedEnv{
		Environment: env,
		cred:        b.cred,
		uid:         uid,
		tracked:     tracked,
		dirs:        dirs,
	}, nil
}

type trackedEnv struct {
	container.Environment
	cred    *credGen
	uid     uint32
	tracked bool
	dirs    []string
}

func (e *trackedEnv) Reset() error {
	if err := e.Environment.Reset(); err != nil {
		return err
	}
	for _, dir := range e.dirs {
		if err := removeChildren(dir); err != nil {
			return fmt.Errorf("reset %s: %w", dir, err)
		}
	}
	return nil
}

func (e *trackedEnv) Destroy() error {
	err := e.Environment.Destroy()
	removeAll(e.dirs)
	if e.tracked {
		e.cred.release(e.uid)
	}
	return err
}

func removeAll(dirs []string) {
	for _, dir := range dirs {
		os.RemoveAll(dir)
	}
}

func removeChildren(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var first error
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil && first == nil {
			first = err
		}
	}
	return first
}
