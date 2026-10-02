// Package buildinfo reports which build of Xibalba is running.
package buildinfo

import "runtime/debug"

// Version is set at build time with
//
//	-ldflags "-X github.com/MaMoja/xibalba/internal/buildinfo.Version=v1.2.3"
//
// Builds made without it report "dev".
var Version = "dev"

// Info describes the running build.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Go      string `json:"go"`
}

// Get returns the build information. The commit comes from the version control
// data the Go toolchain embeds, when it is available.
func Get() Info {
	info := Info{Version: Version}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	info.Go = bi.GoVersion
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			info.Commit = s.Value
			if len(info.Commit) > 12 {
				info.Commit = info.Commit[:12]
			}
		}
	}
	return info
}

// String returns a one-line description such as "xibalba v0.1.0 (abc123def456, go1.24.7)".
func (i Info) String() string {
	s := "xibalba " + i.Version
	switch {
	case i.Commit != "" && i.Go != "":
		s += " (" + i.Commit + ", " + i.Go + ")"
	case i.Go != "":
		s += " (" + i.Go + ")"
	}
	return s
}
