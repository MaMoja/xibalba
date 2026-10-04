package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MaMoja/xibalba/internal/geo"
)

// DefaultCountryDownloadURL is where the free country database of DB-IP is
// published. See docs/COUNTRIES.md for its licence.
const DefaultCountryDownloadURL = "https://download.db-ip.com/free/dbip-country-lite-{year}-{month}.mmdb.gz"

// Countries holds the settings of the country database. It is documented in
// docs/COUNTRIES.md and implemented in internal/geo.
type Countries struct {
	// Database is the database file (.mmdb), relative to the configuration
	// file. Empty: countries are not known and country conditions are a
	// mistake.
	Database string `yaml:"database"`
	// Download fetches the database from DownloadURL when the file is
	// missing or a month old.
	Download bool `yaml:"download"`
	// DownloadURL is where the database is fetched from.
	DownloadURL string `yaml:"download_url"`

	// Path is Database resolved against the configuration file.
	Path string `yaml:"-"`
}

func defaultCountries() Countries {
	return Countries{Database: "", Download: false, DownloadURL: DefaultCountryDownloadURL}
}

func (c *Countries) check(dir string, add func(path, message, hint string)) {
	c.Path = ""
	if err := geo.CheckURL(c.DownloadURL); err != nil {
		// The address is not repeated: it may hold an access key.
		add("countries.download_url", fmt.Sprintf("the address cannot be used: %v", err),
			"give the https address of a database in .mmdb format; {year} and {month} stand for the current year and month")
	}
	if c.Database == "" {
		if c.Download {
			add("countries.download", "downloading is on but no file is named to keep the database in",
				`set countries.database, for example "countries.mmdb"`)
		}
		return
	}
	c.Path = c.Database
	if !filepath.IsAbs(c.Path) {
		c.Path = filepath.Join(dir, c.Path)
	}
	if _, _, err := geo.ReadFile(c.Path); err != nil {
		// With downloading on, a missing or unusable file is fetched again
		// at start; it must not keep Xibalba from starting (a power cut
		// during a download can leave a damaged file).
		if c.Download {
			if info, statErr := os.Stat(c.Path); statErr == nil && !info.Mode().IsRegular() {
				add("countries.database", fmt.Sprintf("%q is not a file", c.Database), "give the name of a file to keep the database in")
			} else if parent, statErr := os.Stat(filepath.Dir(c.Path)); statErr != nil || !parent.IsDir() {
				add("countries.database", fmt.Sprintf("the directory of %q does not exist", c.Database),
					"create the directory and make it writable for the user Xibalba runs as")
			}
			return
		}
		add("countries.database", fmt.Sprintf("the database %q cannot be used: %v", c.Database, err),
			"give a country database in .mmdb format (DB-IP IP to Country Lite, MaxMind GeoLite2 Country), or set countries.download to true; see docs/COUNTRIES.md")
	}
}
