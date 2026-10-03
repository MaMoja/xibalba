package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MaMoja/xibalba/internal/license"
	"github.com/MaMoja/xibalba/internal/pages"
)

// maxLicenseFileSize bounds the license file that is read.
const maxLicenseFileSize = 64 << 10

// Env is what loading a configuration depends on besides the file itself.
// The program uses DefaultEnv; tests supply their own time and key.
type Env struct {
	// Now is the moment against which a license's term is judged.
	Now time.Time
	// LicenseKey is the public key sponsor licenses are checked against.
	LicenseKey ed25519.PublicKey
}

// DefaultEnv returns the environment of the running program: the current
// time and the public key built into it.
func DefaultEnv() Env {
	key, _ := license.PublicKey() // a malformed built-in key leaves it nil: no license verifies
	return Env{Now: time.Now(), LicenseKey: key}
}

// License holds the sponsor license setting and what was found in the file.
type License struct {
	// File is the sponsor license file, relative to the configuration file.
	// Empty means Xibalba runs without a license, which is fully supported.
	File string `yaml:"file"`

	// Path is File resolved against the directory of the configuration file.
	Path string `yaml:"-"`
	// Info is the license found in the file, or nil if there is none.
	Info *license.License `yaml:"-"`
	// State is whether the license could be used when the configuration was
	// loaded. Empty if there is no license.
	State license.State `yaml:"-"`
}

// Usable reports whether a license was found that unlocks the sponsor features.
func (l License) Usable() bool {
	return l.Info != nil && l.State != license.Expired
}

// SponsorSettings returns the settings in use that need a sponsor license.
func (c Config) SponsorSettings() []string {
	var used []string
	if strings.TrimSpace(c.Pages.Operator) != "" {
		used = append(used, "pages.operator")
	}
	if len(c.Pages.Texts) > 0 {
		used = append(used, "pages.texts")
	}
	if !c.Pages.Attribution {
		used = append(used, "pages.attribution")
	}
	return used
}

// checkLicense reads the license file, if one is configured, and reports
// settings that need a license when there is none.
//
// An expired license is not a mistake in the configuration: Xibalba starts,
// the pages return to their standard form, and the start-up log and health
// report say why. A website must not go down because a renewal is late.
func (c *Config) checkLicense(dir string, env Env, add func(path, message, hint string)) {
	c.License.Path, c.License.Info, c.License.State = "", nil, ""

	if strings.TrimSpace(c.License.File) != "" {
		path := c.License.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		path = filepath.Clean(path)
		c.License.Path = path

		data, err := readSmallFile(path, maxLicenseFileSize)
		if err != nil {
			add("license.file", fmt.Sprintf("the license file %q cannot be used: %v", c.License.File, err),
				"check the path; it is relative to the directory of the configuration file. To run without a license, remove license.file")
			return
		}
		found, err := license.Parse(string(data), env.LicenseKey)
		if err != nil {
			add("license.file", fmt.Sprintf("%q is not a usable license: %v", c.License.File, err),
				"copy the license file you received again, unchanged. To run without a license, remove license.file")
			return
		}
		c.License.Info = &found
		c.License.State = found.State(env.Now)
		return
	}

	for _, setting := range c.SponsorSettings() {
		add(setting, "this setting needs a sponsor license, and no license file is configured",
			"sponsors receive a license file; set license.file to it (see docs/SPONSORS.md). Without a license, remove this setting: "+
				"the pages then use the standard wording and show the line \"Protected by Xibalba\"")
	}
}

// PageOptions returns the settings for the visitor pages as they take
// effect: the sponsor settings apply only with a usable license.
func (c Config) PageOptions() pages.Options {
	opts := pages.Options{
		Contact:         c.Pages.Contact,
		DefaultLanguage: c.Pages.DefaultLanguage,
	}
	if c.License.Usable() {
		opts.Operator = c.Pages.Operator
		opts.Texts = c.Pages.Texts
		opts.HideAttribution = !c.Pages.Attribution
	}
	return opts
}

// readSmallFile reads a regular file of at most limit bytes.
func readSmallFile(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errors.New("it does not exist")
	case err != nil:
		return nil, errors.New("it cannot be opened")
	case !info.Mode().IsRegular():
		return nil, errors.New("it is not a regular file")
	case info.Size() > limit:
		return nil, fmt.Errorf("it is larger than %d KiB", limit>>10)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("it cannot be read")
	}
	return data, nil
}
