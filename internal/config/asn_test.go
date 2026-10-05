package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/geo/geotest"
)

func TestASNSettings(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ASN.Database != "" || cfg.ASN.Download || !strings.HasPrefix(cfg.ASN.DownloadURL, "https://") || cfg.Rules.ASNOn {
		t.Errorf("defaults = %+v", cfg.ASN)
	}
	path := writeFiles(t, map[string]string{
		"xibalba.yaml":  base + "asn:\n  database: data/asn.mmdb\nrules:\n  list:\n    - {name: r, match: {asn: [64500, 64501]}, action: challenge}\n",
		"data/asn.mmdb": string(geotest.Build(map[string]string{"192.0.2.0/24": "64500"}, geotest.Options{Layout: "asn"})),
	})
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ASN.Path != filepath.Join(filepath.Dir(path), "data", "asn.mmdb") || !cfg.Rules.ASNOn {
		t.Errorf("Path = %q", cfg.ASN.Path)
	}
	engine, problems := cfg.Compile(cfg.Admin.Changes, time.Now())
	if len(problems) != 0 || !engine.UsesASN() {
		t.Errorf("compiled: %v, uses asn %v", problems, engine.UsesASN())
	}
}

func TestASNProblems(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "broken.mmdb"), []byte("not a database"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "countries.mmdb"), geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{}), 0o600)
	rule := "rules:\n  list:\n    - {name: r, match: {asn: [64500]}, action: deny}\n"
	tests := []struct{ name, yaml, path, message string }{
		{"rule without a database", rule, "rules.list[0].match.asn", "no database of network operators"},
		{"file missing", "asn:\n  database: none.mmdb\n", "asn.database", "does not exist"},
		{"file is not a database", "asn:\n  database: broken.mmdb\n", "asn.database", "not a database"},
		{"a country database", "asn:\n  database: countries.mmdb\n", "asn.database", "holds no network operators"},
		{"download without a file name", "asn:\n  download: true\n", "asn.download", "no file is named"},
		{"download over plain http", "asn:\n  download_url: http://example.org/db.mmdb\n", "asn.download_url", "must start with https"},
		{"text instead of a number", "asn:\n  database: none.mmdb\n  download: true\nrules:\n  list:\n    - {name: r, match: {asn: [AS64500]}, action: deny}\n", "", "AS64500"},
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
	path := filepath.Join(dir, "xibalba.yaml")
	for _, file := range []string{"later.mmdb", "broken.mmdb"} {
		_ = os.WriteFile(path, []byte(base+"asn:\n  database: "+file+"\n  download: true\n"+rule), 0o600)
		if _, err := Load(path); err != nil {
			t.Errorf("download on, %s: %v", file, err)
		}
	}
}

func TestAddressLists(t *testing.T) {
	path := writeFiles(t, map[string]string{
		"xibalba.yaml": base + `rules:
  address_lists:
    vpn: lists/vpn.txt
    data-centres: lists/dc.txt
  list:
    - name: check-vpn
      match:
        address_list: [vpn, data-centres]
      action: challenge
`,
		"lists/vpn.txt": "# exits\n192.0.2.0/24\n2001:db8::/32 # v6\n",
		"lists/dc.txt":  "",
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Rules.Lists["vpn"].Len() != 2 || cfg.Rules.Lists["data-centres"].Len() != 0 || !cfg.Rules.Lists["vpn"].Contains(netip.MustParseAddr("192.0.2.9")) {
		t.Errorf("lists: %+v", cfg.Rules.Lists)
	}
	// The rules typed into the web interface may name the lists too.
	state := cfg.Admin.Changes
	state.Rules = "rules:\n  - {name: mine, match: {address_list: [vpn]}, action: deny}\n"
	if _, problems := cfg.Compile(state, time.Now()); len(problems) != 0 {
		t.Errorf("own rules with a list: %v", problems)
	}

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "bad.txt"), []byte("192.0.2.0/24\nexample.org\n\n999.1.1.1\n"), 0o600)
	_ = os.Mkdir(filepath.Join(dir, "folder"), 0o700)
	rule := "  list:\n    - {name: r, match: {address_list: [vpn]}, action: deny}\n"
	tests := []struct{ name, yaml, path, message string }{
		{"file missing", "rules:\n  address_lists:\n    vpn: none.txt\n", "rules.address_lists.vpn", "does not exist"},
		{"not a file", "rules:\n  address_lists:\n    vpn: folder\n", "rules.address_lists.vpn", "is not a file"},
		{"bad line", "rules:\n  address_lists:\n    vpn: bad.txt\n", "rules.address_lists.vpn", `bad.txt, line 2: "example.org" is not an IP address or network`},
		{"second bad line", "rules:\n  address_lists:\n    vpn: bad.txt\n", "rules.address_lists.vpn", "bad.txt, line 4"},
		{"name", "rules:\n  address_lists:\n    \"VPN list\": bad.txt\n", "rules.address_lists.VPN list", "cannot be the name"},
		{"no file", "rules:\n  address_lists:\n    vpn: \"\"\n", "rules.address_lists.vpn", "no file is named"},
		{"unknown list", "rules:\n" + rule, "rules.list[0].match.address_list[0]", `no address list named "vpn"`},
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
}
