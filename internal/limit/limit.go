// Package limit counts requests per client and says when a client has sent
// more than the site owner allows in a period of time.
//
// A limit counts either every request or the different pages a client asks
// for. A person reads a handful of pages in a few minutes; a crawler walks
// through hundreds. What a page is, is for the caller to say (the gate goes
// by what the website answered); the limiter is told with Page.
//
// A client is an IPv4 address or an IPv6 /64 (one IPv6 connection owns a
// whole /64), or, if configured, the wider network around it. Counts are
// kept in memory only, for as long as the longest period, and never written
// anywhere. Addresses on the exempt list are not counted at all.
package limit

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/maphash"
	"math"
	"math/bits"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// MaxWindows is how many limits may be configured.
const MaxWindows = 4

// Window is one limit: at most Requests per Per.
type Window struct {
	// Requests is how many requests a client may send within Per.
	Requests int
	// Per is the period.
	Per time.Duration
	// Action is what happens to further requests: "challenge" or "deny".
	Action string
	// Pages counts the different pages asked for instead of all requests.
	// The count is an estimate, good up to MaxPages.
	Pages bool
	// DenyAt turns a "challenge" window into "deny" once the client is above
	// this number within Per. It bounds a client that passes the security
	// check and carries on. Zero: never.
	DenyAt int
}

// MaxPages is the largest limit a window that counts pages can have. The
// different pages of a client are estimated from a small bit field; beyond
// this number the estimate is no longer good.
const MaxPages = 500

// sketchBits is the size of the bit field per client, window and period.
const sketchBits = 256

// ID names the window in reports, such as "300/10m0s".
func (w Window) ID() string {
	if w.Pages {
		return fmt.Sprintf("%d pages/%s", w.Requests, w.Per)
	}
	return fmt.Sprintf("%d/%s", w.Requests, w.Per)
}

// Options configures a Limiter.
type Options struct {
	// Windows are the limits. A client that exceeds several gets the
	// strictest action.
	Windows []Window
	// ByNetwork counts a whole network (IPv4 /24, IPv6 /48) as one client
	// instead of a single address (IPv4 address, IPv6 /64).
	ByNetwork bool
	// Exempt lists addresses and networks that are never counted.
	Exempt []netip.Prefix
	// MaxClients is how many clients are tracked at most. When the table
	// is full, old entries make way.
	MaxClients int
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Verdict is what the limiter says about one request.
type Verdict struct {
	// Over reports whether the client is above a limit.
	Over bool
	// Action is "challenge" or "deny" if Over.
	Action string
	// RetryAfter is how long until the exceeded period ends.
	RetryAfter time.Duration
}

const shards = 32

// Limiter counts requests. It is safe for use from many goroutines.
type Limiter struct {
	opts     Options
	perShard int
	seed     maphash.Seed
	shards   [shards]shard
	exceeded []atomic.Uint64 // per window: requests refused or challenged
	stepped  []atomic.Uint64 // per window: of those, refused because of DenyAt
	pages    bool            // some window counts pages
	exempted atomic.Uint64

	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements lifecycle.Component.
func (l *Limiter) Name() string { return "limits" }

// Start begins forgetting idle clients in the background.
func (l *Limiter) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel, l.done = cancel, make(chan struct{})
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.Sweep()
			}
		}
	}()
	return nil
}

// Stop ends the background work.
func (l *Limiter) Stop(ctx context.Context) error {
	if l.cancel == nil {
		return nil
	}
	l.cancel()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type shard struct {
	mu      sync.Mutex
	clients map[netip.Addr]*counters
	swept   time.Time
}

// counters holds, per window, the count of the current period and of the one
// before. From the two a sliding count is estimated, so a client cannot send
// twice the limit by straddling the boundary between two periods.
type counters struct {
	period   [MaxWindows]int64 // number of the current period
	current  [MaxWindows]uint32
	previous [MaxWindows]uint32
	seen     time.Time
	// sketch holds, for windows that count pages, which pages were asked
	// for. It is only there if such a window is configured.
	sketch *sketches
}

// sketches holds one bit field per window for the current period and one
// for the period before. A page sets the bit its address hashes to; the
// number of different pages is estimated from how many bits are still clear.
type sketches struct {
	current, previous [MaxWindows][sketchBits / 64]uint64
}

// distinct estimates how many different pages set the bits of a field.
func distinct(field *[sketchBits / 64]uint64) float64 {
	set := 0
	for _, word := range field {
		set += bits.OnesCount64(word)
	}
	switch set {
	case 0:
		return 0
	case sketchBits:
		return 8 * sketchBits // full: far more than can be told, and more than any field with a bit clear
	}
	return -sketchBits * math.Log(float64(sketchBits-set)/sketchBits)
}

// New returns a Limiter. The options must have been validated (see
// internal/config): one to MaxWindows windows with positive values.
func New(opts Options) *Limiter {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxClients < shards {
		opts.MaxClients = shards
	}
	if len(opts.Windows) > MaxWindows {
		opts.Windows = opts.Windows[:MaxWindows]
	}
	pages := false
	for _, w := range opts.Windows {
		pages = pages || w.Pages
	}
	l := &Limiter{pages: pages, opts: opts, perShard: opts.MaxClients / shards, seed: maphash.MakeSeed(), exceeded: make([]atomic.Uint64, len(opts.Windows)), stepped: make([]atomic.Uint64, len(opts.Windows))}
	for i := range l.shards {
		l.shards[i].clients = map[netip.Addr]*counters{}
	}
	return l
}

// key returns what a client address is counted under.
func (l *Limiter) key(addr netip.Addr) netip.Addr {
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	if l.opts.ByNetwork {
		bits = 24
		if addr.Is6() {
			bits = 48
		}
	}
	p, err := addr.Prefix(bits)
	if err != nil {
		return addr
	}
	return p.Addr()
}

// Count records one request from addr and says whether the client is over a
// limit, on requests or on pages. An invalid or exempt address is never
// counted. Pages are recorded separately, with Page.
func (l *Limiter) Count(addr netip.Addr) Verdict {
	key, s, ok := l.locate(addr, true)
	if !ok {
		return Verdict{}
	}
	now := l.opts.Now()

	s.mu.Lock()
	c := l.client(s, key, now)
	var verdict Verdict
	worst, steppedUp := -1, false
	for i, w := range l.opts.Windows {
		elapsed := l.roll(c, i, w, now)
		var current, previous, limit float64
		if w.Pages {
			// Pages of the previous period that were read again in this
			// one are the same pages: count what the previous period adds.
			both := c.sketch.current[i]
			for j := range both {
				both[j] |= c.sketch.previous[i][j]
			}
			current = distinct(&c.sketch.current[i])
			previous = math.Max(0, distinct(&both)-current)
			// The estimate for a few pages lies a little above their
			// number; half a page of room keeps a limit of N from
			// stopping the Nth page.
			limit = float64(w.Requests) + 0.5
		} else {
			if c.current[i] < 1<<31 {
				c.current[i]++
			}
			current, previous, limit = float64(c.current[i]), float64(c.previous[i]), float64(w.Requests)
		}
		// Share of the previous period that still lies within the last Per.
		remaining := float64(int64(w.Per)-elapsed) / float64(w.Per)
		if estimate := current + previous*remaining; estimate > limit {
			action, step := w.Action, false
			if w.DenyAt > 0 && action != "deny" && estimate > limit+float64(w.DenyAt-w.Requests) {
				action, step = "deny", true
			}
			at := w
			if step {
				at.Requests = w.DenyAt // the wait ends when the client is below DenyAt again
			}
			wait := retryAfter(at, current, previous, elapsed)
			// The strictest action wins; among limits with the same
			// action, the one the client has to wait longest for.
			if worst < 0 || (action == "deny" && verdict.Action != "deny") || (action == verdict.Action && wait > verdict.RetryAfter) {
				worst, steppedUp = i, step
				verdict = Verdict{Over: true, Action: action, RetryAfter: wait}
			}
		}
	}
	s.mu.Unlock()

	if worst >= 0 {
		l.exceeded[worst].Add(1)
		if steppedUp {
			l.stepped[worst].Add(1)
		}
	}
	return verdict
}

// Page records that the client was given the page at path?query. It only
// matters to limits that count pages.
func (l *Limiter) Page(addr netip.Addr, path, query string) {
	if !l.pages {
		return
	}
	key, s, ok := l.locate(addr, false)
	if !ok {
		return
	}
	var h maphash.Hash
	h.SetSeed(l.seed)
	_, _ = h.WriteString(path)
	_ = h.WriteByte('?')
	_, _ = h.WriteString(query)
	bit := h.Sum64() % sketchBits
	now := l.opts.Now()

	s.mu.Lock()
	c := l.client(s, key, now)
	for i, w := range l.opts.Windows {
		if w.Pages {
			l.roll(c, i, w, now)
			c.sketch.current[i][bit/64] |= 1 << (bit % 64)
		}
	}
	s.mu.Unlock()
}

// locate returns what addr is counted under and the shard that holds it. ok
// is false for an address that is not counted: invalid, exempt, or no
// limits. note says whether a request from an exempt address is noted.
func (l *Limiter) locate(addr netip.Addr, note bool) (key netip.Addr, s *shard, ok bool) {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() || len(l.opts.Windows) == 0 {
		return key, nil, false
	}
	for _, p := range l.opts.Exempt {
		if p.Contains(addr) {
			if note {
				l.exempted.Add(1)
			}
			return key, nil, false
		}
	}
	key = l.key(addr)
	raw := key.As16()
	return key, &l.shards[maphash.Bytes(l.seed, raw[:])%shards], true
}

// client returns the counters of a client, making room and creating them if
// need be. The caller holds the shard's lock.
func (l *Limiter) client(s *shard, key netip.Addr, now time.Time) *counters {
	c := s.clients[key]
	if c == nil {
		l.makeRoom(s, now)
		c = &counters{}
		if l.pages {
			c.sketch = &sketches{}
		}
		s.clients[key] = c
	}
	c.seen = now
	return c
}

// roll moves the counters of window i on to the period that now lies in and
// returns how far that period has run.
func (l *Limiter) roll(c *counters, i int, w Window, now time.Time) (elapsed int64) {
	period := now.UnixNano() / int64(w.Per)
	switch c.period[i] {
	case period:
	case period - 1:
		c.previous[i], c.current[i] = c.current[i], 0
		if w.Pages {
			c.sketch.previous[i], c.sketch.current[i] = c.sketch.current[i], [sketchBits / 64]uint64{}
		}
	default:
		c.previous[i], c.current[i] = 0, 0
		if w.Pages {
			c.sketch.previous[i], c.sketch.current[i] = [sketchBits / 64]uint64{}, [sketchBits / 64]uint64{}
		}
	}
	c.period[i] = period
	return now.UnixNano() - period*int64(w.Per)
}

// retryAfter says how long a client that sends nothing more stays over a
// limit. current and previous are its counts, elapsed is how far the current
// period has run.
func retryAfter(w Window, current, previous float64, elapsed int64) time.Duration {
	per := float64(w.Per)
	target := float64(w.Requests) - 1 // leave room for the request that tries again
	left := per - float64(elapsed)    // until the current period ends
	if current <= target && previous > 0 {
		// The share of the previous period still counted has to shrink
		// until current + previous*share fits.
		share := (target - current) / previous
		return time.Duration(left - share*per)
	}
	// The current count alone is too much. It becomes the previous one
	// when the period ends, and then has to fade until it fits.
	if target <= 0 || current <= 0 {
		return time.Duration(left + per)
	}
	return time.Duration(left + per*(1-target/current))
}

// makeRoom keeps a shard within its share of MaxClients. Entries not seen
// for the longest period are removed, at most once a second; if the shard is
// still full, any entry makes way. Forgetting a client only resets its
// count. The caller holds the shard's lock.
func (l *Limiter) makeRoom(s *shard, now time.Time) {
	if len(s.clients) < l.perShard {
		return
	}
	if now.Sub(s.swept) >= time.Second {
		s.swept = now
		longest := l.longest()
		for k, c := range s.clients {
			if now.Sub(c.seen) > 2*longest {
				delete(s.clients, k)
			}
		}
	}
	for k := range s.clients {
		if len(s.clients) < l.perShard {
			break
		}
		delete(s.clients, k)
	}
}

func (l *Limiter) longest() time.Duration {
	var longest time.Duration
	for _, w := range l.opts.Windows {
		longest = max(longest, w.Per)
	}
	return longest
}

// Sweep removes clients not seen for twice the longest period. It is meant
// to be called now and then so addresses do not stay in memory longer than
// needed when traffic is low.
func (l *Limiter) Sweep() {
	now := l.opts.Now()
	longest := l.longest()
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		for k, c := range s.clients {
			if now.Sub(c.seen) > 2*longest {
				delete(s.clients, k)
			}
		}
		s.mu.Unlock()
	}
}

// Report is the state of the limiter at one moment. It holds no address.
type Report struct {
	// Clients is how many clients are being counted right now.
	Clients int `json:"clients"`
	// Exempt is how many requests came from exempt addresses.
	Exempt uint64 `json:"exempt_requests"`
	// Limits lists each limit and how many requests were over it.
	Limits []LimitReport `json:"limits"`
}

// LimitReport is one limit in a Report.
type LimitReport struct {
	Requests int    `json:"requests"`
	Count    string `json:"count"`
	Per      string `json:"per"`
	Action   string `json:"action"`
	DenyAt   int    `json:"deny_at,omitempty"`
	Over     uint64 `json:"requests_over_limit"`
	Denied   uint64 `json:"requests_over_deny_at,omitempty"`
}

// Report returns the current state.
func (l *Limiter) Report() Report {
	r := Report{Exempt: l.exempted.Load(), Limits: make([]LimitReport, len(l.opts.Windows))}
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		r.Clients += len(s.clients)
		s.mu.Unlock()
	}
	for i, w := range l.opts.Windows {
		count := "requests"
		if w.Pages {
			count = "pages"
		}
		r.Limits[i] = LimitReport{Count: count, Requests: w.Requests, Per: w.Per.String(), Action: w.Action, DenyAt: w.DenyAt, Over: l.exceeded[i].Load(), Denied: l.stepped[i].Load()}
	}
	return r
}

// Handler serves the report as JSON for the operations listener.
func (l *Limiter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(l.Report())
	})
}
