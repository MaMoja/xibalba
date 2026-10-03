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

func hit(t *Trap, path, client string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req = req.WithContext(clientip.NewContext(req.Context(), clientip.Info{Client: addr(client)}))
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
	if !strings.HasPrefix(tr.Link(), Prefix) || len(tr.Link()) != len(Prefix)+16 {
		t.Fatalf("link = %q", tr.Link())
	}
	if tr.Caught(addr("203.0.113.5")) {
		t.Fatal("caught before following the link")
	}

	rec := hit(tr, tr.Link(), "203.0.113.5")
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("answer: %d, %v", rec.Code, rec.Header())
	}
	if !tr.Caught(addr("203.0.113.5")) || !tr.Caught(addr("::ffff:203.0.113.5")) {
		t.Error("not caught after following the link")
	}
	if tr.Caught(addr("203.0.113.6")) {
		t.Error("a neighbouring address is caught as well")
	}
	if r := tr.Report(); r.Hits != 1 || r.Clients != 1 {
		t.Errorf("report = %+v", r)
	}

	// Any address under the prefix is the trap, also ones we never handed out.
	hit(tr, Prefix+"anything/else", "2001:db8:1:2::9")
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

func TestLinkDiffersBetweenStarts(t *testing.T) {
	a, b := newTrap(t, Options{}), newTrap(t, Options{})
	if a.Link() == b.Link() {
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
	rec := hit(tr, tr.Link(), "203.0.113.5")
	page := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("status %d, headers %v", rec.Code, rec.Header())
	}
	for _, want := range []string{`<html lang="zxx">`, `content="noindex, nofollow"`, "<p>"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// Every link leads back into the trap, nowhere else.
	links := strings.Split(page, `href="`)[1:]
	if len(links) != mazeLinks {
		t.Fatalf("%d links", len(links))
	}
	for _, l := range links {
		if !strings.HasPrefix(l, Prefix) {
			t.Errorf("a link leaves the trap: %.40s", l)
		}
	}
	if strings.Contains(page, "<script") || strings.Contains(page, "<img") || strings.Contains(page, "http") {
		t.Error("the maze page loads or links something else")
	}
	// The same address gives the same page; another address another page.
	if again := hit(tr, tr.Link(), "203.0.113.5").Body.String(); again != page {
		t.Error("the same address gave a different page")
	}
	if other := hit(tr, Prefix+"other", "203.0.113.5").Body.String(); other == page {
		t.Error("another address gave the same page")
	}
	if len(page) > 8<<10 {
		t.Errorf("page is %d bytes", len(page))
	}
	// Hostile addresses do not end up in the page.
	if evil := hit(tr, Prefix+`"><script>alert(1)</script>`, "203.0.113.5").Body.String(); strings.Contains(evil, "alert") {
		t.Error("the requested address is echoed into the page")
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
		writeMaze(&buf, "/.xibalba/trap/abcdef")
	}
}
