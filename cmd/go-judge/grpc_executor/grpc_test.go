package grpcexecutor

import (
	"testing"

	"github.com/criyle/go-judge/cmd/go-judge/model"
	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/pb"
)

func TestConvertPBFileErrorType(t *testing.T) {
	tests := []struct {
		name string
		in   envexec.FileErrorType
		want pb.Response_FileError_ErrorType
	}{
		{name: "CopyInOpenFile", in: envexec.ErrCopyInOpenFile, want: pb.Response_FileError_CopyInOpenFile},
		{name: "CopyInCreateDir", in: envexec.ErrCopyInCreateDir, want: pb.Response_FileError_CopyInCreateFile},
		{name: "CopyInCreateFile", in: envexec.ErrCopyInCreateFile, want: pb.Response_FileError_CopyInCreateFile},
		{name: "CopyInCopyContent", in: envexec.ErrCopyInCopyContent, want: pb.Response_FileError_CopyInCopyContent},
		{name: "CopyOutOpen", in: envexec.ErrCopyOutOpen, want: pb.Response_FileError_CopyOutOpen},
		{name: "CopyOutNotRegularFile", in: envexec.ErrCopyOutNotRegularFile, want: pb.Response_FileError_CopyOutNotRegularFile},
		{name: "CopyOutSizeExceeded", in: envexec.ErrCopyOutSizeExceeded, want: pb.Response_FileError_CopyOutSizeExceeded},
		{name: "CopyOutCreateFile", in: envexec.ErrCopyOutCreateFile, want: pb.Response_FileError_CopyOutCreateFile},
		{name: "CopyOutCopyContent", in: envexec.ErrCopyOutCopyContent, want: pb.Response_FileError_CopyOutCopyContent},
		{name: "CollectSizeExceeded", in: envexec.ErrCollectSizeExceeded, want: pb.Response_FileError_CollectSizeExceeded},
		{name: "Symlink", in: envexec.ErrSymlink, want: pb.Response_FileError_Symlink},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := convertPBFileErrorType(tc.in); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestConvertPBStreamRequestShmMapping(t *testing.T) {
	req := pb.Request_builder{
		Cmd: []*pb.Request_CmdType{
			pb.Request_CmdType_builder{
				Args:            []string{"/bin/true"},
				CopyOutTruncate: true,
				CopyOutMax:      10,
			}.Build(),
			pb.Request_CmdType_builder{Args: []string{"/bin/true"}}.Build(),
		},
		ShmMapping: []*pb.Request_ShmMap{
			pb.Request_ShmMap_builder{
				Size: 4096,
				Targets: []*pb.Request_CmdFdIndex{
					pb.Request_CmdFdIndex_builder{Index: 0, Fd: 3}.Build(),
					pb.Request_CmdFdIndex_builder{Index: 1, Fd: 3}.Build(),
				},
			}.Build(),
		},
	}.Build()

	got := convertPBStreamRequest(req)
	if len(got.ShmMapping) != 1 || got.ShmMapping[0].Size != 4096 || len(got.ShmMapping[0].Targets) != 2 {
		t.Fatalf("shmMapping: %+v", got.ShmMapping)
	}
	if got.ShmMapping[0].Targets[0] != (model.CmdFdIndex{Index: 0, Fd: 3}) || got.ShmMapping[0].Targets[1] != (model.CmdFdIndex{Index: 1, Fd: 3}) {
		t.Fatalf("targets: %+v", got.ShmMapping[0].Targets)
	}
	if got.Cmd[0].CopyOutTruncate {
		t.Fatal("copyOutTruncate must stay unset on the stream path")
	}
	if got.Cmd[0].CopyOutMax != 10 {
		t.Fatalf("copyOutMax: %d", got.Cmd[0].CopyOutMax)
	}

	workerReq, err := model.ConvertRequest(got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(workerReq.ShmMapping) != 1 || workerReq.ShmMapping[0].Size != 4096 || len(workerReq.ShmMapping[0].Targets) != 2 {
		t.Fatalf("worker shm: %+v", workerReq.ShmMapping)
	}
	if workerReq.ShmMapping[0].Targets[1].Fd != 3 {
		t.Fatalf("worker target: %+v", workerReq.ShmMapping[0].Targets)
	}
	if workerReq.Cmd[0].CopyOutTruncate {
		t.Fatal("worker copyOutTruncate must stay false")
	}
}
