//go:build !linux

package worker

import (
	"context"
	"errors"

	"github.com/criyle/go-judge/envexec"
)

func (w *worker) walkWorkspace(context.Context, envexec.Environment, map[string][32]byte, *WorkspaceDiff) (*walkResult, error) {
	return nil, errors.New("workspace diff is only supported on linux")
}
