package stream

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/criyle/go-judge/cmd/go-judge/model"
	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/worker"
	"go.uber.org/zap"
)

type recordingStream struct {
	mu    sync.Mutex
	sends []Response
}

func (r *recordingStream) Send(resp Response) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, resp)
	return nil
}

func (*recordingStream) Recv() (*Request, error) {
	return nil, nil
}

// cancelOnlyWorker finishes an execution only when its context is cancelled,
// like a sandboxed process that runs until it is killed.
type cancelOnlyWorker struct {
	worker.Worker
}

func (cancelOnlyWorker) Execute(ctx context.Context, _ *worker.Request) <-chan worker.Response {
	rtCh := make(chan worker.Response, 1)
	go func() {
		<-ctx.Done()
		rtCh <- worker.Response{Results: []worker.Result{{
			Status:  envexec.StatusSignalled,
			FileIDs: map[string]string{"out": "leftover-id"},
		}}}
	}()
	return rtCh
}

type badInputStream struct {
	recordingStream
	mu    sync.Mutex
	recvs int
}

func (b *badInputStream) Recv() (*Request, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recvs++
	if b.recvs == 1 {
		return &Request{Request: &model.Request{Cmd: []model.Cmd{{Args: []string{"/bin/true"}}}}}, nil
	}
	return &Request{}, nil
}

func TestStartKillsProcessAndReleasesFilesOnInputError(t *testing.T) {
	s := &badInputStream{}
	var removed []string
	done := make(chan error, 1)
	go func() {
		done <- Start(context.Background(), s, cancelOnlyWorker{}, nil, zap.NewNop(), func(id string) {
			removed = append(removed, id)
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected invalid request error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not cancel the execution after the input error")
	}
	if len(removed) != 1 || removed[0] != "leftover-id" {
		t.Fatalf("removed %v", removed)
	}
}

func TestSendLoopDrainsOutputBeforeFinalResponse(t *testing.T) {
	s := &recordingStream{}
	outCh := make(chan *OutputResponse)
	outDone := make(chan error, 1)
	rtCh := make(chan worker.Response, 1)
	errCh := make(chan error, 1)

	rtCh <- worker.Response{
		Results: []worker.Result{{
			Status: envexec.StatusAccepted,
		}},
	}

	go func() {
		_, err := sendLoop(context.Background(), s, outCh, outDone, rtCh, zap.NewNop())
		errCh <- err
	}()

	outCh <- &OutputResponse{Index: 0, Fd: 1, Content: []byte("tail")}
	close(outCh)
	outDone <- nil
	close(outDone)

	if err := <-errCh; err != nil {
		t.Fatalf("sendLoop returned error: %v", err)
	}

	if len(s.sends) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(s.sends))
	}
	if s.sends[0].Output == nil || string(s.sends[0].Output.Content) != "tail" {
		t.Fatalf("expected output frame first, got %#v", s.sends[0])
	}
	if s.sends[1].Response == nil {
		t.Fatalf("expected final response second, got %#v", s.sends[1])
	}
}
