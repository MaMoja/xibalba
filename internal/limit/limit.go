// Package limit counts requests per client and says when a client has sent
// more than the site owner allows in a period of time.
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
}

// ID names the window in reports, such as "300/10m0s".
func (w Window) ID() string { return fmt.Sprintf("%d/%s", w.Requests, w.Per) }

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
	l := &Limiter{opts: opts, perShard: opts.MaxClients / shards, seed: maphash.MakeSeed(), exceeded: make([]atomic.Uint64, len(opts.Windows))}
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
// limit. An invalid or exempt address is never counted.
func (l *Limiter) Count(addr netip.Addr) Verdict {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() || len(l.opts.Windows) == 0 {
		return Verdict{}
	}
	for _, p := range l.opts.Exempt {
		if p.Contains(addr) {
			l.exempted.Add(1)
			return Verdict{}
		}
	}
	key := l.key(addr)
	raw := key.As16()
	s := &l.shards[maphash.Bytes(l.seed, raw[:])%shards]
	now := l.opts.Now()

	s.mu.Lock()
	c := s.clients[key]
	if c == nil {
		l.makeRoom(s, now)
		c = &counters{}
		s.clients[key] = c
	}
	c.seen = now
	var verdict Verdict
	worst := -1
	for i, w := range l.opts.Windows {
		period := now.UnixNano() / int64(w.Per)
		switch c.period[i] {
		case period:
		case period - 1:
			c.previous[i], c.current[i] = c.current[i], 0
		default:
			c.previous[i], c.current[i] = 0, 0
		}
		c.period[i] = period
		if c.current[i] < 1<<31 {
			c.current[i]++
		}
		// Share of the previous period that still lies within the last Per.
		elapsed := now.UnixNano() - period*int64(w.Per)
		remaining := float64(int64(w.Per)-elapsed) / float64(w.Per)
		estimate := float64(c.current[i]) + float64(c.previous[i])*remaining
		if estimate > float64(w.Requests) {
			if worst < 0 || (w.Action == "deny" && verdict.Action != "deny") {
				worst = i
				verdict = Verdict{Over: true, Action: w.Action, RetryAfter: time.Duration(int64(w.Per) - elapsed)}
			}
		}
	}
	s.mu.Unlock()

	if worst >= 0 {
		l.exceeded[worst].Add(1)
	}
	return verdict
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
	Per      string `json:"per"`
	Action   string `json:"action"`
	Over     uint64 `json:"requests_over_limit"`
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
		r.Limits[i] = LimitReport{Requests: w.Requests, Per: w.Per.String(), Action: w.Action, Over: l.exceeded[i].Load()}
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
