package trap

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
)

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func request(path, client string) *http.Request {
	req := httptest.NewRequest("GET", path, nil)
	return req.WithContext(clientip.NewContext(req.Context(), clientip.Info{Client: addr(client)}))
}

// linkFor returns the link a page shown to client would carry.
func linkFor(t *Trap, client string) string { return t.Link(request("/", client)) }

func hit(t *Trap, path, client string, headers ...string) *httptest.ResponseRecorder {
	req := request(path, client)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	t.Handler().ServeHTTP(rec, req)
	return rec
}

func newTrap(t *testing.T, opts Options) *Trap {
	t.Helper()
	tr, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestFollowingTheLinkIsRemembered(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	tr := newTrap(t, Options{Remember: time.Hour, MaxClients: 100, Now: func() time.Time { return now }})
	link := linkFor(tr, "203.0.113.5")
	if !strings.HasPrefix(link, Prefix) || len(link) != len(Prefix)+linkLen {
		t.Fatalf("link = %q", link)
	}
	if tr.Caught(addr("203.0.113.5")) {
		t.Fatal("caught before following the link")
	}

	rec := hit(tr, link, "203.0.113.5")
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("answer: %d, %v", rec.Code, rec.Header())
	}
	if !tr.Caught(addr("203.0.113.5")) || !tr.Caught(addr("::ffff:203.0.113.5")) {
		t.Error("not caught after following the link")
	}
	if tr.Caught(addr("203.0.113.6")) {
		t.Error("a neighbouring address is caught as well")
	}
	if r := tr.Report(); r.Hits != 1 || r.Clients != 1 || r.Ignored != 0 {
		t.Errorf("report = %+v", r)
	}

	// An IPv6 client is remembered, and gets its link, by its /64.
	v6 := linkFor(tr, "2001:db8:1:2::9")
	if v6 != linkFor(tr, "2001:db8:1:2:ffff::1") || v6 == linkFor(tr, "2001:db8:1:3::9") {
		t.Error("IPv6 links are not per /64")
	}
	hit(tr, v6, "2001:db8:1:2:aaaa::7")
	if !tr.Caught(addr("2001:db8:1:2:ffff::1")) || tr.Caught(addr("2001:db8:1:3::1")) {
		t.Error("an IPv6 client is not remembered by its /64")
	}

	now = now.Add(61 * time.Minute)
	if tr.Caught(addr("203.0.113.5")) {
		t.Error("still caught after the time is up")
	}
	if r := tr.Report(); r.Clients != 0 {
		t.Errorf("report after the time is up = %+v", r)
	}
	if tr.Caught(netip.Addr{}) {
		t.Error("the invalid address is caught")
	}
}

// Nobody can get somebody else caught. Another website can make a visitor's
// browser request any address, but it cannot know the visitor's link.
func TestOnlyTheClientALinkWasMadeForIsCaught(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	tr := newTrap(t, Options{Remember: time.Hour, MaxClients: 100, Maze: true, Now: func() time.Time { return now }})
	victim, attacker := "203.0.113.5", "198.51.100.66"
	attackersLink := linkFor(tr, attacker)
	victimsLink := linkFor(tr, victim)
	if attackersLink == victimsLink {
		t.Fatal("two clients got the same link")
	}

	attempts := []struct{ name, path string }{
		{"the bare prefix", Prefix},
		{"any address under the prefix", Prefix + "x"},
		{"the attacker's own link", attackersLink},
		{"the attacker's own maze address", attackersLink + "/abc"},
		{"a link of the right length", Prefix + strings.Repeat("0", linkLen)},
		{"the victim's link with a letter changed", victimsLink[:len(victimsLink)-1] + "x"},
		{"the victim's link with something appended", victimsLink + "x"},
		{"the victim's link with a very long tail", victimsLink + "/" + strings.Repeat("a", 200)},
		{"the victim's link in upper case", strings.ToUpper(victimsLink)},
	}
	for _, a := range attempts {
		rec := hit(tr, a.path, victim)
		if rec.Code != http.StatusNotFound || tr.Caught(addr(victim)) {
			t.Fatalf("%s: status %d, victim caught: %v", a.name, rec.Code, tr.Caught(addr(victim)))
		}
	}
	// Even the right link does nothing when the browser says another site sent it.
	if hit(tr, victimsLink, victim, "Sec-Fetch-Site", "cross-site"); tr.Caught(addr(victim)) {
		t.Fatal("caught by a request another website caused")
	}
	if r := tr.Report(); r.Hits != 0 || r.Ignored != uint64(len(attempts))+1 {
		t.Errorf("report = %+v", r)
	}

	// The link works for its own client, also the day after, and not later.
	now = now.Add(24 * time.Hour)
	if hit(tr, victimsLink+"/abc", victim); !tr.Caught(addr(victim)) {
		t.Error("yesterday's link does not work")
	}
	other := "203.0.113.77"
	old := linkFor(tr, other)
	now = now.Add(48 * time.Hour)
	if hit(tr, old, other); tr.Caught(addr(other)) {
		t.Error("a link from three days ago still works")
	}
	// Without a known client there is no link.
	if got := tr.Link(httptest.NewRequest("GET", "/", nil)); got != "" {
		t.Errorf("link without a client = %q", got)
	}
}

func TestLinksDifferBetweenStarts(t *testing.T) {
	a, b := newTrap(t, Options{}), newTrap(t, Options{})
	if linkFor(a, "203.0.113.5") == linkFor(b, "203.0.113.5") {
		t.Error("two traps hand out the same link")
	}
}

func TestTableIsBounded(t *testing.T) {
	tr := newTrap(t, Options{Remember: time.Hour, MaxClients: 500})
	for i := 0; i < 5000; i++ {
		tr.catch(netip.AddrFrom4([4]byte{11, 0, byte(i >> 8), byte(i)}))
	}
	if n := len(tr.clients); n > 500 {
		t.Errorf("%d clients remembered, limit 500", n)
	}
	tr.catch(addr("203.0.113.5"))
	if !tr.Caught(addr("203.0.113.5")) {
		t.Error("a full table stopped the trap from working")
	}
}

func TestMaze(t *testing.T) {
	tr := newTrap(t, Options{Remember: time.Hour, MaxClients: 100, Maze: true})
	client := "203.0.113.5"
	link := linkFor(tr, client)
	rec := hit(tr, link, client)
	page := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("status %d, headers %v", rec.Code, rec.Header())
	}
	for _, want := range []string{`<html lang="zxx">`, `content="noindex, nofollow"`, "<p>"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// Every link leads deeper into this client's maze, nowhere else, and works.
	links := strings.Split(page, `href="`)[1:]
	if len(links) != mazeLinks {
		t.Fatalf("%d links", len(links))
	}
	for _, l := range links {
		next := l[:strings.Index(l, `"`)]
		if !strings.HasPrefix(next, link+"/") {
			t.Errorf("a link leaves the client's maze: %s", next)
		}
		if deeper := hit(tr, next, client); deeper.Code != 200 || deeper.Body.String() == page {
			t.Errorf("%s: status %d", next, deeper.Code)
		}
	}
	if strings.Contains(page, "<script") || strings.Contains(page, "<img") || strings.Contains(page, "http") {
		t.Error("the maze page loads or links something else")
	}
	if again := hit(tr, link, client).Body.String(); again != page {
		t.Error("the same address gave a different page")
	}
	if len(page) > 8<<10 {
		t.Errorf("page is %d bytes", len(page))
	}
	// What follows the link is not echoed into the page.
	if evil := hit(tr, link+`/"><script>alert(1)`, client).Body.String(); strings.Contains(evil, "alert") {
		t.Error("the requested address is echoed into the page")
	}
	// Another client does not get into this maze.
	if rec := hit(tr, link+"/abc", "198.51.100.66"); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger in the maze: %d", rec.Code)
	}
}

func TestStartStopAndConcurrentUse(t *testing.T) {
	tr := newTrap(t, Options{Remember: time.Hour, MaxClients: 300, Maze: true})
	if err := tr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				a := netip.AddrFrom4([4]byte{11, byte(g), byte(i >> 8), byte(i)})
				tr.catch(a)
				tr.Caught(a)
				tr.Report()
			}
		}(g)
	}
	wg.Wait()
	if err := tr.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tr.Name() != "trap" {
		t.Errorf("Name() = %q", tr.Name())
	}
}

func BenchmarkMaze(b *testing.B) {
	var buf bytes.Buffer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		writeMaze(&buf, "/.xibalba/trap/abcdef", "/.xibalba/trap/abcdef/x")
	}
}
