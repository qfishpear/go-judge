//go:build linux

package env

import (
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestKernelVersionAtLeast(t *testing.T) {
	tests := []struct {
		name                         string
		major, minor                 int
		requiredMajor, requiredMinor int
		want                         bool
	}{
		{name: "older major", major: 5, minor: 15, requiredMajor: 6, requiredMinor: 9, want: false},
		{name: "older minor", major: 6, minor: 8, requiredMajor: 6, requiredMinor: 9, want: false},
		{name: "exact", major: 6, minor: 9, requiredMajor: 6, requiredMinor: 9, want: true},
		{name: "newer minor", major: 6, minor: 10, requiredMajor: 6, requiredMinor: 9, want: true},
		{name: "newer major", major: 7, minor: 0, requiredMajor: 6, requiredMinor: 9, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kernelVersionAtLeast(tt.major, tt.minor, tt.requiredMajor, tt.requiredMinor); got != tt.want {
				t.Fatalf("kernelVersionAtLeast(%d, %d, %d, %d) = %v, want %v",
					tt.major, tt.minor, tt.requiredMajor, tt.requiredMinor, got, tt.want)
			}
		})
	}
}

func TestPrepareMountAndPathsAddsReadOnlyCopyInStaging(t *testing.T) {
	staging := t.TempDir()
	_, builder, _, _, err := prepareMountAndPaths(Config{
		MountConf:        filepath.Join(t.TempDir(), "missing.yaml"),
		CopyInStagingDir: staging,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	appendCopyInMount(builder, staging)

	for _, mount := range builder.Mounts {
		if mount.Source == staging && mount.Target == "copyin" {
			if !mount.IsReadOnly() {
				t.Fatal("copy-in staging mount is writable")
			}
			return
		}
	}
	t.Fatal("copy-in staging mount is missing")
}
