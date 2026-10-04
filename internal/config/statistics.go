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

	// Path is Directory resolved against the configuration file.
	Path string `yaml:"-"`
}

func defaultStatistics() Statistics { return Statistics{Directory: "", KeepDays: 400} }

func (s *Statistics) check(dir string, add func(path, message, hint string)) {
	s.Path = ""
	if s.KeepDays < 1 || s.KeepDays > stats.MaxKeepDays {
		add("statistics.keep_days", fmt.Sprintf("%d is out of range", s.KeepDays),
			fmt.Sprintf("use a number of days from 1 to %d; 400 keeps a little over a year", stats.MaxKeepDays))
	}
	if s.Directory == "" {
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
