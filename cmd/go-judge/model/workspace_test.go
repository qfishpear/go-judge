package model

import (
	"encoding/json"
	"testing"

	"github.com/criyle/go-judge/worker"
)

func TestWorkspaceSkippedJSONIsBase64(t *testing.T) {
	got, err := json.Marshal(workspaceDiffResultFromWorker(&worker.WorkspaceDiffResult{
		Status:       worker.WorkspaceStatusOK,
		Skipped:      []worker.WorkspaceSkipped{{Path: []byte{0xff}, Reason: worker.WorkspaceReasonInvalidPath}},
		SkippedTotal: 1,
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"status":"OK","skipped":[{"path":"/w==","reason":"INVALID_PATH"}],"skippedTotal":1}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}

func TestWorkspaceDiffToWorkerNil(t *testing.T) {
	var d *WorkspaceDiff
	if d.ToWorker() != nil {
		t.Fatal("nil request should convert to nil")
	}
}
