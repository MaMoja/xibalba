package crawlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

// Status says what is known about a request that claims to be a crawler.
type Status int

const (
	// NotACrawler: the user agent names no known crawler.
	NotACrawler Status = iota
	// Verified: the request comes from the crawler's operator.
	Verified
	// Unverified: the request carries the crawler's name but does not come
	// from the crawler's operator. It is an impostor.
	Unverified
	// Unverifiable: the operator publishes no way to verify this crawler.
	Unverifiable
	// Pending: not known yet. The address list has not been downloaded, or
	// the DNS lookup is still running. A pending request is treated as
	// neither genuine nor an impostor.
	Pending
)

func (s Status) String() string {
	switch s {
	case Verified:
		return "verified"
	case Unverified:
		return "unverified"
	case Unverifiable:
		return "unverifiable"
	case Pending:
		return "pending"
	default:
		return "none"
	}
}

// Identity is what is known about who sent a request.
type Identity struct {
	// Name and Class describe the crawler the request claims to be. Empty
	// if Status is NotACrawler.
	Name  string
	Class Class
	// Status says whether the claim is true.
	Status Status
}

// Resolver looks names up in DNS. *net.Resolver implements it.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Options configures a Registry.
type Options struct {
	// Definitions are the crawlers to know.
	Definitions []Definition
	// Refresh downloads the operators' address lists in the background.
	// Without it only lists from CacheDir, addresses written in the
	// definitions and reverse DNS are used.
	Refresh bool
	// RefreshInterval is how often the lists are downloaded again.
	RefreshInterval time.Duration
	// CacheDir, if set, keeps downloaded lists across restarts.
	CacheDir string
	// UserAgent is sent when downloading lists.
	UserAgent string
	// Client downloads the lists. Nil means a client with sensible limits.
	Client *http.Client
	// Resolver does the DNS lookups. Nil means the system resolver.
	Resolver Resolver
	// Log receives the registry's messages.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Limits.
const (
	// maxUA is how much of a user agent is searched for crawler names.
	maxUA = 512

	dnsWorkers      = 16
	dnsQueue        = 1024
	dnsCacheEntries = 20000 // each of the two tables
	dnsTimeout      = 3 * time.Second
	dnsVerifiedTTL  = 24 * time.Hour
	dnsRefusedTTL   = time.Hour
	dnsRetryAfter   = time.Minute
	dnsSweepEvery   = 10 * time.Second
	fetchTimeout    = 30 * time.Second
	maxRedirects    = 5
	// minListAge: a list is never considered too old before this age.
	minListAge = 7 * 24 * time.Hour
)

// retryAfter is how long to wait before trying failed downloads again.
var retryAfter = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// Registry identifies crawlers. It is a lifecycle component: Start begins
// the background work, Stop ends it. Identify may be called at any time and
// from any goroutine.
type Registry struct {
	opts Options
	log  *slog.Logger

	// crawlers is sorted so that longer user agent texts are tried first.
	crawlers []*crawler
	urls     []string // distinct address list locations, sorted

	lists atomic.Pointer[map[string]*list] // by URL; replaced as a whole

	// Reverse DNS results. Confirmed addresses and everything else are kept
	// apart, each with its own limit, so a flood of impostors cannot push
	// out the genuine crawlers. Negative results are kept per network (an
	// IPv6 client owns a whole /64 and could otherwise fill the table with
	// one address each).
	dnsMu       sync.Mutex
	dnsVerified map[dnsKey]time.Time // exact address -> expiry
	dnsOther    map[dnsKey]dnsEntry  // network -> refused or under way
	dnsSwept    time.Time
	dnsJobs     chan dnsKey

	mu       sync.Mutex
	fetchErr map[string]string // by URL: why the last download failed

	cancel context.CancelFunc
	done   sync.WaitGroup
}

type crawler struct {
	def        Definition
	lowerUA    string
	lowerBytes []byte
	counts     [5]atomic.Uint64 // indexed by Status
	dnsSuffix  []string
}

// list is one downloaded address list.
type list struct {
	prefixes []netip.Prefix
	fetched  time.Time
}

type dnsKey struct {
	addr    netip.Addr
	crawler int // index into Registry.crawlers
}

type dnsEntry struct {
	status  Status
	expires time.Time
}

// New returns a Registry for opts. It does nothing until Start is called;
// Identify already works, with what is known without the network.
func New(opts Options) *Registry {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Client == nil {
		opts.Client = &http.Client{
			Timeout: fetchTimeout,
			// A redirect must not lead away from a protected connection or
			// to somewhere a list could not have been configured.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return errors.New("too many redirects")
				}
				if err := checkRangesURL(req.URL.String()); err != nil {
					return errors.New("redirected to an address that cannot be used")
				}
				return nil
			},
		}
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = 24 * time.Hour
	}
	r := &Registry{
		opts:        opts,
		log:         opts.Log.With("component", "crawlers"),
		dnsVerified: map[dnsKey]time.Time{},
		dnsOther:    map[dnsKey]dnsEntry{},
		dnsJobs:     make(chan dnsKey, dnsQueue),
		fetchErr:    map[string]string{},
	}
	seen := map[string]bool{}
	for _, def := range opts.Definitions {
		r.crawlers = append(r.crawlers, &crawler{def: def, lowerUA: strings.ToLower(def.UserAgent), lowerBytes: []byte(strings.ToLower(def.UserAgent)), dnsSuffix: def.Verify.ReverseDNS})
		if u := def.Verify.RangesURL; u != "" && !seen[u] {
			seen[u] = true
			r.urls = append(r.urls, u)
		}
	}
	// "Googlebot-Image" must be tried before "Googlebot".
	sort.SliceStable(r.crawlers, func(a, b int) bool { return len(r.crawlers[a].lowerUA) > len(r.crawlers[b].lowerUA) })
	sort.Strings(r.urls)
	empty := map[string]*list{}
	r.lists.Store(&empty)
	return r
}

// Name implements lifecycle.Component.
func (r *Registry) Name() string { return "crawlers" }

// Start loads address lists kept from an earlier run and begins the
// background work. It does not wait for the network.
func (r *Registry) Start(context.Context) error {
	r.loadCache()

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	for i := 0; i < dnsWorkers; i++ {
		r.done.Add(1)
		go func() {
			defer r.done.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case key := <-r.dnsJobs:
					r.resolve(ctx, key)
				}
			}
		}()
	}

	if r.opts.Refresh && len(r.urls) > 0 {
		r.done.Add(1)
		go func() {
			defer r.done.Done()
			failures := 0
			for {
				wait := r.opts.RefreshInterval
				if r.refreshAll(ctx) {
					failures = 0
				} else {
					// Do not wait a whole interval after a failure: the
					// network may simply not have been up yet.
					if failures < len(retryAfter) {
						wait = min(wait, retryAfter[failures])
					} else {
						wait = min(wait, retryAfter[len(retryAfter)-1])
					}
					failures++
				}
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	return nil
}

// Stop ends the background work.
func (r *Registry) Stop(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	finished := make(chan struct{})
	go func() { r.done.Wait(); close(finished) }()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return errors.New("background work did not stop in time")
	}
}

// Identify says which crawler a request claims to be and whether the claim
// is true. It never waits for the network: what is not known yet is Pending.
func (r *Registry) Identify(userAgent string, client netip.Addr) Identity {
	return r.identify(userAgent, client, false)
}

// Peek answers like Identify from what is known at this moment, and leaves
// no trace: nothing is counted and no lookup is started. It is for trying a
// rule on a made-up request.
func (r *Registry) Peek(userAgent string, client netip.Addr) Identity {
	return r.identify(userAgent, client, true)
}

func (r *Registry) identify(userAgent string, client netip.Addr, quiet bool) Identity {
	// Only the start of the user agent is searched. Crawlers name
	// themselves well within it, and a client must not be able to make a
	// request expensive by sending a huge header.
	if len(userAgent) > maxUA {
		userAgent = userAgent[:maxUA]
	}
	// Lower the case once, on the stack, and let the optimised search of
	// the standard library look for each name.
	var buf [maxUA]byte
	lower := buf[:len(userAgent)]
	for i := 0; i < len(userAgent); i++ {
		b := userAgent[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		lower[i] = b
	}
	index := -1
	for i, c := range r.crawlers {
		if bytes.Contains(lower, c.lowerBytes) {
			index = i
			break
		}
	}
	if index < 0 {
		return Identity{}
	}
	c := r.crawlers[index]
	client = client.Unmap().WithZone("")
	status := r.verify(index, c, client, quiet)
	if !quiet {
		c.counts[status].Add(1)
	}
	return Identity{Name: c.def.Name, Class: c.def.Class, Status: status}
}

func (r *Registry) verify(index int, c *crawler, client netip.Addr, quiet bool) Status {
	v := c.def.Verify
	if !v.Verifiable() {
		return Unverifiable
	}
	if !client.IsValid() {
		return Pending
	}
	if contains(v.prefixes, client) {
		return Verified
	}

	undecided := false
	if v.RangesURL != "" {
		if l := (*r.lists.Load())[v.RangesURL]; l != nil && !r.tooOld(l) {
			if contains(l.prefixes, client) {
				return Verified
			}
		} else {
			undecided = true // the list is not here yet
		}
	}
	if len(c.dnsSuffix) > 0 {
		switch r.dnsStatus(dnsKey{addr: client, crawler: index}, quiet) {
		case Verified:
			return Verified
		case Pending:
			undecided = true
		}
	}
	if undecided {
		return Pending
	}
	return Unverified
}

// tooOld reports whether a list is too old to be believed. Operators give
// up address space; a list that has not been renewed for a long time must
// not go on vouching for whoever uses those addresses now.
func (r *Registry) tooOld(l *list) bool {
	return r.opts.Now().Sub(l.fetched) > max(minListAge, 3*r.opts.RefreshInterval)
}

// network returns the key under which negative results for key are kept:
// the address itself for IPv4, its /64 for IPv6.
func network(key dnsKey) dnsKey {
	if key.addr.Is6() {
		if p, err := key.addr.Prefix(64); err == nil {
			key.addr = p.Addr()
		}
	}
	return key
}

// dnsStatus returns the cached result of the reverse DNS check, starting the
// check in the background if there is none.
func (r *Registry) dnsStatus(key dnsKey, quiet bool) Status {
	now := r.opts.Now()
	r.dnsMu.Lock()
	defer r.dnsMu.Unlock()

	if expires, ok := r.dnsVerified[key]; ok && now.Before(expires) {
		return Verified
	}
	net := network(key)
	if entry, ok := r.dnsOther[net]; ok && now.Before(entry.expires) {
		return entry.status
	}
	if quiet {
		return Pending // not known, and not to be found out on this occasion
	}
	select {
	case r.dnsJobs <- key:
		// Remember that the lookup is under way, so it is not queued again.
		r.makeRoom(now)
		r.dnsOther[net] = dnsEntry{status: Pending, expires: now.Add(dnsRetryAfter)}
	default:
		// The queue is full. Try again on a later request.
	}
	return Pending
}

// makeRoom keeps both tables within their limit. Expired entries are swept
// at most every few seconds; if a table is still full, any entry makes way.
// Forgetting an entry only costs a repeated lookup. The caller holds dnsMu.
func (r *Registry) makeRoom(now time.Time) {
	if len(r.dnsOther) < dnsCacheEntries && len(r.dnsVerified) < dnsCacheEntries {
		return
	}
	if now.Sub(r.dnsSwept) >= dnsSweepEvery {
		r.dnsSwept = now
		for k, entry := range r.dnsOther {
			if !now.Before(entry.expires) {
				delete(r.dnsOther, k)
			}
		}
		for k, expires := range r.dnsVerified {
			if !now.Before(expires) {
				delete(r.dnsVerified, k)
			}
		}
	}
	for k := range r.dnsOther {
		if len(r.dnsOther) < dnsCacheEntries {
			break
		}
		delete(r.dnsOther, k)
	}
	for k := range r.dnsVerified {
		if len(r.dnsVerified) < dnsCacheEntries {
			break
		}
		delete(r.dnsVerified, k)
	}
}

// resolve does the reverse DNS check for one address: the address must
// resolve to a name with one of the crawler's domain endings, and that name
// must resolve back to the address. The second step matters: anyone can make
// their own address resolve to any name, but only the owner of the domain
// can make the name resolve back.
func (r *Registry) resolve(ctx context.Context, key dnsKey) {
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	status, ttl := r.lookup(ctx, key.addr, r.crawlers[key.crawler].dnsSuffix)
	now := r.opts.Now()
	r.dnsMu.Lock()
	defer r.dnsMu.Unlock()
	r.makeRoom(now)
	if status == Verified {
		r.dnsVerified[key] = now.Add(ttl)
		delete(r.dnsOther, network(key))
		return
	}
	r.dnsOther[network(key)] = dnsEntry{status: status, expires: now.Add(ttl)}
}

func (r *Registry) lookup(ctx context.Context, addr netip.Addr, suffixes []string) (Status, time.Duration) {
	names, err := r.opts.Resolver.LookupAddr(ctx, addr.String())
	if err != nil {
		if notFound(err) {
			return Unverified, dnsRefusedTTL // the address has no name at all
		}
		return Pending, dnsRetryAfter // DNS trouble: decide nothing, try again later
	}
	trouble := false
	for _, name := range names {
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		if !hasDomainSuffix(name, suffixes) {
			continue
		}
		// The trailing dot makes the name absolute, so the resolver's
		// search domains play no part.
		hosts, err := r.opts.Resolver.LookupHost(ctx, name+".")
		if err != nil {
			if !notFound(err) {
				trouble = true
			}
			continue
		}
		for _, host := range hosts {
			if resolved, err := netip.ParseAddr(host); err == nil && resolved.Unmap().WithZone("") == addr {
				return Verified, dnsVerifiedTTL
			}
		}
	}
	if trouble {
		return Pending, dnsRetryAfter
	}
	return Unverified, dnsRefusedTTL
}

func notFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// hasDomainSuffix reports whether name ends with one of the domain endings.
// Each ending starts with a dot, so "evilgooglebot.com" does not end with
// ".googlebot.com".
func hasDomainSuffix(name string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
			return true
		}
	}
	return false
}

// refreshAll downloads every address list. A list that cannot be downloaded
// or is refused keeps its previous content. It reports whether every list
// was downloaded.
func (r *Registry) refreshAll(ctx context.Context) bool {
	complete := true
	for _, u := range r.urls {
		if ctx.Err() != nil {
			return false
		}
		prefixes, err := r.fetch(ctx, u)
		r.mu.Lock()
		previous := r.fetchErr[u]
		if err != nil {
			r.fetchErr[u] = err.Error()
		} else {
			delete(r.fetchErr, u)
		}
		r.mu.Unlock()

		if err != nil {
			complete = false
			if previous == "" { // say it once, not at every attempt
				r.log.Warn("an address list could not be downloaded; the previous one stays in use", "url", display(u), "error", err.Error())
			}
			continue
		}
		if previous != "" {
			r.log.Info("an address list is available again", "url", display(u))
		}
		l := &list{prefixes: prefixes, fetched: r.opts.Now()}
		r.store(u, l)
		r.saveCache(u, l)
	}
	return complete
}

// display returns a list location without its query, which may hold a token.
func display(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(address list)"
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

func (r *Registry) fetch(ctx context.Context, u string) ([]netip.Prefix, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.opts.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain")
	resp, err := r.opts.Client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // without the address, which is logged separately
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered with status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, errors.New("the download was interrupted")
	}
	return ParseRanges(data)
}

// store replaces one list. The table is copied and swapped as a whole, so
// Identify reads it without locking.
func (r *Registry) store(u string, l *list) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := *r.lists.Load()
	next := make(map[string]*list, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[u] = l
	r.lists.Store(&next)
}

// cached is the layout of a list kept in CacheDir.
type cached struct {
	URL      string    `json:"url"`
	Fetched  time.Time `json:"fetched"`
	Prefixes []string  `json:"prefixes"`
}

func (r *Registry) cachePath(u string) string {
	sum := sha256.Sum256([]byte(u))
	return filepath.Join(r.opts.CacheDir, "ranges-"+hex.EncodeToString(sum[:8])+".json")
}

func (r *Registry) saveCache(u string, l *list) {
	if r.opts.CacheDir == "" {
		return
	}
	c := cached{URL: u, Fetched: l.fetched.UTC(), Prefixes: make([]string, len(l.prefixes))}
	for i, p := range l.prefixes {
		c.Prefixes[i] = p.String()
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	// Write to a temporary file and rename, so a crash cannot leave half a list.
	tmp, err := os.CreateTemp(r.opts.CacheDir, "ranges-*.tmp") // mode 0600, fresh name
	if err == nil {
		_, err = tmp.Write(data)
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), r.cachePath(u))
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	if err != nil {
		r.log.Warn("an address list could not be kept for the next start", "error", err.Error())
	}
}

// loadCache reads the lists kept from an earlier run. A kept list goes
// through the same checks as a downloaded one.
func (r *Registry) loadCache() {
	if r.opts.CacheDir == "" {
		return
	}
	for _, u := range r.urls {
		data, err := os.ReadFile(r.cachePath(u))
		if err != nil {
			continue
		}
		var c cached
		if json.Unmarshal(data, &c) != nil || c.URL != u {
			continue
		}
		prefixes, err := ParseRanges([]byte(strings.Join(c.Prefixes, "\n")))
		if err != nil {
			continue
		}
		r.store(u, &list{prefixes: prefixes, fetched: c.Fetched})
	}
}

// Health reports whether the address lists are in place. A missing or old
// list is "degraded": requests are still served, but the crawlers concerned
// cannot be verified and are treated as not known.
func (r *Registry) Health() health.Status {
	lists := *r.lists.Load()
	r.mu.Lock()
	defer r.mu.Unlock()

	var missing, stale []string
	for _, u := range r.urls {
		l := lists[u]
		switch {
		case l == nil:
			detail := display(u)
			if reason := r.fetchErr[u]; reason != "" {
				detail += " (" + reason + ")"
			}
			missing = append(missing, detail)
		case r.tooOld(l):
			missing = append(missing, display(u)+" (the list from "+l.fetched.UTC().Format("2006-01-02")+" is too old to be used)")
		case r.opts.Refresh && r.opts.Now().Sub(l.fetched) > 3*r.opts.RefreshInterval:
			stale = append(stale, display(u)+" (from "+l.fetched.UTC().Format("2006-01-02")+")")
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return health.Status{State: health.OK}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d address lists are not available, so their crawlers cannot be verified: %s", len(missing), strings.Join(missing, "; ")))
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("%d address lists are out of date: %s", len(stale), strings.Join(stale, "; ")))
	}
	return health.Status{State: health.Degraded, Detail: strings.Join(parts, ". ")}
}

// Report describes one crawler and what was seen of it.
type Report struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
	Class    Class  `json:"class"`
	Purpose  string `json:"purpose"`
	Note     string `json:"note,omitempty"`
	Source   string `json:"source"`
	Checked  string `json:"checked"`
	// VerifiedBy is "addresses", "reverse DNS", both, or "none".
	VerifiedBy string `json:"verified_by"`
	// Addresses is how many networks are known for the crawler.
	Addresses int `json:"addresses"`
	// ListFrom is when the crawler's address list was downloaded.
	ListFrom *time.Time `json:"list_from,omitempty"`
	// ListError is why the last download failed.
	ListError string `json:"list_error,omitempty"`
	// Requests counts the requests that claimed to be this crawler since start.
	Requests RequestCounts `json:"requests"`
}

// RequestCounts counts requests by what was found out about them.
type RequestCounts struct {
	Verified     uint64 `json:"verified"`
	Unverified   uint64 `json:"unverified"`
	Unverifiable uint64 `json:"unverifiable"`
	Pending      uint64 `json:"pending"`
}

// Reports returns every known crawler, sorted by operator and name.
func (r *Registry) Reports() []Report {
	lists := *r.lists.Load()
	r.mu.Lock()
	defer r.mu.Unlock()

	reports := make([]Report, 0, len(r.crawlers))
	for _, c := range r.crawlers {
		d := c.def
		rep := Report{
			Name: d.Name, Operator: d.Operator, Class: d.Class, Purpose: d.Purpose, Note: strings.TrimSpace(d.Note),
			Source: d.Source, Checked: d.Checked, VerifiedBy: d.Verify.Method(),
			Addresses: len(d.Verify.prefixes),
			Requests: RequestCounts{
				Verified: c.counts[Verified].Load(), Unverified: c.counts[Unverified].Load(),
				Unverifiable: c.counts[Unverifiable].Load(), Pending: c.counts[Pending].Load(),
			},
		}
		if u := d.Verify.RangesURL; u != "" {
			if l := lists[u]; l != nil {
				rep.Addresses += len(l.prefixes)
				fetched := l.fetched.UTC().Truncate(time.Second)
				rep.ListFrom = &fetched
			}
			rep.ListError = r.fetchErr[u]
		}
		reports = append(reports, rep)
	}
	sort.Slice(reports, func(a, b int) bool {
		if reports[a].Operator != reports[b].Operator {
			return reports[a].Operator < reports[b].Operator
		}
		return reports[a].Name < reports[b].Name
	})
	return reports
}

// Handler serves the reports as JSON for the operations listener.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"crawlers": r.Reports()})
	})
}
