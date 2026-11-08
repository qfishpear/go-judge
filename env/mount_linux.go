package env

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/criyle/go-sandbox/container"
	"github.com/criyle/go-sandbox/pkg/mount"
	"github.com/goccy/go-yaml"
)

// Mount defines single mount point configuration.
// type could be bind / tmpfs / instanceBind.
// instanceBind is a per-container writable directory under Source, mounted at Target.
// quota and inodes (for example "8g", "4k") are user-quota limits for that container's host uid.
type Mount struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	Readonly bool   `yaml:"readonly"`
	Data     string `yaml:"data"`
	Quota    string `yaml:"quota"`
	Inodes   string `yaml:"inodes"`
}

// Link defines symlinks to be created after mounts
type Link struct {
	LinkPath string `yaml:"linkPath"`
	Target   string `yaml:"target"`
}

// Mounts defines mount points for the container.
type Mounts struct {
	Mount      []Mount  `yaml:"mount"`
	SymLinks   []Link   `yaml:"symLink"`
	MaskPaths  []string `yaml:"maskPath"`
	InitCmd    string   `yaml:"initCmd"`
	WorkDir    string   `yaml:"workDir"`
	HostName   string   `yaml:"hostName"`
	DomainName string   `yaml:"domainName"`
	UID        int      `yaml:"uid"`
	GID        int      `yaml:"gid"`
	Proc       bool     `yaml:"proc"`
	ProcRW     bool     `yaml:"procrw"`
}

func readMountConfig(p string) (*Mounts, error) {
	var m Mounts
	d, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(d, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func parseMountConfig(m *Mounts) (*mount.Builder, []instanceBindSpec, error) {
	b := mount.NewBuilder()
	wd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	var binds []instanceBindSpec
	seenTarget := map[string]struct{}{}
	for _, mt := range m.Mount {
		target := mt.Target
		if filepath.IsAbs(target) {
			target = filepath.Clean(target[1:])
		}
		source := mt.Source
		if source != "" && !filepath.IsAbs(source) {
			source = filepath.Join(wd, source)
		}
		switch mt.Type {
		case "bind":
			b.WithBind(source, target, mt.Readonly)
		case "tmpfs":
			b.WithTmpfs(target, mt.Data)
		case "instanceBind":
			if _, ok := seenTarget[target]; ok {
				return nil, nil, fmt.Errorf("duplicate instanceBind target %q", target)
			}
			seenTarget[target] = struct{}{}
			quotaBytes, err := parseByteSize(mt.Quota)
			if err != nil || quotaBytes == 0 {
				return nil, nil, fmt.Errorf("instanceBind %q: quota must be a size like 8g", target)
			}
			inodes, err := parseByteSize(mt.Inodes)
			if err != nil || inodes == 0 {
				return nil, nil, fmt.Errorf("instanceBind %q: inodes must be a count like 4k", target)
			}
			if source == "" {
				return nil, nil, fmt.Errorf("instanceBind %q: source is required", target)
			}
			// Placeholder bind so the mount list has a slot. Each container replaces Source.
			b.WithBind(source, target, false)
			binds = append(binds, instanceBindSpec{
				Source:     source,
				Target:     target,
				QuotaBytes: quotaBytes,
				Inodes:     inodes,
			})
		default:
			return nil, nil, fmt.Errorf("invalid_mount_type: %v", mt.Type)
		}
	}
	if m.Proc {
		b.WithProcRW(m.ProcRW)
	}
	return b, binds, nil
}

func getDefaultMount(tmpFsConf string) *mount.Builder {
	return mount.NewBuilder().
		// basic exec and lib
		WithBind("/bin", "bin", true).
		WithBind("/lib", "lib", true).
		WithBind("/lib64", "lib64", true).
		WithBind("/usr", "usr", true).
		WithBind("/etc/ld.so.cache", "etc/ld.so.cache", true).
		// java wants /proc/self/exe as it need relative path for lib
		// however, /proc gives interface like /proc/1/fd/3 ..
		// it is fine since open that file will be a EPERM
		// changing the fs uid and gid would be a good idea
		WithProc().
		// some compiler have multiple version
		WithBind("/etc/alternatives", "etc/alternatives", true).
		// fpc wants /etc/fpc.cfg
		WithBind("/etc/fpc.cfg", "etc/fpc.cfg", true).
		// mono wants /etc/mono
		WithBind("/etc/mono", "etc/mono", true).
		// go wants /dev/null
		WithBind("/dev/null", "dev/null", false).
		// ghc wants /var/lib/ghc
		WithBind("/var/lib/ghc", "var/lib/ghc", true).
		// javaScript wants /dev/urandom
		WithBind("/dev/urandom", "dev/urandom", false).
		// additional devices
		WithBind("/dev/random", "dev/random", false).
		WithBind("/dev/zero", "dev/zero", false).
		WithBind("/dev/full", "dev/full", false).
		// work dir
		WithTmpfs("w", tmpFsConf).
		// tmp dir
		WithTmpfs("tmp", tmpFsConf)
}

var defaultSymLinks = []container.SymbolicLink{
	{LinkPath: "/dev/fd", Target: "/proc/self/fd"},
	{LinkPath: "/dev/stdin", Target: "/proc/self/fd/0"},
	{LinkPath: "/dev/stdout", Target: "/proc/self/fd/1"},
	{LinkPath: "/dev/stderr", Target: "/proc/self/fd/2"},
}

var defaultMaskPaths = []string{
	"/sys/firmware",
	"/sys/devices/virtual/powercap",
	"/proc/acpi",
	"/proc/asound",
	"/proc/kcore",
	"/proc/keys",
	"/proc/latency_stats",
	"/proc/timer_list",
	"/proc/timer_stats",
	"/proc/sched_debug",
	"/proc/scsi",
	"/usr/lib/wsl/drivers",
	"/usr/lib/wsl/lib",
}
