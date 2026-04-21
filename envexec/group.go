package envexec

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// Group defines the running instruction to run multiple
// exec in parallel restricted within cgroup
type Group struct {
	// Cmd defines Cmd running in parallel in multiple environments
	Cmd []*Cmd

	// Pipes defines the potential mapping between Cmd.
	// ensure nil is used as placeholder in correspond cmd
	Pipes []Pipe

	// Shms defines memfd-backed shared memory regions shared between multiple
	// Cmd. Each Shm entry reserves one or more file descriptor slots on the
	// referenced cmds; the slot in that cmd's Files list must be nil.
	Shms []Shm

	// NewStoreFile defines interface to create stored file
	NewStoreFile NewStoreFile
}

// CmdFdIndex points to a specific fd slot of a specific Cmd inside a Group.
type CmdFdIndex struct {
	Index int
	Fd    int
}

// Pipe defines the pipe between parallel Cmd
type Pipe struct {
	// In, Out defines the pipe input source and output destination
	In, Out CmdFdIndex

	// CPUSet pins the proxy relay thread to the same cpuset as the commands.
	CPUSet string

	// Name defines copy out entry name if it is not empty and proxy is enabled
	Name string

	// Limit defines maximum bytes copy out from proxy and proxy will still
	// copy data after limit exceeded
	Limit Size

	// Proxy creates 2 pipe and connects them by copying data
	Proxy bool

	// Disable no copy on Linux
	DisableZeroCopy bool
}

// Shm defines a shared memory region backed by a memfd created by the parent.
// The memfd is created with the given Size, sealed with F_SEAL_SHRINK |
// F_SEAL_GROW | F_SEAL_SEAL, and placed (as O_RDWR) at every (Index, Fd)
// position in Targets. Typical usage is for the cmds to mmap this fd.
type Shm struct {
	Size    Size
	Targets []CmdFdIndex
}

// Run starts the cmd and returns exec results
func (r *Group) Run(ctx context.Context) ([]Result, error) {
	// prepare files
	fds, pipeToCollect, err := prepareFds(r, r.NewStoreFile)
	if err != nil {
		return nil, err
	}

	// wait all cmd to finish
	var g errgroup.Group
	result := make([]Result, len(r.Cmd))
	for i, c := range r.Cmd {
		g.Go(func() error {
			r, err := runSingle(ctx, c, fds[i], pipeToCollect[i], r.NewStoreFile)
			result[i] = r
			if err != nil {
				result[i].Status = StatusInternalError
				result[i].Error = err.Error()
				return err
			}
			return nil
		})
	}
	err = g.Wait()
	return result, err
}
