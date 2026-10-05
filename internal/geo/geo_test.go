package geo

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/geo/geotest"
	"github.com/MaMoja/xibalba/internal/health"
)

var networks = map[string]string{
	"192.0.2.0/24":    "DE",
	"198.51.100.0/25": "AT",
	"203.0.113.7/32":  "CH",
	"2001:db8::/32":   "FR",
	"2a00::/12":       "NL",
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestCountry(t *testing.T) {
	plain := networks
	lookups := map[string]string{
		"192.0.2.1": "DE", "192.0.2.255": "DE", "192.0.3.0": "",
		"198.51.100.127": "AT", "198.51.100.128": "",
		"203.0.113.7": "CH", "203.0.113.8": "",
		"::ffff:192.0.2.9": "DE", // IPv4 written as IPv6
		"2001:db8::1":      "FR", "2001:db9::1": "",
		"2a0f:ffff::1": "NL", "2a10::1": "",
		"10.0.0.1": "", "::1": "", "0.0.0.0": "", "255.255.255.255": "",
	}
	variants := map[string]geotest.Options{
		"24 bit":          {},
		"28 bit":          {RecordSize: 28},
		"32 bit":          {RecordSize: 32},
		"with pointers":   {Pointers: true},
		"country_code":    {Layout: "code"},
		"plain country":   {Layout: "plain"},
		"IPv4 only":       {IPv4Only: true},
		"IPv4 only, 28":   {IPv4Only: true, RecordSize: 28},
		"28 + pointers":   {RecordSize: 28, Pointers: true},
		"32, IPv4, plain": {RecordSize: 32, IPv4Only: true, Layout: "plain"},
	}
	for name, opts := range variants {
		t.Run(name, func(t *testing.T) {
			db, err := Open(geotest.Build(plain, opts))
			if err != nil {
				t.Fatal(err)
			}
			if db.Type != "Test-Country" || db.Built.Year() != 2026 {
				t.Errorf("type %q, built %v", db.Type, db.Built)
			}
			for a, want := range lookups {
				if opts.IPv4Only && addr(a).Unmap().Is6() {
					want = ""
				}
				if got := db.Country(addr(a)).String(); got != want {
					t.Errorf("%s = %q, want %q", a, got, want)
				}
			}
			if db.Country(netip.Addr{}) != (Code{}) {
				t.Error("the invalid address has a country")
			}
		})
	}
}

func TestCountryDoesNotAllocate(t *testing.T) {
	db, err := Open(geotest.Build(networks, geotest.Options{Pointers: true}))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []netip.Addr{addr("192.0.2.1"), addr("2001:db8::1"), addr("10.0.0.1")} {
		if n := testing.AllocsPerRun(100, func() { db.Country(a) }); n != 0 {
			t.Errorf("Country(%s) allocates %v times", a, n)
		}
	}
}

func TestParseCode(t *testing.T) {
	for in, want := range map[string]string{"DE": "DE", "de": "DE", "aT": "AT", "D": "", "DEU": "", "D1": "", "": "", "ÄÖ": ""} {
		code, ok := ParseCode(in)
		if code.String() != want || ok != (want != "") {
			t.Errorf("ParseCode(%q) = %q, %v", in, code, ok)
		}
	}
}

func TestOpenRefuses(t *testing.T) {
	good := geotest.Build(networks, geotest.Options{})
	at := bytes.LastIndex(good, marker)
	tests := map[string][]byte{
		"empty":                    {},
		"not a database":           []byte("<html>not found</html>"),
		"only the marker":          marker,
		"cut off in the tree":      append(append([]byte{}, good[:20]...), good[at:]...),
		"description cut off":      good[:at+len(marker)+10],
		"another format version":   bytes.Replace(good, []byte("binary_format_major_version\xa1\x02"), []byte("binary_format_major_version\xa1\x03"), 1),
		"unknown record size":      bytes.Replace(good, []byte("record_size\xa1\x18"), []byte("record_size\xa1\x14"), 1),
		"node count far too large": bytes.Replace(good, []byte("node_count\xc4\x00"), []byte("node_count\xc4\x7f"), 1),
	}
	for name, data := range tests {
		if db, err := Open(data); err == nil || db != nil {
			t.Errorf("%s: opened", name)
		}
	}
}

// Whatever is in the file, a lookup returns and does not panic.
func TestDamagedFilesAreSafe(t *testing.T) {
	for _, opts := range []geotest.Options{{}, {RecordSize: 28, Pointers: true}, {RecordSize: 32, Layout: "code"}} {
		good := geotest.Build(networks, opts)
		at := bytes.LastIndex(good, marker)
		probes := []netip.Addr{addr("192.0.2.1"), addr("198.51.100.1"), addr("2001:db8::1"), addr("2a00::1"), addr("8.8.8.8"), addr("::")}
		for i := 0; i < at; i++ {
			for _, flip := range []byte{0xff, 0x01, 0x80, 0x20} {
				bad := append([]byte{}, good...)
				bad[i] ^= flip
				db, err := Open(bad)
				if err != nil {
					continue
				}
				for _, a := range probes {
					db.Country(a)
				}
			}
		}
	}
}

// A file in which maps point at themselves must not hang a lookup.
func TestPointerLoopsEnd(t *testing.T) {
	good := geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{IPv4Only: true})
	db, err := Open(good)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the data section by: a map with very many pairs whose keys
	// are pointers back to the start.
	loop := []byte{0xe0 | 31, 0xff, 0xff, 0xff}
	for len(loop) < len(db.data) {
		loop = append(loop, 0x20, 0x00)
	}
	copy(db.data, loop[:len(db.data)])
	done := make(chan struct{})
	go func() { db.Country(addr("192.0.2.1")); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the lookup did not end")
	}
}

func FuzzOpen(f *testing.F) {
	f.Add(geotest.Build(networks, geotest.Options{}))
	f.Add(geotest.Build(networks, geotest.Options{RecordSize: 28, Pointers: true}))
	f.Add([]byte("\xab\xcd\xefMaxMind.com\xe0"))
	f.Fuzz(func(t *testing.T, data []byte) {
		db, err := Open(data)
		if err != nil {
			return
		}
		db.Country(addr("192.0.2.1"))
		db.Country(addr("2001:db8::1"))
	})
}

func BenchmarkCountry(b *testing.B) {
	db, err := Open(geotest.Build(networks, geotest.Options{}))
	if err != nil {
		b.Fatal(err)
	}
	a := addr("2001:db8::1")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db.Country(a)
	}
}

// --- locator --------------------------------------------------------------

func write(t *testing.T, path string, data []byte, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestLocatorLoadsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "countries.mmdb")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	l := New(Options{Path: path, Now: func() time.Time { return now }})

	// No file: nothing is known, and health says why.
	l.reload()
	if l.Country(addr("192.0.2.1")) != (Code{}) {
		t.Error("a country without a database")
	}
	if h := l.Health(); h.State != health.Degraded || !strings.Contains(h.Detail, "does not exist") {
		t.Errorf("health = %+v", h)
	}

	write(t, path, geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{}), now)
	l.reload()
	if got := l.Country(addr("192.0.2.1")).String(); got != "DE" {
		t.Fatalf("after loading: %q", got)
	}
	if h := l.Health(); h.State != health.OK {
		t.Errorf("health = %+v", h)
	}

	// A newer file is picked up.
	write(t, path, geotest.Build(map[string]string{"192.0.2.0/24": "AT"}, geotest.Options{}), now.Add(time.Hour))
	l.reload()
	if got := l.Country(addr("192.0.2.1")).String(); got != "AT" {
		t.Fatalf("after the file changed: %q", got)
	}

	// A broken file does not replace the good database.
	write(t, path, []byte("broken"), now.Add(2*time.Hour))
	l.reload()
	if got := l.Country(addr("192.0.2.1")).String(); got != "AT" {
		t.Fatalf("after the file broke: %q", got)
	}
	if h := l.Health(); h.State != health.Degraded || !strings.Contains(h.Detail, "stays in use") {
		t.Errorf("health = %+v", h)
	}

	// Old data is reported.
	write(t, path, geotest.Build(map[string]string{"192.0.2.0/24": "AT"}, geotest.Options{}), now.Add(3*time.Hour))
	l.reload()
	now = time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	if h := l.Health(); h.State != health.Degraded || !strings.Contains(h.Detail, "replace it") {
		t.Errorf("health with old data = %+v", h)
	}
}

func TestDownload(t *testing.T) {
	database := geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{})
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write(database)
	_ = zw.Close()

	var answer atomic.Value
	var asked atomic.Value
	answer.Store(zipped.Bytes())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r.URL.Path + " " + r.Header.Get("User-Agent"))
		_, _ = w.Write(answer.Load().([]byte))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "countries.mmdb")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	l := New(Options{Path: path, Download: true, DownloadURL: server.URL + "/db-{year}-{month}.mmdb.gz", UserAgent: "Xibalba/test",
		Now: func() time.Time { return now }})
	ctx := context.Background()

	l.maybeDownload(ctx)
	l.reload()
	if got := l.Country(addr("192.0.2.1")).String(); got != "DE" {
		t.Fatalf("after the download: %q (%+v)", got, l.Health())
	}
	if asked.Load() != "/db-2026-10.mmdb.gz Xibalba/test" {
		t.Errorf("asked for %v", asked.Load())
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v", info.Mode().Perm())
	}

	// A fresh file is not downloaded again.
	asked.Store("")
	now = now.Add(24 * time.Hour)
	_ = os.Chtimes(path, now.Add(-time.Hour), now.Add(-time.Hour))
	l.maybeDownload(ctx)
	if asked.Load() != "" {
		t.Error("a fresh file was downloaded again")
	}

	// A month later it is; an uncompressed answer works too.
	now = now.Add(31 * 24 * time.Hour)
	answer.Store(geotest.Build(map[string]string{"192.0.2.0/24": "AT"}, geotest.Options{}))
	l.maybeDownload(ctx)
	l.reload()
	if got := l.Country(addr("192.0.2.1")).String(); got != "AT" || asked.Load() != "/db-2026-11.mmdb.gz Xibalba/test" {
		t.Fatalf("a month later: %q, asked %v", got, asked.Load())
	}

	// A download that is not a database does not replace the file, and
	// leaves no temporary file behind.
	old := now.Add(-40 * 24 * time.Hour)
	_ = os.Chtimes(path, old, old)
	for _, bad := range [][]byte{[]byte("<html>maintenance</html>"), {0x1f, 0x8b, 1, 2, 3}, {}} {
		now = now.Add(7 * time.Hour)
		answer.Store(bad)
		l.maybeDownload(ctx)
		l.reload()
		if got := l.Country(addr("192.0.2.1")).String(); got != "AT" {
			t.Fatalf("after a bad download (%q): %q", bad, got)
		}
	}
	if h := l.Health(); h.State != health.Degraded || !strings.Contains(h.Detail, "download failed") {
		t.Errorf("health = %+v", h)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d files in the directory", len(entries))
	}
}

// A file that is fresh but damaged (a power cut during the last download)
// is fetched again instead of being kept for a month.
func TestDamagedFileIsDownloadedAgain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{}))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "countries.mmdb")
	write(t, path, []byte{}, time.Now())
	l := New(Options{Path: path, Download: true, DownloadURL: server.URL})
	l.reload()
	l.maybeDownload(context.Background())
	l.reload()
	if got := l.Country(addr("192.0.2.1")).String(); got != "DE" {
		t.Errorf("after a damaged file: %q (%+v)", got, l.Health())
	}
	if h := l.Health(); h.State != health.OK {
		t.Errorf("health = %+v", h)
	}
}

func TestDownloadIsOffUnlessAsked(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer server.Close()
	l := New(Options{Path: filepath.Join(t.TempDir(), "c.mmdb"), DownloadURL: server.URL})
	l.maybeDownload(context.Background())
	if hit {
		t.Error("downloaded although downloading is off")
	}
}

func TestCheckURL(t *testing.T) {
	for _, u := range []string{"https://download.example.org/db-{year}-{month}.mmdb.gz", "http://127.0.0.1:8080/db", "http://localhost/db"} {
		if err := CheckURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"http://download.example.org/db", "ftp://example.org/db", "example.org/db", "https://user:pw@example.org/db", "file:///etc/passwd", ""} {
		if err := CheckURL(u); err == nil {
			t.Errorf("%s was accepted", u)
		}
	}
}

func TestStartAndStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "countries.mmdb")
	write(t, path, geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{}), time.Now())
	l := New(Options{Path: path})
	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := l.Country(addr("192.0.2.1")).String(); got != "DE" || l.Name() != "countries" {
		t.Errorf("country %q, name %q", got, l.Name())
	}
	if err := l.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestASN(t *testing.T) {
	operators := map[string]string{"192.0.2.0/24": "64500", "198.51.100.0/24": "4200000000", "2001:db8::/32": "64501"}
	for _, opts := range []geotest.Options{{Layout: "asn"}, {Layout: "asn-text"}, {Layout: "asn", RecordSize: 28}, {Layout: "asn", IPv4Only: true}} {
		db, err := Open(geotest.Build(operators, opts))
		if err != nil {
			t.Fatal(err)
		}
		if !db.HasASN() {
			t.Errorf("%+v: HasASN is false", opts)
		}
		want := map[string]uint32{"192.0.2.9": 64500, "198.51.100.1": 4200000000, "203.0.113.1": 0, "::ffff:192.0.2.9": 64500, "2001:db8::1": 64501, "2001:db9::1": 0}
		if opts.IPv4Only {
			want["2001:db8::1"] = 0
		}
		for a, n := range want {
			if got := db.ASN(addr(a)); got != n {
				t.Errorf("%+v: ASN(%s) = %d, want %d", opts, a, got, n)
			}
		}
		if db.Country(addr("192.0.2.9")) != (Code{}) {
			t.Error("a database of operators named a country")
		}
		probe := addr("192.0.2.9")
		if n := testing.AllocsPerRun(100, func() { db.ASN(probe) }); n != 0 {
			t.Errorf("%v allocations per lookup", n)
		}
	}
	countries, _ := Open(geotest.Build(networks, geotest.Options{}))
	if countries.HasASN() || countries.ASN(addr("192.0.2.1")) != 0 {
		t.Error("a country database passes for one of network operators")
	}
	for text, want := range map[string]uint32{"AS64500": 64500, "as7": 7, "64500": 64500, "AS": 0, "": 0, "AS-1": 0, "AS99999999999": 0, "ASx": 0, "AS4294967296": 0, "AS4294967295": 4294967295} {
		if got := parseASN([]byte(text)); got != want {
			t.Errorf("parseASN(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestDamagedOperatorFilesAreSafe(t *testing.T) {
	good := geotest.Build(map[string]string{"192.0.2.0/24": "64500", "2001:db8::/32": "64501"}, geotest.Options{Layout: "asn"})
	at := bytes.LastIndex(good, marker)
	for i := 0; i < at; i++ {
		for _, flip := range []byte{0xff, 0x01, 0x80, 0x20} {
			bad := append([]byte{}, good...)
			bad[i] ^= flip
			db, err := Open(bad)
			if err != nil {
				continue
			}
			db.HasASN()
			db.ASN(addr("192.0.2.1"))
			db.ASN(addr("2001:db8::1"))
		}
	}
}

func TestLocatorForOperators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asn.mmdb")
	if err := os.WriteFile(path, geotest.Build(map[string]string{"192.0.2.0/24": "64500"}, geotest.Options{Layout: "asn"}), 0o600); err != nil {
		t.Fatal(err)
	}
	l := New(Options{Name: "asn", What: "network", Path: path})
	if l.Name() != "asn" || l.ASN(addr("192.0.2.1")) != 0 || l.Loaded() {
		t.Error("before the start")
	}
	if !strings.Contains(l.Health().Detail, "no network database is loaded") {
		t.Errorf("health: %+v", l.Health())
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Stop(context.Background()) }()
	if l.ASN(addr("192.0.2.1")) != 64500 || !l.Loaded() {
		t.Error("after the start")
	}
}
