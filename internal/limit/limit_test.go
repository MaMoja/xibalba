package limit

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func newClock() *clock { return &clock{now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)} }

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestLimit(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 5, Per: time.Minute, Action: "challenge"}}, MaxClients: 1000, Now: c.Now})
	for i := 1; i <= 5; i++ {
		if v := l.Count(addr("192.0.2.1")); v.Over {
			t.Fatalf("request %d is over the limit of 5", i)
		}
	}
	v := l.Count(addr("192.0.2.1"))
	if !v.Over || v.Action != "challenge" || v.RetryAfter <= 0 || v.RetryAfter > 2*time.Minute {
		t.Fatalf("request 6: %+v", v)
	}
	// Another client is not affected.
	if l.Count(addr("192.0.2.2")).Over {
		t.Error("a different address is over the limit")
	}
	// After the period has fully passed, the client starts afresh.
	c.advance(2 * time.Minute)
	if l.Count(addr("192.0.2.1")).Over {
		t.Error("still over the limit two periods later")
	}
}

// Retry-After must be the moment at which trying again really helps.
func TestRetryAfterIsWhenTheClientIsBelowTheLimitAgain(t *testing.T) {
	for _, sent := range []int{6, 11, 50, 3000} {
		c := newClock()
		l := New(Options{Windows: []Window{{Requests: 5, Per: time.Minute, Action: "deny"}}, MaxClients: 1000, Now: c.Now})
		c.advance(20 * time.Second)
		var v Verdict
		for i := 0; i < sent; i++ {
			v = l.Count(addr("192.0.2.1"))
		}
		if !v.Over || v.RetryAfter <= 0 || v.RetryAfter > 2*time.Minute || (sent == 3000 && v.RetryAfter < 90*time.Second) {
			t.Fatalf("%d sent: %+v", sent, v)
		}
		// At that moment the next request is let through, although it is
		// counted as well.
		c.advance(v.RetryAfter + time.Second)
		if got := l.Count(addr("192.0.2.1")); got.Over {
			t.Errorf("%d sent: still over the limit %v after the announced %v", sent, got.RetryAfter, v.RetryAfter)
		}
	}
}

// A client must not get twice the limit by sending half before and half
// after the boundary between two periods.
func TestBoundaryCannotBeStraddled(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 100, Per: time.Minute, Action: "deny"}}, MaxClients: 1000, Now: c.Now})
	c.advance(59 * time.Second) // end of a period
	for i := 0; i < 100; i++ {
		l.Count(addr("192.0.2.1"))
	}
	c.advance(2 * time.Second) // start of the next
	passed := 0
	for i := 0; i < 100; i++ {
		if !l.Count(addr("192.0.2.1")).Over {
			passed++
		}
	}
	if passed > 5 {
		t.Errorf("%d more requests passed right after the boundary", passed)
	}
}

func TestStrictestActionWins(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{
		{Requests: 2, Per: time.Minute, Action: "challenge"},
		{Requests: 4, Per: time.Hour, Action: "deny"},
		{Requests: 3, Per: 10 * time.Minute, Action: "challenge"},
	}, MaxClients: 1000, Now: c.Now})
	var got []string
	for i := 0; i < 6; i++ {
		got = append(got, l.Count(addr("192.0.2.1")).Action)
	}
	if strings.Join(got, ",") != ",,challenge,challenge,deny,deny" {
		t.Errorf("actions = %v", got)
	}
}

func TestWhatCountsAsOneClient(t *testing.T) {
	tests := []struct {
		name      string
		byNetwork bool
		a, b      string
		same      bool
	}{
		{"same IPv4 address", false, "192.0.2.1", "192.0.2.1", true},
		{"neighbouring IPv4 addresses", false, "192.0.2.1", "192.0.2.2", false},
		{"IPv4 and its IPv6-mapped form", false, "192.0.2.1", "::ffff:192.0.2.1", true},
		{"two addresses of one IPv6 /64", false, "2001:db8:1:2::1", "2001:db8:1:2:ffff::9", true},
		{"two IPv6 /64", false, "2001:db8:1:2::1", "2001:db8:1:3::1", false},
		{"by network: one IPv4 /24", true, "192.0.2.1", "192.0.2.200", true},
		{"by network: two IPv4 /24", true, "192.0.2.1", "192.0.3.1", false},
		{"by network: one IPv6 /48", true, "2001:db8:1:2::1", "2001:db8:1:ffff::1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(Options{Windows: []Window{{Requests: 1, Per: time.Hour, Action: "deny"}}, ByNetwork: tt.byNetwork, MaxClients: 1000})
			l.Count(addr(tt.a))
			if got := l.Count(addr(tt.b)).Over; got != tt.same {
				t.Errorf("counted as one client: %v, want %v", got, tt.same)
			}
		})
	}
}

func TestExemptAndInvalidAddresses(t *testing.T) {
	l := New(Options{
		Windows:    []Window{{Requests: 1, Per: time.Hour, Action: "deny"}},
		Exempt:     []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("2001:db8::/32")},
		MaxClients: 1000,
	})
	for i := 0; i < 10; i++ {
		for _, a := range []netip.Addr{addr("192.0.2.77"), addr("::ffff:192.0.2.77"), addr("2001:db8::1"), {}} {
			if l.Count(a).Over {
				t.Fatalf("%v was limited", a)
			}
		}
	}
	r := l.Report()
	if r.Clients != 0 || r.Exempt != 30 {
		t.Errorf("report = %+v", r)
	}
}

// A flood from many addresses must not grow the table without bound, and
// must not stop the limiter from working.
func TestTableIsBounded(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 3, Per: time.Hour, Action: "deny"}}, MaxClients: 3200, Now: c.Now})
	for i := 0; i < 50000; i++ {
		l.Count(netip.AddrFrom4([4]byte{11, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	if n := l.Report().Clients; n > 3200 {
		t.Errorf("%d clients tracked, limit 3200", n)
	}
	for i := 0; i < 3; i++ {
		l.Count(addr("192.0.2.1"))
	}
	if !l.Count(addr("192.0.2.1")).Over {
		t.Error("the limiter stopped limiting when the table was full")
	}
}

func TestSweepForgetsIdleClients(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 3, Per: time.Minute, Action: "deny"}}, MaxClients: 1000, Now: c.Now})
	l.Count(addr("192.0.2.1"))
	c.advance(90 * time.Second)
	l.Count(addr("192.0.2.2"))
	l.Sweep()
	if n := l.Report().Clients; n != 2 {
		t.Fatalf("after 1.5 periods: %d clients, want 2", n)
	}
	c.advance(60 * time.Second)
	l.Sweep()
	if n := l.Report().Clients; n != 1 {
		t.Errorf("after 2.5 periods: %d clients, want 1", n)
	}
}

func TestReportHoldsNoAddress(t *testing.T) {
	l := New(Options{Windows: []Window{{Requests: 1, Per: time.Minute, Action: "deny"}}, MaxClients: 1000})
	l.Count(addr("192.0.2.1"))
	l.Count(addr("192.0.2.1"))
	rec := httptest.NewRecorder()
	l.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/limits", nil))
	body := rec.Body.String()
	if strings.Contains(body, "192.0.2") || !strings.Contains(body, `"requests_over_limit": 1`) || !strings.Contains(body, `"clients": 1`) {
		t.Errorf("body = %s", body)
	}
}

func TestConcurrentUse(t *testing.T) {
	l := New(Options{Windows: []Window{{Requests: 100, Per: time.Minute, Action: "deny"}}, MaxClients: 640})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				l.Count(netip.AddrFrom4([4]byte{11, byte(g), byte(i >> 8), byte(i)}))
				if i%500 == 0 {
					l.Sweep()
					l.Report()
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestCountDoesNotAllocateForAKnownClient(t *testing.T) {
	l := New(Options{Windows: []Window{{Requests: 1000000, Per: time.Minute, Action: "deny"}, {Requests: 1000000, Per: time.Hour, Action: "deny"}}, MaxClients: 1000})
	a := addr("192.0.2.1")
	l.Count(a)
	if n := testing.AllocsPerRun(100, func() { l.Count(a) }); n != 0 {
		t.Errorf("Count allocates %v times", n)
	}
}

func BenchmarkCount(b *testing.B) {
	l := New(Options{Windows: []Window{{Requests: 300, Per: 10 * time.Minute, Action: "challenge"}, {Requests: 5000, Per: 24 * time.Hour, Action: "deny"}}, MaxClients: 100000})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		l.Count(netip.AddrFrom4([4]byte{11, 0, byte(i >> 8), byte(i)}))
	}
}

// read stands for one request for a page that the website answers with a
// page: it is checked, and if let through, recorded.
func read(l *Limiter, a netip.Addr, path, query string) Verdict {
	v := l.Count(a)
	if !v.Over || v.Action != "deny" {
		l.Page(a, path, query)
	}
	return v
}

// A person reads a few pages, each with many images and scripts. A crawler
// asks for one page after the other.
func TestLimitOnDifferentPages(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 30, Per: 10 * time.Minute, Action: "challenge", Pages: true}}, MaxClients: 1000, Now: c.Now})
	reader, crawler := addr("192.0.2.1"), addr("192.0.2.2")

	// Five pages with forty other requests each, read again and again.
	for round := 0; round < 20; round++ {
		for page := 0; page < 5; page++ {
			if read(l, reader, "/artikel/"+string(rune('a'+page)), "").Over {
				t.Fatalf("the reader is over the limit in round %d", round)
			}
			for asset := 0; asset < 40; asset++ {
				if l.Count(reader).Over { // not a page: only checked
					t.Fatal("other requests pushed the reader over the limit")
				}
			}
		}
	}

	// One different page after the other. The estimate is not exact: the
	// crawler must be stopped somewhere near the limit.
	stoppedAt := 0
	for i := 1; i <= 100; i++ {
		if read(l, crawler, "/artikel", "id="+string(rune('0'+i/10))+string(rune('0'+i%10))).Over {
			stoppedAt = i
			break
		}
	}
	if stoppedAt < 26 || stoppedAt > 40 {
		t.Errorf("the crawler was stopped at page %d, limit 30", stoppedAt)
	}
	// Once over, its other requests are stopped as well.
	if !l.Count(crawler).Over {
		t.Error("another request of a client over the page limit is let through")
	}
	// The pages are forgotten after two periods.
	c.advance(21 * time.Minute)
	if read(l, crawler, "/artikel", "id=1").Over {
		t.Error("still over the limit two periods later")
	}
	if r := l.Report(); r.Limits[0].Count != "pages" {
		t.Errorf("report = %+v", r)
	}
}

// Reading the same pages again in the next period must not count them twice.
func TestTheSamePagesAcrossPeriodsCountOnce(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 60, Per: 10 * time.Minute, Action: "deny", Pages: true}}, MaxClients: 1000, Now: c.Now})
	a := addr("192.0.2.1")
	for round := 0; round < 6; round++ {
		for page := 0; page < 40; page++ {
			if read(l, a, "/akte/"+string(rune('A'+page)), "").Over {
				t.Fatalf("round %d, page %d: over a limit of 60 with 40 different pages", round, page)
			}
		}
		c.advance(10 * time.Minute)
	}
	// Forty other pages in the next period do add up.
	over := false
	for page := 0; page < 40; page++ {
		over = over || read(l, a, "/neu/"+string(rune('A'+page)), "").Over
	}
	if !over {
		t.Error("80 different pages within the sliding period stayed under a limit of 60")
	}
}

// A limit of N lets N pages through and stops at the next.
func TestSmallPageLimitsAreExact(t *testing.T) {
	for _, n := range []int{1, 2, 5, 10} {
		l := New(Options{Windows: []Window{{Requests: n, Per: time.Hour, Action: "deny", Pages: true}}, MaxClients: 1000})
		stopped := 0
		for i := 1; i <= n+5 && stopped == 0; i++ {
			if read(l, addr("192.0.2.1"), "/p/"+string(rune('a'+i)), "").Over {
				stopped = i
			}
		}
		// The page after the Nth is still served (it cannot be known to
		// be new before the website answers); the one after that is not.
		if stopped != n+2 {
			t.Errorf("limit %d: stopped at page %d", n, stopped)
		}
	}
}

func TestEstimateOfDifferentPages(t *testing.T) {
	for _, n := range []int{1, 10, 50, 100, 250, 500} {
		l := New(Options{Windows: []Window{{Requests: MaxPages, Per: time.Hour, Action: "deny", Pages: true}}, MaxClients: 1000})
		for i := 0; i < n; i++ {
			l.Page(addr("192.0.2.1"), "/p/"+time.Duration(i).String(), "")
		}
		var got float64
		for i := range l.shards {
			for _, c := range l.shards[i].clients {
				got = distinct(&c.sketch.current[0])
			}
		}
		if got < float64(n)*0.8-1 || got > float64(n)*1.2+1 {
			t.Errorf("%d different pages estimated as %.0f", n, got)
		}
	}
	// A full field counts as more than any field with a bit clear.
	full := [sketchBits / 64]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
	almost := full
	almost[0] &^= 1
	if distinct(&full) <= distinct(&almost) {
		t.Errorf("full field %.0f, one bit clear %.0f", distinct(&full), distinct(&almost))
	}
}

func TestRequestsWindowsAreUnchangedByPageWindows(t *testing.T) {
	l := New(Options{Windows: []Window{
		{Requests: 3, Per: time.Hour, Action: "deny"},
		{Requests: 100, Per: time.Hour, Action: "challenge", Pages: true},
	}, MaxClients: 1000})
	for i := 0; i < 3; i++ {
		if l.Count(addr("192.0.2.1")).Over {
			t.Fatal("over too early")
		}
	}
	if v := l.Count(addr("192.0.2.1")); !v.Over || v.Action != "deny" {
		t.Errorf("verdict = %+v", v)
	}
}

func TestPagesOfExemptAndInvalidAddressesAreNotRecorded(t *testing.T) {
	l := New(Options{Windows: []Window{{Requests: 1, Per: time.Hour, Action: "deny", Pages: true}},
		Exempt: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, MaxClients: 1000})
	for i := 0; i < 10; i++ {
		l.Page(addr("192.0.2.7"), "/p/"+string(rune('a'+i)), "")
		l.Page(netip.Addr{}, "/p", "")
	}
	if r := l.Report(); r.Clients != 0 || r.Exempt != 0 {
		t.Errorf("report = %+v", r)
	}
	// Without a limit on pages, Page does nothing at all.
	plain := New(Options{Windows: []Window{{Requests: 1, Per: time.Hour, Action: "deny"}}, MaxClients: 1000})
	plain.Page(addr("203.0.113.1"), "/p", "")
	if plain.Report().Clients != 0 {
		t.Error("Page created an entry although no limit counts pages")
	}
}

func TestPageDoesNotAllocateForAKnownClient(t *testing.T) {
	l := New(Options{Windows: []Window{{Requests: 400, Per: time.Minute, Action: "deny", Pages: true}}, MaxClients: 1000})
	a := addr("192.0.2.1")
	l.Count(a)
	if n := testing.AllocsPerRun(100, func() { l.Count(a); l.Page(a, "/artikel/42", "seite=2") }); n != 0 {
		t.Errorf("Count and Page allocate %v times", n)
	}
}

// A client that passes the security check and carries on is refused at deny_at.
func TestDenyAt(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 2, Per: time.Minute, Action: "challenge", DenyAt: 4}}, MaxClients: 1000, Now: c.Now})
	var got []string
	var last Verdict
	for i := 0; i < 6; i++ {
		last = l.Count(addr("192.0.2.1"))
		got = append(got, last.Action)
	}
	if strings.Join(got, ",") != ",,challenge,challenge,deny,deny" {
		t.Errorf("actions = %v", got)
	}
	if last.RetryAfter <= 0 || last.RetryAfter > 2*time.Minute {
		t.Errorf("retry after = %s", last.RetryAfter)
	}
	// Waiting that long is enough to be let near the check again.
	c.advance(last.RetryAfter)
	if v := l.Count(addr("192.0.2.1")); v.Action == "deny" {
		t.Errorf("still refused after waiting: %+v", v)
	}
	r := l.Report().Limits[0]
	if r.DenyAt != 4 || r.Denied != 2 || r.Over != 5 {
		t.Errorf("report = %+v", r)
	}
}

func TestWithoutDenyAtAChallengeLimitNeverRefuses(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{{Requests: 2, Per: time.Minute, Action: "challenge"}}, MaxClients: 1000, Now: c.Now})
	for i := 0; i < 1000; i++ {
		if v := l.Count(addr("192.0.2.1")); v.Action == "deny" {
			t.Fatalf("request %d refused", i+1)
		}
	}
}

// Over two limits with the same action, the wait is the longer one.
func TestRetryAfterIsTheLongestOfTheExceededLimits(t *testing.T) {
	c := newClock()
	l := New(Options{Windows: []Window{
		{Requests: 2, Per: time.Minute, Action: "deny"},
		{Requests: 3, Per: 24 * time.Hour, Action: "deny"},
	}, MaxClients: 1000, Now: c.Now})
	var v Verdict
	for i := 0; i < 6; i++ {
		v = l.Count(addr("192.0.2.1"))
	}
	if v.Action != "deny" || v.RetryAfter < time.Hour {
		t.Errorf("verdict = %+v", v)
	}
}
