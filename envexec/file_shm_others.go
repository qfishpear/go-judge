//go:build !linux

package envexec

import (
	"fmt"
	"os"
)

func newShmMaster(size Size) (*os.File, error) {
	return nil, fmt.Errorf("shm: memfd-backed shared memory is only supported on Linux")
}

func dupShm(master *os.File) (*os.File, error) {
	return nil, fmt.Errorf("shm: memfd-backed shared memory is only supported on Linux")
}
