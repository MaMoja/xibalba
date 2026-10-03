package crawlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/data"
	"github.com/MaMoja/xibalba/internal/health"
)

// The definitions that ship with Xibalba must be valid, must each say where
// they come from and when that was checked, and must not collide.
func TestBuiltInDefinitions(t *testing.T) {
	defs, problems := LoadFS(data.Files, "crawlers")
	for _, p := range problems {
		t.Errorf("%s: %s: %s", p.File, p.Field, p.Message)
	}
	if len(defs) < 20 {
		t.Fatalf("only %d crawlers are defined", len(defs))
	}
	for _, p := range CheckUnique(defs, func(i int) (string, string) { return defs[i].Operator, defs[i].Name }) {
		t.Errorf("%s: %s", p.File, p.Message)
	}
	classes := map[Class]bool{}
	for _, d := range defs {
		classes[d.Class] = true
		if !strings.HasPrefix(d.Source, "https://") {
			t.Errorf("%s: the source %q is not an https address", d.Name, d.Source)
		}
		if _, err := time.Parse("2006-01-02", d.Checked); err != nil {
			t.Errorf("%s: no date of the last check", d.Name)
		}
		if u := d.Verify.RangesURL; u != "" && !strings.HasPrefix(u, "https://") {
			t.Errorf("%s: the address list %q is not fetched over https", d.Name, u)
		}
		// A crawler that cannot be verified must say so, so the reader of
		// /crawlers knows why it is never let through.
		if !d.Verify.Verifiable() && d.Note == "" {
			t.Errorf("%s cannot be verified and has no note saying why", d.Name)
		}
	}
	for _, class := range []Class{Training, AISearch, UserFetch, SearchEngine} {
		if !classes[class] {
			t.Errorf("no built-in crawler of class %s", class)
		}
	}
}

const validFile = `operator: Example
source: https://example.org/bots
checked: 2026-01-02
crawlers:
  - name: ExampleBot
    class: training
    user_agent: ExampleBot
    purpose: Test.
    verify:
      ranges: ["192.0.2.0/24", "2001:db8::/32", "198.51.100.7"]
      reverse_dns: [".bot.example.org"]
`

func TestParseFile(t *testing.T) {
	defs, problems := ParseFile("x.yaml", []byte(validFile))
	if len(problems) > 0 {
		t.Fatalf("problems: %+v", problems)
	}
	d := defs[0]
	if d.Operator != "Example" || d.Source != "https://example.org/bots" || d.Checked != "2026-01-02" {
		t.Errorf("file fields were not copied: %+v", d)
	}
	if len(d.Verify.prefixes) != 3 || d.Verify.Method() != "addresses, reverse DNS" {
		t.Errorf("verify = %+v (%s)", d.Verify, d.Verify.Method())
	}
}

func TestParseFileProblems(t *testing.T) {
	tests := []struct {
		name, replace, with string
		field, message      string
	}{
		{"no operator", "operator: Example", "operator: ''", "operator", "missing"},
		{"source is no address", "https://example.org/bots", "the docs", "source", "not a web address"},
		{"bad date", "2026-01-02", "yesterday", "checked", "not a date"},
		{"bad name", "name: ExampleBot", "name: 'Example Bot'", "crawlers[0].name", "not a valid crawler name"},
		{"unknown class", "class: training", "class: evil", "crawlers[0].class", "not a class"},
		{"user agent too short", "user_agent: ExampleBot", "user_agent: ab", "crawlers[0].user_agent", "cannot identify"},
		{"no purpose", "purpose: Test.", "purpose: ''", "crawlers[0].purpose", "missing"},
		{"not an address", `"198.51.100.7"`, `"bot.example.org"`, "crawlers[0].verify.ranges[2]", "not an IP address"},
		{"whole internet", `"192.0.2.0/24"`, `"0.0.0.0/0"`, "crawlers[0].verify.ranges[0]", "too much of the internet"},
		{"half the IPv6 internet", `"2001:db8::/32"`, `"::/1"`, "crawlers[0].verify.ranges[1]", "too much of the internet"},
		{"dns ending without dot", `".bot.example.org"`, `"bot.example.org"`, "crawlers[0].verify.reverse_dns[0]", "not a domain ending"},
		{"dns ending is a top-level domain", `".bot.example.org"`, `".com"`, "crawlers[0].verify.reverse_dns[0]", "not a domain ending"},
		{"unknown key", "purpose: Test.", "purpose: Test.\n    trusted: true", "", "not valid"},
		{"list over plain http", `ranges: [`, "ranges_url: http://example.org/list.json\n      ranges: [", "crawlers[0].verify.ranges_url", "must use https"},
		{"list with password", `ranges: [`, "ranges_url: https://u:p@example.org/list.json\n      ranges: [", "crawlers[0].verify.ranges_url", "user name or password"},
		{"list from a file", `ranges: [`, "ranges_url: file:///etc/passwd\n      ranges: [", "crawlers[0].verify.ranges_url", "cannot be used"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(validFile, tt.replace) {
				t.Fatalf("test is broken: %q is not in the file", tt.replace)
			}
			defs, problems := ParseFile("x.yaml", []byte(strings.Replace(validFile, tt.replace, tt.with, 1)))
			if defs != nil {
				t.Error("a file with a problem still produced definitions")
			}
			for _, p := range problems {
				if p.Field == tt.field && strings.Contains(p.Message, tt.message) && p.File == "x.yaml" && p.Hint != "" {
					return
				}
			}
			t.Errorf("no problem at %q containing %q; got %+v", tt.field, tt.message, problems)
		})
	}
}

func TestLoopbackListMayUseHTTP(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8080/l.json", "http://localhost/l", "http://[::1]:1/l", "https://example.org/l"} {
		if err := checkRangesURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"http://127.0.0.1.example.org/l", "http://localhost.example.org/l", "ftp://example.org/l", "example.org/l"} {
		if err := checkRangesURL(u); err == nil {
			t.Errorf("%s was accepted", u)
		}
	}
}

func TestCheckUnique(t *testing.T) {
	defs := []Definition{
		{Name: "A-Bot", UserAgent: "A-Bot", Operator: "One"},
		{Name: "a-bot", UserAgent: "other", Operator: "Two"},
		{Name: "C-Bot", UserAgent: "a-BOT", Operator: "Three"},
	}
	problems := CheckUnique(defs, func(i int) (string, string) { return "f", fmt.Sprintf("crawlers[%d]", i) })
	if len(problems) != 2 || problems[0].Field != "crawlers[1].name" || problems[1].Field != "crawlers[2].user_agent" {
		t.Errorf("problems = %+v", problems)
	}
}

func TestParseRanges(t *testing.T) {
	google := `{"creationTime": "2026-09-30T14:46:11.000000", "prefixes": [
		{"ipv6Prefix": "2001:4860:4801:10::/64"}, {"ipv4Prefix": "66.249.64.0/27"}, {"ipv4Prefix": "66.249.64.0/27"}]}`
	got, err := ParseRanges([]byte(google))
	if err != nil || len(got) != 2 {
		t.Fatalf("operator layout: %v, %v", got, err)
	}
	if !contains(got, netip.MustParseAddr("66.249.64.5")) || contains(got, netip.MustParseAddr("66.249.65.5")) {
		t.Error("wrong networks")
	}

	got, err = ParseRanges([]byte("# our bot\n192.0.2.7\n\n2001:db8::/48\r\n::ffff:198.51.100.0/120\n"))
	if err != nil || len(got) != 3 {
		t.Fatalf("text: %v, %v", got, err)
	}
	if !contains(got, netip.MustParseAddr("198.51.100.9")) {
		t.Error("an IPv4-mapped network was not read as IPv4")
	}
	if got, err = ParseRanges([]byte(`["192.0.2.0/24", ["198.51.100.1"]]`)); err != nil || len(got) != 2 {
		t.Fatalf("plain list: %v, %v", got, err)
	}
}

func TestParseRangesRefuses(t *testing.T) {
	deep := strings.Repeat("[", 100) + `"192.0.2.1"` + strings.Repeat("]", 100)
	many := &strings.Builder{}
	for i := 0; i <= maxListPrefixes; i++ {
		fmt.Fprintf(many, "11.%d.%d.%d\n", i>>16&255, i>>8&255, i&255)
	}
	tests := []struct{ name, data, message string }{
		{"empty", "  \n", "empty"},
		{"an error page", "<html><body>Not found</body></html>", "not an address"},
		{"broken JSON", `{"prefixes": [`, "not valid JSON"},
		{"JSON without addresses", `{"status": "ok", "date": "2026-01-01T10:00:00.000"}`, "no addresses"},
		{"everything", `{"prefixes": [{"ipv4Prefix": "192.0.2.0/24"}, {"ipv4Prefix": "0.0.0.0/0"}]}`, "too much"},
		{"everything, IPv6", "::/0", "too much"},
		{"a tenth of the internet", "192.0.2.1\n16.0.0.0/4", "too much"},
		{"unspecified", "0.0.0.0", "unspecified"},
		{"private network", "192.0.2.1\n10.1.0.0/16", "not a public address"},
		{"this machine", `["127.0.0.1"]`, "not a public address"},
		{"private IPv6", "fd00::/48", "not a public address"},
		{"content is not echoed", "root:x:0:0:root:/root:/bin/bash", "not an address"},
		{"nested beyond the limit", deep, "no addresses"},
		{"too many", many.String(), "more than"},
		{"too large", strings.Repeat("x", maxListBytes+1), "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRanges([]byte(tt.data))
			if err == nil || got != nil || !strings.Contains(err.Error(), tt.message) {
				t.Errorf("got %d networks, error %v; want an error containing %q", len(got), err, tt.message)
			}
		})
	}
}

// --- registry -------------------------------------------------------------

func defs(t *testing.T, yaml string) []Definition {
	t.Helper()
	d, problems := ParseFile("test.yaml", []byte(yaml))
	if len(problems) > 0 {
		t.Fatalf("test definitions: %+v", problems)
	}
	return d
}

func defsFile(crawlers string) string {
	return "operator: Example\nsource: https://example.org/bots\nchecked: 2026-01-02\ncrawlers:\n" + crawlers
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestIdentifyWithStaticAddresses(t *testing.T) {
	r := New(Options{Definitions: defs(t, defsFile(`
  - {name: Bot, class: search-engine, user_agent: ExBot, purpose: p, verify: {ranges: ["192.0.2.0/24"]}}
  - {name: Bot-Image, class: other, user_agent: ExBot-Image, purpose: p, verify: {ranges: ["192.0.2.0/24"]}}
  - {name: Loose, class: training, user_agent: LooseBot, purpose: p, note: n}
`))})
	tests := []struct {
		ua, client string
		want       Identity
	}{
		{"Mozilla/5.0 (compatible; ExBot/2.1; +http://example.org/bot)", "192.0.2.9", Identity{"Bot", SearchEngine, Verified}},
		{"mozilla/5.0 (compatible; exbot/2.1)", "192.0.2.9", Identity{"Bot", SearchEngine, Verified}},
		{"ExBot/2.1", "::ffff:192.0.2.9", Identity{"Bot", SearchEngine, Verified}},
		{"ExBot/2.1", "198.51.100.1", Identity{"Bot", SearchEngine, Unverified}},
		{"ExBot/2.1", "2001:db8::1", Identity{"Bot", SearchEngine, Unverified}},
		{"ExBot-Image/1.0", "192.0.2.9", Identity{"Bot-Image", Other, Verified}},
		{"LooseBot", "192.0.2.9", Identity{"Loose", Training, Unverifiable}},
		{"Mozilla/5.0 Firefox/130.0", "192.0.2.9", Identity{}},
		{"", "192.0.2.9", Identity{}},
		// Only the first 512 bytes of a user agent are searched.
		{strings.Repeat("x", 500) + " EXBOT/2", "192.0.2.9", Identity{"Bot", SearchEngine, Verified}},
		{strings.Repeat("x", 600) + " EXBOT/2", "192.0.2.9", Identity{}},
	}
	for _, tt := range tests {
		if got := r.Identify(tt.ua, addr(tt.client)); got != tt.want {
			t.Errorf("Identify(%q, %s) = %+v, want %+v", tt.ua, tt.client, got, tt.want)
		}
	}
	// Without a client address nothing can be verified, and nothing is refuted.
	if got := r.Identify("ExBot", netip.Addr{}); got.Status != Pending {
		t.Errorf("without an address: %v", got.Status)
	}
}

func TestIdentifyDoesNotAllocate(t *testing.T) {
	r := New(Options{Definitions: defs(t, defsFile(`
  - {name: Bot, class: search-engine, user_agent: ExBot, purpose: p, verify: {ranges: ["192.0.2.0/24"]}}
`))})
	client := addr("192.0.2.9")
	for _, ua := range []string{"Mozilla/5.0 (compatible; ExBot/2.1)", "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0"} {
		if n := testing.AllocsPerRun(100, func() { r.Identify(ua, client) }); n != 0 {
			t.Errorf("Identify(%q) allocates %v times", ua, n)
		}
	}
}

// listServer serves an address list that the test can change.
type listServer struct {
	*httptest.Server
	mu     sync.Mutex
	body   string
	status int
	hits   atomic.Int64
	agent  atomic.Value
}

func newListServer(t *testing.T, body string) *listServer {
	s := &listServer{body: body, status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.agent.Store(r.Header.Get("User-Agent"))
		s.mu.Lock()
		defer s.mu.Unlock()
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *listServer) set(status int, body string) {
	s.mu.Lock()
	s.status, s.body = status, body
	s.mu.Unlock()
}

func listed(t *testing.T, url string) []Definition {
	return defs(t, defsFile(`
  - {name: Bot, class: ai-search, user_agent: ExBot, purpose: p, verify: {ranges_url: "`+url+`"}}
  - {name: Bot-User, class: user-fetch, user_agent: ExUser, purpose: p, verify: {ranges_url: "`+url+`"}}
`))
}

func TestAddressListLifecycle(t *testing.T) {
	server := newListServer(t, `{"prefixes": [{"ipv4Prefix": "192.0.2.0/24"}]}`)
	dir := t.TempDir()
	opts := Options{Definitions: listed(t, server.URL), Refresh: true, RefreshInterval: time.Hour, CacheDir: dir, UserAgent: "Xibalba/test"}
	r := New(opts)
	ctx := context.Background()

	// Before the list is there, the claim is neither confirmed nor refuted.
	if got := r.Identify("ExBot", addr("192.0.2.1")).Status; got != Pending {
		t.Fatalf("before the download: %v", got)
	}
	if r.Health().State != health.Degraded {
		t.Error("a missing list is not reported")
	}

	r.refreshAll(ctx)
	if got := r.Identify("ExBot", addr("192.0.2.1")).Status; got != Verified {
		t.Fatalf("after the download: %v", got)
	}
	if got := r.Identify("ExUser", addr("203.0.113.1")).Status; got != Unverified {
		t.Fatalf("impostor: %v", got)
	}
	if h := r.Health(); h.State != health.OK {
		t.Errorf("health = %+v", h)
	}
	if server.hits.Load() != 1 {
		t.Errorf("the list shared by two crawlers was downloaded %d times", server.hits.Load())
	}
	if server.agent.Load() != "Xibalba/test" {
		t.Errorf("user agent = %v", server.agent.Load())
	}

	// A list that turns hostile, broken or unavailable does not replace the good one.
	for _, bad := range []struct {
		status int
		body   string
	}{
		{200, `{"prefixes": [{"ipv4Prefix": "0.0.0.0/0"}]}`},
		{200, `<html>maintenance</html>`},
		{200, ``},
		{500, `{"prefixes": [{"ipv4Prefix": "203.0.113.0/24"}]}`},
	} {
		server.set(bad.status, bad.body)
		r.refreshAll(ctx)
		if got := r.Identify("ExBot", addr("203.0.113.1")).Status; got != Unverified {
			t.Fatalf("after the list became %q (%d), an outsider is %v", bad.body, bad.status, got)
		}
		if got := r.Identify("ExBot", addr("192.0.2.1")).Status; got != Verified {
			t.Fatalf("after the list became %q (%d), the genuine crawler is %v", bad.body, bad.status, got)
		}
	}
	reports := r.Reports()
	if reports[0].ListError == "" || reports[0].Addresses != 1 || reports[0].ListFrom == nil {
		t.Errorf("report = %+v", reports[0])
	}
	if reports[0].Requests.Verified == 0 || reports[0].Requests.Unverified == 0 {
		t.Errorf("requests are not counted: %+v", reports[0].Requests)
	}

	// A good list replaces the old one.
	server.set(200, "203.0.113.0/24\n")
	r.refreshAll(ctx)
	if got := r.Identify("ExBot", addr("192.0.2.1")).Status; got != Unverified {
		t.Fatalf("an address removed from the list is still %v", got)
	}

	// A new start without network uses the kept list.
	server.Close()
	second := New(opts)
	second.loadCache()
	if got := second.Identify("ExBot", addr("203.0.113.5")).Status; got != Verified {
		t.Fatalf("after a restart: %v", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("cache directory holds %d files", len(entries))
	}
	if info, _ := entries[0].Info(); info.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode = %v", info.Mode().Perm())
	}

	// A kept list goes through the same checks as a downloaded one.
	path := dir + "/" + entries[0].Name()
	raw, _ := os.ReadFile(path)
	var c cached
	_ = json.Unmarshal(raw, &c)
	c.Prefixes = append(c.Prefixes, "0.0.0.0/0")
	raw, _ = json.Marshal(c)
	_ = os.WriteFile(path, raw, 0o600)
	third := New(opts)
	third.loadCache()
	if got := third.Identify("ExBot", addr("8.8.8.8")).Status; got != Pending {
		t.Fatalf("a tampered cache made an outsider %v", got)
	}
}

func TestStartAndStop(t *testing.T) {
	server := newListServer(t, "192.0.2.0/24")
	r := New(Options{Definitions: listed(t, server.URL), Refresh: true, RefreshInterval: time.Hour})
	if r.Name() != "crawlers" {
		t.Errorf("Name() = %q", r.Name())
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.Identify("ExBot", addr("192.0.2.1")).Status != Verified {
		if time.Now().After(deadline) {
			t.Fatal("the list was not downloaded after the start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStaleListIsReported(t *testing.T) {
	server := newListServer(t, "192.0.2.0/24")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(Options{Definitions: listed(t, server.URL), Refresh: true, RefreshInterval: time.Hour, Now: func() time.Time { return now }})
	r.refreshAll(context.Background())
	now = now.Add(4 * time.Hour)
	h := r.Health()
	if h.State != health.Degraded || !strings.Contains(h.Detail, "out of date") {
		t.Errorf("health = %+v", h)
	}
	// A few missed downloads do no harm: the list stays in use.
	if got := r.Identify("ExBot", addr("192.0.2.1")).Status; got != Verified {
		t.Errorf("with a stale list: %v", got)
	}
	// But a list that was not renewed for more than a week vouches for nobody.
	now = now.Add(8 * 24 * time.Hour)
	if got := r.Identify("ExBot", addr("192.0.2.1")).Status; got != Pending {
		t.Errorf("with a list that is too old: %v", got)
	}
	if h := r.Health(); !strings.Contains(h.Detail, "too old") {
		t.Errorf("health = %+v", h)
	}
}

func TestRedirectsAreChecked(t *testing.T) {
	inner := newListServer(t, "192.0.2.0/24")
	var target atomic.Value
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.Load().(string), http.StatusFound)
	}))
	defer redirector.Close()
	r := New(Options{Definitions: listed(t, redirector.URL+"/list?token=secret"), Refresh: true})

	// To another loopback address: as acceptable as the first one.
	target.Store(inner.URL)
	if !r.refreshAll(context.Background()) {
		t.Fatalf("a harmless redirect was refused: %+v", r.Health())
	}
	// Away from what a list location may be.
	for _, bad := range []string{"http://example.org/list", "file:///etc/passwd", redirector.URL + "/loop"} {
		target.Store(bad)
		if r.refreshAll(context.Background()) {
			t.Errorf("a redirect to %s was followed", bad)
		}
	}
	if h := r.Health(); strings.Contains(h.Detail, "secret") {
		t.Errorf("the query of the list address is shown: %s", h.Detail)
	}
}

// fakeDNS answers from tables.
type fakeDNS struct {
	mu      sync.Mutex
	names   map[string][]string // address -> names
	hosts   map[string][]string // name -> addresses
	broken  bool
	lookups int
}

func (f *fakeDNS) LookupAddr(_ context.Context, a string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.broken {
		return nil, &net.DNSError{Err: "timeout", IsTimeout: true}
	}
	if n, ok := f.names[a]; ok {
		return n, nil
	}
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

func (f *fakeDNS) LookupHost(_ context.Context, h string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasSuffix(h, ".") {
		return nil, &net.DNSError{Err: "the name was not absolute", IsTimeout: true}
	}
	if a, ok := f.hosts[strings.TrimSuffix(h, ".")]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

func TestReverseDNS(t *testing.T) {
	dns := &fakeDNS{
		names: map[string][]string{
			"192.0.2.1":   {"crawl-1.bot.example.org."},
			"192.0.2.2":   {"crawl-2.bot.example.org."},   // name does not resolve back
			"192.0.2.3":   {"crawl.evilbot.example.org."}, // wrong domain
			"192.0.2.4":   {"bot.example.org.evil.test."}, // domain as a prefix
			"192.0.2.5":   {"notbot.example.org."},        // ending without the dot boundary
			"192.0.2.6":   {"bot.example.org."},           // the bare domain itself
			"2001:db8::7": {"CRAWL-7.BOT.EXAMPLE.ORG."},
			"192.0.2.8":   {"other.test.", "crawl-8.bot.example.org."},
		},
		hosts: map[string][]string{
			"crawl-1.bot.example.org":   {"192.0.2.1"},
			"crawl-2.bot.example.org":   {"203.0.113.9"},
			"crawl.evilbot.example.org": {"192.0.2.3"},
			"bot.example.org.evil.test": {"192.0.2.4"},
			"notbot.example.org":        {"192.0.2.5"},
			"bot.example.org":           {"192.0.2.6"},
			"crawl-7.bot.example.org":   {"192.0.2.77", "2001:db8::7"},
			"crawl-8.bot.example.org":   {"192.0.2.8"},
		},
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clock sync.Mutex
	r := New(Options{
		Definitions: defs(t, defsFile(`
  - {name: Bot, class: search-engine, user_agent: ExBot, purpose: p, verify: {reverse_dns: [".bot.example.org"]}}
`)),
		Resolver: dns,
		Now:      func() time.Time { clock.Lock(); defer clock.Unlock(); return now },
	})
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop(context.Background()) }()

	settle := func(client string) Status {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if s := r.Identify("ExBot/1.0", addr(client)).Status; s != Pending {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s is still pending", client)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	// The first request does not wait for DNS.
	if got := r.Identify("ExBot/1.0", addr("192.0.2.1")).Status; got != Pending {
		t.Errorf("the first request was %v, want pending", got)
	}
	want := map[string]Status{
		"192.0.2.1": Verified, "192.0.2.2": Unverified, "192.0.2.3": Unverified, "192.0.2.4": Unverified,
		"192.0.2.5": Unverified, "192.0.2.6": Unverified, "2001:db8::7": Verified, "192.0.2.8": Verified,
		"192.0.2.99": Unverified, // no name at all
	}
	for client, status := range want {
		if got := settle(client); got != status {
			t.Errorf("%s is %v, want %v", client, got, status)
		}
	}

	// Results are remembered: asking again costs no lookup.
	dns.mu.Lock()
	before := dns.lookups
	dns.mu.Unlock()
	for i := 0; i < 50; i++ {
		r.Identify("ExBot/1.0", addr("192.0.2.1"))
		r.Identify("ExBot/1.0", addr("192.0.2.2"))
	}
	dns.mu.Lock()
	if dns.lookups != before {
		t.Errorf("%d more lookups for addresses already known", dns.lookups-before)
	}
	// DNS trouble decides nothing: neither genuine nor impostor.
	dns.broken = true
	dns.mu.Unlock()
	r.Identify("ExBot/1.0", addr("192.0.2.50"))
	time.Sleep(50 * time.Millisecond)
	if got := r.Identify("ExBot/1.0", addr("192.0.2.50")).Status; got != Pending {
		t.Errorf("with DNS down: %v, want pending", got)
	}
	dns.mu.Lock()
	dns.broken = false
	dns.mu.Unlock()

	// A refusal is forgotten after an hour, so a corrected DNS entry takes effect.
	dns.mu.Lock()
	dns.hosts["crawl-2.bot.example.org"] = []string{"192.0.2.2"}
	dns.mu.Unlock()
	clock.Lock()
	now = now.Add(dnsRefusedTTL + time.Minute)
	clock.Unlock()
	if got := settle("192.0.2.2"); got != Verified {
		t.Errorf("after the entry was corrected: %v", got)
	}
}

func TestDNSCacheIsBounded(t *testing.T) {
	r := New(Options{
		Definitions: defs(t, defsFile(`
  - {name: Bot, class: search-engine, user_agent: ExBot, purpose: p, verify: {reverse_dns: [".bot.example.org"]}}
`)),
		Resolver: &fakeDNS{},
	})
	// Not started: nothing drains the queue, as under a flood.
	base := addr("10.0.0.0").As4()
	for i := 0; i < dnsCacheEntries+5000; i++ {
		a := base
		a[1], a[2], a[3] = byte(i>>16), byte(i>>8), byte(i)
		if got := r.Identify("ExBot", netip.AddrFrom4(a)).Status; got != Pending {
			t.Fatalf("request %d was %v", i, got)
		}
	}
	if len(r.dnsOther) > dnsCacheEntries || len(r.dnsJobs) > dnsQueue {
		t.Errorf("cache %d, queue %d", len(r.dnsOther), len(r.dnsJobs))
	}
}

// One IPv6 client owns a whole /64. Its addresses share one entry and one lookup.
func TestIPv6ImpostorsAreKeptPerNetwork(t *testing.T) {
	dns := &fakeDNS{}
	r := New(Options{
		Definitions: defs(t, defsFile(`
  - {name: Bot, class: search-engine, user_agent: ExBot, purpose: p, verify: {reverse_dns: [".bot.example.org"]}}
`)),
		Resolver: dns,
	})
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop(context.Background()) }()
	for i := 0; i < 3000; i++ {
		r.Identify("ExBot", netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, byte(i >> 8), byte(i)}))
	}
	time.Sleep(50 * time.Millisecond)
	if got := r.Identify("ExBot", addr("2001:db8:0:1::ffff:1")).Status; got != Unverified {
		t.Errorf("another address of a refuted network is %v", got)
	}
	dns.mu.Lock()
	defer dns.mu.Unlock()
	r.dnsMu.Lock()
	defer r.dnsMu.Unlock()
	if dns.lookups != 1 || len(r.dnsOther) != 1 {
		t.Errorf("%d lookups, %d entries for one /64", dns.lookups, len(r.dnsOther))
	}
}

// When the tables are full, new lookups still happen: old entries make way.
func TestFullDNSCacheStillVerifies(t *testing.T) {
	dns := &fakeDNS{names: map[string][]string{"192.0.2.1": {"c.bot.example.org."}}, hosts: map[string][]string{"c.bot.example.org": {"192.0.2.1"}}}
	r := New(Options{
		Definitions: defs(t, defsFile(`
  - {name: Bot, class: search-engine, user_agent: ExBot, purpose: p, verify: {reverse_dns: [".bot.example.org"]}}
`)),
		Resolver: dns,
	})
	far := time.Now().Add(time.Hour)
	for i := 0; i < dnsCacheEntries; i++ {
		r.dnsOther[dnsKey{addr: netip.AddrFrom4([4]byte{11, byte(i >> 16), byte(i >> 8), byte(i)})}] = dnsEntry{status: Unverified, expires: far}
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for r.Identify("ExBot", addr("192.0.2.1")).Status != Verified {
		if time.Now().After(deadline) {
			t.Fatal("the genuine crawler is not verified while the table is full of impostors")
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.dnsMu.Lock()
	defer r.dnsMu.Unlock()
	if len(r.dnsOther) > dnsCacheEntries {
		t.Errorf("table grew to %d", len(r.dnsOther))
	}
}

func TestConcurrentUse(t *testing.T) {
	server := newListServer(t, "192.0.2.0/24")
	r := New(Options{
		Definitions: append(listed(t, server.URL), defs(t, defsFile(`
  - {name: DNSBot, class: search-engine, user_agent: DNSBot, purpose: p, verify: {reverse_dns: [".bot.example.org"]}}
`))...),
		Refresh: true, RefreshInterval: time.Hour, Resolver: &fakeDNS{},
	})
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.Identify("ExBot", addr("192.0.2.1"))
				r.Identify("DNSBot", netip.AddrFrom4([4]byte{10, byte(g), byte(i >> 8), byte(i)}))
				r.Health()
				r.Reports()
				if i%100 == 0 {
					r.refreshAll(context.Background())
				}
			}
		}(g)
	}
	wg.Wait()
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHandler(t *testing.T) {
	r := New(Options{Definitions: defs(t, defsFile(`
  - {name: Bot, class: search-engine, user_agent: ExBot, purpose: p, verify: {ranges: ["192.0.2.0/24"]}}
`))})
	r.Identify("ExBot", addr("192.0.2.1"))
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/crawlers", nil))
	var body struct {
		Crawlers []Report `json:"crawlers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Crawlers) != 1 || body.Crawlers[0].Requests.Verified != 1 || body.Crawlers[0].VerifiedBy != "addresses" {
		t.Errorf("body = %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "192.0.2.1") {
		t.Error("the report contains a client address")
	}
}

func BenchmarkIdentify(b *testing.B) {
	all, problems := LoadFS(data.Files, "crawlers")
	if len(problems) > 0 {
		b.Fatal(problems)
	}
	r := New(Options{Definitions: all})
	client := addr("192.0.2.1")
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Identify(ua, client)
	}
}
