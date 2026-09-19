// Package buildinfo reports which build of veilbridged is running.
//
// Release binaries get the tag baked in at link time:
//
//	go build -ldflags "-X github.com/veilbridge-os/veilbridge/internal/buildinfo.version=v0.1.0" ./cmd/veilbridged
//
// Builds without that flag (go build, go install, `go run`) fall back to the
// VCS stamps the Go toolchain embeds automatically, so a bug report from a
// source build still identifies a commit instead of saying "devel".
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version is overwritten at link time for release artifacts. Keep the name and
// package path stable: the release workflow references them literally.
var version string

// Info describes the running binary.
type Info struct {
	Version  string // "v0.1.0" for a release, else "devel"
	Revision string // git commit, when the toolchain stamped one
	Time     string // commit time, RFC 3339, when stamped
	Dirty    bool   // built from a modified working tree
	Go       string // toolchain version
	Platform string // GOOS/GOARCH
}

// Read collects the build stamps available in this binary.
func Read() Info {
	info := Info{
		Version:  version,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if info.Version == "" {
		info.Version = "devel"
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
		case "vcs.time":
			info.Time = s.Value
		case "vcs.modified":
			info.Dirty = s.Value == "true"
		}
	}
	return info
}

// String renders one human-readable line, e.g.
// "veilbridged v0.1.0 (a3c1b1e, 2026-09-19T08:00:00Z) go1.26.4 linux/arm64".
func (i Info) String() string {
	var b strings.Builder
	b.WriteString("veilbridged ")
	b.WriteString(i.Version)

	var stamps []string
	if i.Revision != "" {
		rev := i.Revision
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if i.Dirty {
			rev += "-dirty"
		}
		stamps = append(stamps, rev)
	}
	if i.Time != "" {
		stamps = append(stamps, i.Time)
	}
	if len(stamps) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(stamps, ", "))
		b.WriteString(")")
	}

	b.WriteString(" ")
	b.WriteString(i.Go)
	b.WriteString(" ")
	b.WriteString(i.Platform)
	return b.String()
}
