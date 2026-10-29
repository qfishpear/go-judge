package grpcexecutor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/criyle/go-judge/filestore"
	"github.com/criyle/go-judge/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestRedactURLDropsQuery(t *testing.T) {
	got := redactURL("https://minio.example/bucket/obj?X-Amz-Signature=sekret&X-Amz-Credential=abc")
	if strings.Contains(got, "sekret") || strings.Contains(got, "?") {
		t.Fatalf("query leaked: %s", got)
	}
	if got != "https://minio.example/bucket/obj" {
		t.Fatalf("got %s", got)
	}
}

func TestFileAddCancelRemovesFile(t *testing.T) {
	dir := t.TempDir()
	srv := &execServer{fs: filestore.NewFileLocalStore(dir), logger: zap.NewNop(), minioTimeout: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := srv.FileAdd(ctx, pb.FileAddRequest_builder{
		Name:    proto.String("a.txt"),
		Content: []byte("hello"),
	}.Build())
	if status.Code(err) != codes.Canceled {
		t.Fatalf("got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover %v", entries)
	}
}

func TestDownloadCancelRemovesAddedFiles(t *testing.T) {
	dir := t.TempDir()
	var hits atomic.Int32
	release := make(chan struct{})
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "first")
			return
		}
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		httpSrv.Close()
	})

	signed := httpSrv.URL + "/obj?X-Amz-Signature=sekret"
	srv := &execServer{fs: filestore.NewFileLocalStore(dir), logger: zap.NewNop(), minioTimeout: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := srv.FileDownloadFromMinio(ctx, pb.DownloadFromMinioRequest_builder{
			PresignedGetUrls: []string{signed, signed},
		}.Build())
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	err := <-done
	if status.Code(err) != codes.Canceled {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "sekret") {
		t.Fatalf("signature leaked: %v", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover %v", entries)
	}
}

func TestDownloadErrorRedactsPresignedQuery(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(httpSrv.Close)

	dir := t.TempDir()
	srv := &execServer{fs: filestore.NewFileLocalStore(dir), logger: zap.NewNop(), minioTimeout: time.Hour}
	signed := httpSrv.URL + "/obj?X-Amz-Signature=sekret"
	_, err := srv.FileDownloadFromMinio(context.Background(), pb.DownloadFromMinioRequest_builder{
		PresignedGetUrls: []string{signed},
	}.Build())
	if err == nil || strings.Contains(err.Error(), "sekret") {
		t.Fatalf("error should not contain the signature: %v", err)
	}
}

func TestUploadErrorRedactsURLError(t *testing.T) {
	dir := t.TempDir()
	fs := filestore.NewFileLocalStore(dir)
	f, err := fs.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	id, err := fs.Add("a.bin", f.Name())
	if err != nil {
		t.Fatal(err)
	}

	srv := &execServer{fs: fs, logger: zap.NewNop(), minioTimeout: 50 * time.Millisecond}
	signed := "http://127.0.0.1:1/bucket/obj?X-Amz-Signature=sekret"
	_, err = srv.FileUploadToMinio(context.Background(), pb.UploadToMinioRequest_builder{
		Items: []*pb.UploadItem{pb.UploadItem_builder{
			FileId:          id,
			PresignedPutUrl: signed,
		}.Build()},
	}.Build())
	if err == nil || strings.Contains(err.Error(), "sekret") {
		t.Fatalf("error should not contain the signature: %v", err)
	}
}
