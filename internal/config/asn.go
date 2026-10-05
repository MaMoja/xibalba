package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MaMoja/xibalba/internal/geo"
)

// DefaultASNDownloadURL is where the free database of network operators of
// DB-IP is published. See docs/NETWORKS.md for its licence.
const DefaultASNDownloadURL = "https://download.db-ip.com/free/dbip-asn-lite-{year}-{month}.mmdb.gz"

// ASN holds the settings of the database that says which network operator
// (autonomous system) an address belongs to. It is documented in
// docs/NETWORKS.md and implemented in internal/geo.
type ASN struct {
	// Database is the database file (.mmdb), relative to the configuration
	// file. Empty: operators are not known and asn conditions are a mistake.
	Database string `yaml:"database"`
	// Download fetches the database from DownloadURL when the file is
	// missing or a month old.
	Download bool `yaml:"download"`
	// DownloadURL is where the database is fetched from.
	DownloadURL string `yaml:"download_url"`

	// Path is Database resolved against the configuration file.
	Path string `yaml:"-"`
}

func defaultASN() ASN {
	return ASN{Database: "", Download: false, DownloadURL: DefaultASNDownloadURL}
}

func (c *ASN) check(dir string, add func(path, message, hint string)) {
	c.Path = ""
	if err := geo.CheckURL(c.DownloadURL); err != nil {
		// The address is not repeated: it may hold an access key.
		add("asn.download_url", fmt.Sprintf("the address cannot be used: %v", err),
			"give the https address of a database in .mmdb format; {year} and {month} stand for the current year and month")
	}
	if c.Database == "" {
		if c.Download {
			add("asn.download", "downloading is on but no file is named to keep the database in",
				`set asn.database, for example "asn.mmdb"`)
		}
		return
	}
	c.Path = c.Database
	if !filepath.IsAbs(c.Path) {
		c.Path = filepath.Join(dir, c.Path)
	}
	db, _, err := geo.ReadFile(c.Path)
	const want = "give a database of network operators in .mmdb format (DB-IP IP to ASN Lite, MaxMind GeoLite2 ASN), or set asn.download to true; see docs/NETWORKS.md"
	switch {
	case err == nil && geo.WantsASN(db) != nil:
		add("asn.database", fmt.Sprintf("the database %q holds no network operators (it says it is %q)", c.Database, db.Type), want)
	case err == nil:
	case c.Download:
		// A missing or unusable file is fetched again at start; it must
		// not keep Xibalba from starting.
		if info, statErr := os.Stat(c.Path); statErr == nil && !info.Mode().IsRegular() {
			add("asn.database", fmt.Sprintf("%q is not a file", c.Database), "give the name of a file to keep the database in")
		} else if parent, statErr := os.Stat(filepath.Dir(c.Path)); statErr != nil || !parent.IsDir() {
			add("asn.database", fmt.Sprintf("the directory of %q does not exist", c.Database),
				"create the directory and make it writable for the user Xibalba runs as")
		}
	default:
		add("asn.database", fmt.Sprintf("the database %q cannot be used: %v", c.Database, err), want)
	}
}
