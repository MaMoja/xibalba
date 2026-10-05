// Package preview remembers the link-preview tags of the website's pages.
//
// When someone shares a link, the service they share it on (a messenger, a
// social network) fetches the page and shows its title, description and
// picture, taken from "meta" tags in the page's head (Open Graph). A page
// behind the security check would show the check instead. With this package
// the challenge page carries the tags of the page that was asked for, so the
// preview is the right one although the fetcher never passes the check.
//
// Tags are fetched from the website in the background, a few per minute at
// most, and kept for a while. A request never waits for a fetch: the first
// challenge page for an address comes without tags, later ones with them.
package preview

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

// Limits.
const (
	// MaxTags is how many tags of one page are kept.
	MaxTags = 40
	// MaxValue is the longest value of a tag, in bytes.
	MaxValue = 1000
	maxKey   = 64
	maxHead  = 256 << 10 // how much of a page is read
	maxQuery = 512       // an address with a longer query gets no tags
	maxPath  = 1024
	queue    = 64
)

// Tag is one "meta" tag: <meta property="og:title" content="…"> or, with
// Name set, <meta name="description" content="…">.
type Tag struct {
	Key, Value string
	Name       bool
}

// Options configures a Cache.
type Options struct {
	// Upstream is the website the tags are fetched from.
	Upstream *url.URL
	// PreserveHost sends the visitor's host name to the website, as the
	// proxy does.
	PreserveHost bool
	// TTL is how long tags are kept before they are fetched again.
	TTL time.Duration
	// MaxEntries is how many addresses are remembered.
	MaxEntries int
	// PerMinute is how many pages are fetched per minute at most.
	PerMinute int
	// Query keeps the part of an address after "?" apart: each query is
	// fetched and remembered on its own. Without it the query is left out,
	// so that one page is fetched once however the link was decorated.
	Query bool
	// Fixed, if not empty, is used for every address, and nothing is
	// fetched.
	Fixed []Tag
	// UserAgent is sent with the fetches.
	UserAgent string
	// Timeout bounds one fetch. Zero means five seconds.
	Timeout time.Duration
	// Log receives messages.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

type entry struct {
	tags    []Tag
	expires time.Time
}

type job struct{ key, host, path, query string }

// Cache holds the tags. It is a lifecycle component and safe for use from
// many goroutines.
type Cache struct {
	opts   Options
	log    *slog.Logger
	client *http.Client

	mu      sync.Mutex
	entries map[string]entry
	jobs    chan job

	fetched, failed, dropped atomic.Uint64
	lastErr                  atomic.Value // string

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Cache. Nothing is fetched before Start.
func New(opts Options) *Cache {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.MaxEntries < 1 {
		opts.MaxEntries = 1
	}
	if opts.PerMinute < 1 {
		opts.PerMinute = 1
	}
	return &Cache{
		opts: opts, log: opts.Log.With("component", "previews"),
		entries: map[string]entry{}, jobs: make(chan job, queue),
		client: &http.Client{
			Timeout: opts.Timeout,
			// Straight to the website, as the proxy goes: never through a
			// proxy named in the environment.
			Transport: &http.Transport{MaxIdleConns: 2, IdleConnTimeout: time.Minute, ResponseHeaderTimeout: opts.Timeout},
			// The website's answer is taken as it is. Following it
			// elsewhere would let a page send Xibalba to other hosts.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Name implements lifecycle.Component.
func (c *Cache) Name() string { return "previews" }

// Start begins fetching in the background.
func (c *Cache) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	go func() {
		defer close(c.done)
		// One fetch at a time, spaced so that PerMinute is never exceeded.
		pause := time.Minute / time.Duration(c.opts.PerMinute)
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-c.jobs:
				c.fetch(ctx, j)
				select {
				case <-ctx.Done():
					return
				case <-time.After(pause):
				}
			}
		}
	}()
	return nil
}

// Stop ends the background work.
func (c *Cache) Stop(ctx context.Context) error {
	if c.cancel == nil {
		return nil
	}
	c.cancel()
	select {
	case <-c.done:
		c.client.CloseIdleConnections()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Health reports whether the website answers the fetches.
func (c *Cache) Health() health.Status {
	if msg, _ := c.lastErr.Load().(string); msg != "" {
		return health.Status{State: health.Degraded, Detail: "the last fetch of preview tags failed: " + msg}
	}
	return health.Status{State: health.OK}
}

// Counts returns how many pages were fetched, how many fetches failed, and
// how many were not made because too many were waiting.
func (c *Cache) Counts() (fetched, failed, dropped uint64) {
	return c.fetched.Load(), c.failed.Load(), c.dropped.Load()
}

// Tags returns the tags for an address of the website, or nil if none are
// known yet. It never waits: an address not known is put in line to be
// fetched, and a later call has the answer.
func (c *Cache) Tags(host, path, query string) []Tag {
	if len(c.opts.Fixed) > 0 {
		return c.opts.Fixed
	}
	if !c.opts.Query {
		query = ""
	}
	if len(query) > maxQuery || len(path) > maxPath || len(host) > 255 || !plainPath(path) {
		return nil
	}
	key := path + "?" + query
	if c.opts.PreserveHost {
		key = strings.ToLower(host) + key
	}
	now := c.opts.Now()

	c.mu.Lock()
	e, known := c.entries[key]
	if known && now.Before(e.expires) {
		c.mu.Unlock()
		return e.tags
	}
	// Note that the fetch is under way, so the address is asked for once:
	// what was known stays in use until the new answer is there.
	if !known && len(c.entries) >= c.opts.MaxEntries {
		c.makeRoom(now)
	}
	if len(c.entries) >= c.opts.MaxEntries && !known {
		c.mu.Unlock()
		c.dropped.Add(1)
		return nil
	}
	c.entries[key] = entry{tags: e.tags, expires: now.Add(time.Minute)}
	c.mu.Unlock()

	select {
	case c.jobs <- job{key: key, host: host, path: path, query: query}:
	default:
		c.dropped.Add(1) // too many waiting; the entry above is asked for again in a minute
	}
	return e.tags
}

// makeRoom forgets expired entries, and if none has expired, any one. The
// caller holds the lock.
func (c *Cache) makeRoom(now time.Time) {
	for key, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, key)
		}
	}
	for key := range c.entries {
		if len(c.entries) < c.opts.MaxEntries {
			break
		}
		delete(c.entries, key)
	}
}

func (c *Cache) store(key string, tags []Tag, keep time.Duration) {
	c.mu.Lock()
	c.entries[key] = entry{tags: tags, expires: c.opts.Now().Add(keep)}
	c.mu.Unlock()
}

// fetch asks the website for one page and keeps its tags. A page that
// cannot be fetched, or has no tags, is remembered as such for a while, so
// that it is not asked for again and again.
func (c *Cache) fetch(ctx context.Context, j job) {
	target := *c.opts.Upstream
	target.Path = strings.TrimSuffix(target.Path, "/") + j.path
	target.RawQuery = j.query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		c.store(j.key, nil, c.opts.TTL)
		return
	}
	if c.opts.PreserveHost && j.host != "" {
		req.Host = j.host
	}
	req.Header.Set("X-Forwarded-Host", j.host)
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := c.client.Do(req)
	if err != nil {
		c.failed.Add(1)
		c.lastErr.Store("the website did not answer")
		c.store(j.key, nil, min(c.opts.TTL, 5*time.Minute))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	c.fetched.Add(1)
	c.lastErr.Store("")
	if resp.StatusCode >= 500 { // a passing trouble: ask again soon
		c.store(j.key, nil, min(c.opts.TTL, 5*time.Minute))
		return
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		c.store(j.key, nil, c.opts.TTL)
		return
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, maxHead))
	c.store(j.key, Parse(string(head)), c.opts.TTL)
}

// Parse returns the link-preview tags in the head of an HTML page: those
// whose property or name starts with "og:", "twitter:" or "article:", and
// the description. If the page has a title but no og:title, the title is
// used. Whatever the page holds, the result is bounded and plain text.
func Parse(page string) []Tag {
	lower := lowerASCII(page)
	if end := strings.Index(lower, "</head"); end >= 0 {
		page, lower = page[:end], lower[:end]
	}
	var tags []Tag
	seen := map[string]bool{}
	hasTitle := false
	for at := 0; len(tags) < MaxTags; {
		i := strings.Index(lower[at:], "<meta")
		if i < 0 {
			break
		}
		start := at + i + len("<meta")
		end := tagEnd(page[start:])
		if end < 0 {
			break
		}
		at = start + end + 1
		attrs := attributes(page[start : start+end])
		key, name := attrs["property"], false
		if key == "" {
			key, name = attrs["name"], true
		}
		key = lowerASCII(strings.TrimSpace(key))
		value := clean(attrs["content"])
		wanted := strings.HasPrefix(key, "og:") || strings.HasPrefix(key, "twitter:") || strings.HasPrefix(key, "article:") || key == "description"
		if !wanted || !plainKey(key) || value == "" || seen[key] {
			continue
		}
		seen[key] = true
		hasTitle = hasTitle || key == "og:title"
		tags = append(tags, Tag{Key: key, Value: value, Name: name && !strings.HasPrefix(key, "og:") && !strings.HasPrefix(key, "article:")})
	}
	if !hasTitle && len(tags) < MaxTags {
		if i := strings.Index(lower, "<title"); i >= 0 {
			if open := strings.IndexByte(lower[i:], '>'); open >= 0 {
				rest := page[i+open+1:]
				if end := strings.Index(lowerASCII(rest), "</title"); end >= 0 {
					if title := clean(rest[:end]); title != "" {
						tags = append(tags, Tag{Key: "og:title", Value: title})
					}
				}
			}
		}
	}
	return tags
}

// lowerASCII lowers the letters A to Z and leaves every other byte alone,
// so that a position in the result is the same position in s. (ToLower may
// change the length of text that is not valid UTF-8.)
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// tagEnd returns where the tag that s is the inside of ends: the first ">"
// outside quotes. -1 if it does not end.
func tagEnd(s string) int {
	var quote byte
	for i := 0; i < len(s) && i < 16<<10; i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			// A quote opens a value only after "=", as in a browser.
			if j := strings.TrimRight(s[:i], " \t\r\n"); strings.HasSuffix(j, "=") {
				quote = c
			}
		case c == '>':
			return i
		}
	}
	return -1
}

// attributes reads name="value" pairs from the inside of a tag.
func attributes(s string) map[string]string {
	out := map[string]string{}
	for len(out) < 16 {
		s = strings.TrimLeft(s, " \t\r\n/")
		if s == "" {
			break
		}
		end := strings.IndexAny(s, "= \t\r\n")
		if end < 0 {
			break
		}
		name := strings.ToLower(s[:end])
		s = strings.TrimLeft(s[end:], " \t\r\n")
		if !strings.HasPrefix(s, "=") {
			continue // an attribute without a value
		}
		s = strings.TrimLeft(s[1:], " \t\r\n")
		if s == "" {
			break
		}
		var value string
		if s[0] == '"' || s[0] == '\'' {
			close := strings.IndexByte(s[1:], s[0])
			if close < 0 {
				break
			}
			value, s = s[1:1+close], s[close+2:]
		} else {
			end := strings.IndexAny(s, " \t\r\n")
			if end < 0 {
				end = len(s)
			}
			value, s = s[:end], s[end:]
		}
		if _, dup := out[name]; !dup {
			out[name] = value
		}
	}
	return out
}

// clean turns what a page wrote into plain text of bounded length.
func clean(s string) string { return tidy(html.UnescapeString(s)) }

func tidy(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	for len(s) > MaxValue { // cut at a character, not inside one
		_, size := lastRune(s)
		s = s[:len(s)-size]
	}
	return s
}

func lastRune(s string) (rune, int) {
	for i := len(s) - 1; i >= 0 && i >= len(s)-4; i-- {
		if s[i]&0xC0 != 0x80 {
			return rune(s[i]), len(s) - i
		}
	}
	return 0, 1
}

// plainPath reports whether path is one a link is shared with: from the
// root, without "." or ".." steps and without control characters. The rest
// is not worth a fetch, and ".." could lead out of the website's own part
// of the server.
func plainPath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, step := range strings.Split(path[1:], "/") {
		if step == "." || step == ".." {
			return false
		}
	}
	return !strings.ContainsFunc(path, func(r rune) bool { return r < ' ' || r == 0x7f || r == '\\' })
}

// Fixed turns the tags of the configuration into the form the cache takes,
// in the order of their names.
func Fixed(tags map[string]string) []Tag {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Tag, 0, len(keys))
	for _, key := range keys {
		out = append(out, Tag{Key: key, Value: tags[key], Name: !strings.HasPrefix(key, "og:") && !strings.HasPrefix(key, "article:")})
	}
	return out
}

// CheckTag reports what is wrong with a tag of the configuration, or "".
func CheckTag(key, value string) string {
	wanted := strings.HasPrefix(key, "og:") || strings.HasPrefix(key, "twitter:") || strings.HasPrefix(key, "article:") || key == "description"
	switch {
	case !wanted || !plainKey(key):
		return `the name has to be "description" or start with "og:", "twitter:" or "article:", in small letters`
	case value == "" || value != tidy(value):
		return fmt.Sprintf("the value has to be plain text of 1 to %d characters, on one line", MaxValue)
	}
	return ""
}

func plainKey(key string) bool {
	if key == "" || len(key) > maxKey {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		letter, digit := c >= 'a' && c <= 'z', c >= '0' && c <= '9'
		if !letter && !digit && c != ':' && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}
