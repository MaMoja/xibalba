package geo

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reader is checked against the test databases MaxMind publishes with the
// format description (github.com/maxmind/MaxMind-DB). They are not part of
// this repository. To run this test, clone that repository and set
// XIBALBA_MMDB_TESTDATA to its directory.
func TestAgainstThePublishedTestDatabases(t *testing.T) {
	root := os.Getenv("XIBALBA_MMDB_TESTDATA")
	if root == "" {
		t.Skip("XIBALBA_MMDB_TESTDATA is not set")
	}
	checked := 0
	for _, name := range []string{"GeoLite2-Country-Test", "GeoIP2-Country-Test", "GeoIP2-City-Test", "GeoLite2-City-Test"} {
		raw, err := os.ReadFile(filepath.Join(root, "test-data", name+".mmdb"))
		if err != nil {
			t.Fatal(err)
		}
		db, err := Open(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if db.Type == "" || db.Built.IsZero() {
			t.Errorf("%s: type %q, built %v", name, db.Type, db.Built)
		}
		source, err := os.ReadFile(filepath.Join(root, "source-data", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var entries []map[string]struct {
			Country struct {
				ISO string `json:"iso_code"`
			} `json:"country"`
		}
		if err := json.Unmarshal(source, &entries); err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			for network, record := range entry {
				prefix, err := netip.ParsePrefix(network)
				if err != nil {
					t.Fatalf("%s: %q: %v", name, network, err)
				}
				// The source lists IPv4 networks in IPv6 notation (::a.b.c.d/n).
				addr := prefix.Addr()
				if raw := addr.As16(); addr.Is6() && strings.HasPrefix(network, "::") && raw[10] == 0 && raw[11] == 0 {
					addr = netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]})
				}
				if got := db.Country(addr).String(); got != record.Country.ISO {
					t.Errorf("%s: %s (%s) = %q, want %q", name, network, addr, got, record.Country.ISO)
				}
				checked++
			}
		}
	}
	if checked < 300 {
		t.Errorf("only %d networks checked", checked)
	}

	// Every other published file, including the deliberately broken ones,
	// must open or be refused, and must answer lookups, without a panic.
	for _, dir := range []string{"test-data", "bad-data"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			db, err := Open(raw)
			if err != nil {
				return nil
			}
			for _, a := range []string{"1.1.1.1", "2.125.160.216", "::1", "2001:218::1", "::ffff:1.1.1.1", "255.255.255.255", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "0.0.0.0", "::"} {
				db.Country(netip.MustParseAddr(a))
			}
			return nil
		})
	}
}
