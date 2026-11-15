package model

import (
	"encoding/base64"
	"encoding/json"

	"github.com/criyle/go-judge/worker"
)

// WorkspaceDiff is the HTTP form of a workspace diff request.
type WorkspaceDiff struct {
	MaxFileCount uint32 `json:"maxFileCount"`
	MaxTotalSize uint64 `json:"maxTotalSize"`
	MaxSkipped   uint32 `json:"maxSkipped"`
}

// ToWorker converts the HTTP request form into the worker's request.
func (d *WorkspaceDiff) ToWorker() *worker.WorkspaceDiff {
	if d == nil {
		return nil
	}
	return &worker.WorkspaceDiff{
		MaxFileCount: d.MaxFileCount,
		MaxTotalSize: d.MaxTotalSize,
		MaxSkipped:   d.MaxSkipped,
	}
}

// WorkspaceChanged is one changed regular file in a workspace diff response.
type WorkspaceChanged struct {
	Path   string `json:"path"`
	FileID string `json:"fileId"`
	Size   uint64 `json:"size"`
}

// WorkspaceSkipped is a path the diff did not hash. Path is raw bytes and may
// not be UTF-8, so JSON always emits standard base64.
type WorkspaceSkipped struct {
	Path   []byte
	Reason string
}

func (s WorkspaceSkipped) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}{
		Path:   base64.StdEncoding.EncodeToString(s.Path),
		Reason: s.Reason,
	})
}

// WorkspaceDiffResult is the HTTP form of a workspace diff.
type WorkspaceDiffResult struct {
	Status       string             `json:"status"`
	Changed      []WorkspaceChanged `json:"changed,omitempty"`
	Removed      []string           `json:"removed,omitempty"`
	Skipped      []WorkspaceSkipped `json:"skipped,omitempty"`
	SkippedTotal uint32             `json:"skippedTotal"`
}

func workspaceDiffResultFromWorker(d *worker.WorkspaceDiffResult) *WorkspaceDiffResult {
	if d == nil {
		return nil
	}
	out := &WorkspaceDiffResult{
		Status:       string(d.Status),
		Removed:      d.Removed,
		SkippedTotal: d.SkippedTotal,
	}
	for _, c := range d.Changed {
		out.Changed = append(out.Changed, WorkspaceChanged{Path: c.Path, FileID: c.FileID, Size: c.Size})
	}
	for _, s := range d.Skipped {
		out.Skipped = append(out.Skipped, WorkspaceSkipped{Path: append([]byte(nil), s.Path...), Reason: string(s.Reason)})
	}
	return out
}
