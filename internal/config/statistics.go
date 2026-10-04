package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MaMoja/xibalba/internal/stats"
)

// Statistics holds the settings for counters kept on disk. They are
// documented in docs/STATISTICS.md and implemented in internal/stats.
type Statistics struct {
	// Directory is where the counters are kept, relative to the
	// configuration file. Empty: nothing is kept across restarts.
	Directory string `yaml:"directory"`
	// KeepDays is how long an hour's counters are kept.
	KeepDays int `yaml:"keep_days"`

	// Networks holds the settings for counts per network of origin.
	Networks StatisticsNetworks `yaml:"networks"`

	// Path is Directory resolved against the configuration file.
	Path string `yaml:"-"`
}

// StatisticsNetworks holds the settings for counts per network of origin
// (IPv4 /24, IPv6 /48). Off by default: a network is not a person, but it is
// closer to one than a rule name.
type StatisticsNetworks struct {
	// Enabled switches the counts on. They need Statistics.Directory.
	Enabled bool `yaml:"enabled"`
	// Top is how many networks are kept per hour; the rest is summed up.
	Top int `yaml:"top"`
	// KeepDays is how long an hour's counts per network are kept.
	KeepDays int `yaml:"keep_days"`
}

// NetworksPath is the directory the counts per network are kept in.
func (s Statistics) NetworksPath() string { return filepath.Join(s.Path, "networks") }

func defaultStatistics() Statistics {
	return Statistics{Directory: "", KeepDays: 400, Networks: StatisticsNetworks{Enabled: false, Top: 50, KeepDays: 30}}
}

func (s *Statistics) check(dir string, add func(path, message, hint string)) {
	s.Path = ""
	if s.KeepDays < 1 || s.KeepDays > stats.MaxKeepDays {
		add("statistics.keep_days", fmt.Sprintf("%d is out of range", s.KeepDays),
			fmt.Sprintf("use a number of days from 1 to %d; 400 keeps a little over a year", stats.MaxKeepDays))
	}
	if n := s.Networks; n.Top < 1 || n.Top > 1000 {
		add("statistics.networks.top", fmt.Sprintf("%d is out of range", n.Top), "use a number from 1 to 1000; 50 shows where most requests come from")
	}
	if n := s.Networks; n.KeepDays < 1 || n.KeepDays > 400 {
		add("statistics.networks.keep_days", fmt.Sprintf("%d is out of range", n.KeepDays),
			"use a number of days from 1 to 400; keep networks only as long as you need them, 30 is a good start")
	}
	if s.Directory == "" {
		if s.Networks.Enabled {
			add("statistics.networks.enabled", "counts per network are kept on disk, and statistics.directory is empty",
				"set statistics.directory, or set statistics.networks.enabled to false")
		}
		return
	}
	s.Path = s.Directory
	if !filepath.IsAbs(s.Path) {
		s.Path = filepath.Join(dir, s.Path)
	}
	info, err := os.Stat(s.Path)
	switch {
	case err != nil:
		add("statistics.directory", fmt.Sprintf("the directory %q cannot be used: it does not exist or cannot be opened", s.Directory),
			"create the directory and make it writable for the user Xibalba runs as")
	case !info.IsDir():
		add("statistics.directory", fmt.Sprintf("%q is not a directory", s.Directory), "give a directory")
	}
}
