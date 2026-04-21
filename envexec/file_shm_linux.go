//go:build linux

package envexec

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const shmSeals = unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_SEAL

// newShmMaster creates a memfd of the requested size, applies size seals
// (F_SEAL_SHRINK | F_SEAL_GROW | F_SEAL_SEAL), and returns the result as an
// *os.File. Callers should dup the returned file for every target slot and
// close the master once placement is complete.
func newShmMaster(size Size) (*os.File, error) {
	if size == 0 {
		return nil, fmt.Errorf("shm: size must be positive")
	}
	fd, err := unix.MemfdCreate("go-judge-shm", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("shm: memfd_create: %w", err)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("shm: ftruncate(%d): %w", size, err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, shmSeals); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("shm: add seals: %w", err)
	}
	return os.NewFile(uintptr(fd), "shm"), nil
}

// dupShm duplicates a master shm fd so it can be passed to a child process
// independently. The returned *os.File shares the same open file description
// as master (and therefore the same page-cache-backed shared memory).
func dupShm(master *os.File) (*os.File, error) {
	newFd, err := unix.Dup(int(master.Fd()))
	if err != nil {
		return nil, fmt.Errorf("shm: dup: %w", err)
	}
	return os.NewFile(uintptr(newFd), master.Name()), nil
}
