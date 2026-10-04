package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaMoja/xibalba/internal/geo/geotest"
)

func TestCountrySettings(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Countries.Database != "" || cfg.Countries.Download || !strings.HasPrefix(cfg.Countries.DownloadURL, "https://") {
		t.Errorf("defaults = %+v", cfg.Countries)
	}

	database := string(geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{}))
	path := writeFiles(t, map[string]string{
		"xibalba.yaml":        base + "countries:\n  database: data/countries.mmdb\nrules:\n  list:\n    - {name: r, match: {country: [DE, at]}, action: deny}\n",
		"data/countries.mmdb": database,
	})
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Countries.Path != filepath.Join(filepath.Dir(path), "data", "countries.mmdb") {
		t.Errorf("Path = %q", cfg.Countries.Path)
	}
}

func TestCountryProblems(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.mmdb"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	rule := "rules:\n  list:\n    - {name: r, match: {country: [DE]}, action: deny}\n"
	tests := []struct{ name, yaml, path, message string }{
		{"rule without a database", rule, "rules.list[0].match.country", "no country database is configured"},
		{"file missing", "countries:\n  database: none.mmdb\n", "countries.database", "does not exist"},
		{"file is not a database", "countries:\n  database: broken.mmdb\n", "countries.database", "not a database"},
		{"download without a file name", "countries:\n  download: true\n", "countries.download", "no file is named"},
		{"download over plain http", "countries:\n  download_url: http://example.org/db.mmdb\n", "countries.download_url", "must start with https"},
		{"UK", "countries:\n  database: none.mmdb\n  download: true\n" + strings.Replace(rule, "DE", "UK", 1), "rules.list[0].match.country[0]", `use "GB"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "xibalba.yaml")
			if err := os.WriteFile(path, []byte(base+tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.path) || !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error = %v", err)
			}
		})
	}

	// With downloading on, a file that is not there yet, or is damaged, is
	// not a mistake: it is fetched at start.
	path := filepath.Join(dir, "xibalba.yaml")
	for _, file := range []string{"later.mmdb", "broken.mmdb"} {
		_ = os.WriteFile(path, []byte(base+"countries:\n  database: "+file+"\n  download: true\n"+rule), 0o600)
		if _, err := Load(path); err != nil {
			t.Errorf("download on, %s: %v", file, err)
		}
	}
	// The download address is not repeated in a message; it may hold a key.
	_ = os.WriteFile(path, []byte(base+"countries:\n  download_url: http://example.org/db?key=secret\n"), 0o600)
	if _, err := Load(path); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("error = %v", err)
	}
}
