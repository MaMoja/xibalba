// Package trap catches crawlers that follow links no person can see.
//
// The pages Xibalba shows carry a link inside an inert part of the page: a
// browser does not display it, a screen reader does not announce it, and it
// cannot be reached with the keyboard. A program that collects every address
// it finds in the page text follows it anyway. Whoever requests the trap
// address is remembered for a while, so rules can treat that client's other
// requests differently.
//
// Optionally the trap answers with a maze: generated pages of meaningless
// syllables whose links lead only to more such pages.
//
// The link is different for every client and every day: it carries a keyed
// check value over the client's address. Only the client a link was made
// for can be caught by it. Another website can therefore not get a visitor
// caught by making the visitor's browser request the trap: it does not know
// the visitor's link, and a request that a browser marks as coming from
// another site is ignored as well.
//
// Clients are remembered in memory only, by IPv4 address or IPv6 /64, and
// forgotten after the configured time.
package trap

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"html/template"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
)

// Prefix is the address space of the trap. It lies inside Xibalba's own
// address space, so it can never collide with the website.
const Prefix = "/.xibalba/trap/"

// Options configures a Trap.
type Options struct {
	// Remember is how long a client that followed the link is remembered.
	Remember time.Duration
	// MaxClients is how many clients are remembered at most.
	MaxClients int
	// Maze answers the trap address with generated pages that link to more
	// trap addresses. Without it the answer is a plain "not found".
	Maze bool
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Trap remembers who followed the hidden link.
type Trap struct {
	opts Options
	key  [32]byte // makes the links; new at every start

	mu      sync.RWMutex
	clients map[netip.Addr]time.Time // key -> when it is forgotten
	swept   time.Time

	hits      atomic.Uint64 // catches
	strangers atomic.Uint64 // requests under the prefix that were no catch

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Trap. The link it hands out differs from start to start.
func New(opts Options) (*Trap, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxClients < 1 {
		opts.MaxClients = 1
	}
	t := &Trap{opts: opts, clients: map[netip.Addr]time.Time{}}
	if _, err := rand.Read(t.key[:]); err != nil {
		return nil, err
	}
	return t, nil
}

// linkLen is the length of a link's check value in hex characters.
const linkLen = 32

// maxTail bounds what may follow a link in a maze address.
const maxTail = 32

// link returns the trap address for a client on a day (days since 1970).
func (t *Trap) link(client netip.Addr, day int64) string {
	raw := client.As16()
	var msg [24]byte
	copy(msg[:16], raw[:])
	binary.BigEndian.PutUint64(msg[16:], uint64(day))
	mac := hmac.New(sha256.New, t.key[:])
	_, _ = mac.Write(msg[:])
	return Prefix + hex.EncodeToString(mac.Sum(nil))[:linkLen]
}

// Link is the address to hide in a page shown to the client of r. It is
// empty if the client's address is not known.
func (t *Trap) Link(r *http.Request) string {
	info, ok := clientip.FromContext(r.Context())
	if !ok {
		return ""
	}
	k, ok := key(info.Client)
	if !ok {
		return ""
	}
	return t.link(k, t.opts.Now().Unix()/86400)
}

// own reports whether path is a trap address made for this client, today or
// yesterday, and returns the link it starts with. A maze address is a link
// followed by a short tail.
func (t *Trap) own(path string, client netip.Addr) (string, bool) {
	if len(path) < len(Prefix)+linkLen || len(path) > len(Prefix)+linkLen+1+maxTail {
		return "", false
	}
	base, tail := path[:len(Prefix)+linkLen], path[len(Prefix)+linkLen:]
	if tail != "" && tail[0] != '/' {
		return "", false
	}
	day := t.opts.Now().Unix() / 86400
	for _, d := range []int64{day, day - 1} {
		if hmac.Equal([]byte(base), []byte(t.link(client, d))) {
			return base, true
		}
	}
	return "", false
}

// Name implements lifecycle.Component.
func (t *Trap) Name() string { return "trap" }

// Start begins forgetting clients whose time is up.
func (t *Trap) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.done = cancel, make(chan struct{})
	go func() {
		defer close(t.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.mu.Lock()
				t.sweep(t.opts.Now())
				t.mu.Unlock()
			}
		}
	}()
	return nil
}

// Stop ends the background work.
func (t *Trap) Stop(ctx context.Context) error {
	if t.cancel == nil {
		return nil
	}
	t.cancel()
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func key(addr netip.Addr) (netip.Addr, bool) {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() {
		return addr, false
	}
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.Addr(), true
		}
	}
	return addr, true
}

// Caught reports whether the client followed the hidden link recently.
func (t *Trap) Caught(addr netip.Addr) bool {
	k, ok := key(addr)
	if !ok {
		return false
	}
	t.mu.RLock()
	until, found := t.clients[k]
	t.mu.RUnlock()
	return found && t.opts.Now().Before(until)
}

func (t *Trap) catch(addr netip.Addr) {
	k, ok := key(addr)
	if !ok {
		return
	}
	now := t.opts.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, known := t.clients[k]; !known && len(t.clients) >= t.opts.MaxClients {
		if now.Sub(t.swept) >= time.Second {
			t.sweep(now)
		}
		for old := range t.clients { // still full: any entry makes way
			if len(t.clients) < t.opts.MaxClients {
				break
			}
			delete(t.clients, old)
		}
	}
	t.clients[k] = now.Add(t.opts.Remember)
}

// sweep forgets clients whose time is up. The caller holds the lock.
func (t *Trap) sweep(now time.Time) {
	t.swept = now
	for k, until := range t.clients {
		if !now.Before(until) {
			delete(t.clients, k)
		}
	}
}

// Handler answers requests under Prefix. A request is a catch only if the
// address is the one made for the requesting client and the browser does
// not say that another website caused the request. Everything else gets
// "not found" and no consequence.
func (t *Trap) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("X-Content-Type-Options", "nosniff")

		var base string
		caught := false
		if info, ok := clientip.FromContext(r.Context()); ok && r.Header.Get("Sec-Fetch-Site") != "cross-site" {
			if k, valid := key(info.Client); valid {
				if base, caught = t.own(r.URL.Path, k); caught {
					t.hits.Add(1)
					t.catch(info.Client)
				}
			}
		}
		if !caught {
			t.strangers.Add(1)
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		if !t.opts.Maze {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		var page bytes.Buffer
		writeMaze(&page, base, r.URL.Path)
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", strconv.Itoa(page.Len()))
		h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if r.Method != http.MethodHead {
			_, _ = w.Write(page.Bytes())
		}
	})
}

// The maze is made of syllables, not words. It says nothing, in no language,
// so nothing false can appear under the name of the website's owner.
var syllables = []string{
	"ba", "ke", "li", "mo", "nu", "ra", "se", "ti", "vo", "zu", "da", "fe", "gi", "ho", "ju", "la",
	"me", "ni", "po", "ru", "sa", "te", "wi", "xo", "yu", "za", "bre", "klo", "dri", "fnu", "gla", "pse",
}

const (
	mazeParagraphs = 6
	mazeWords      = 60
	mazeLinks      = 5
)

// writeMaze writes the page for one address. The same address always gives
// the same page; generating it takes a few microseconds and keeps no state.
// Its links are base followed by a tail, so they are trap addresses of the
// same client.
func writeMaze(w *bytes.Buffer, base, path string) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	state := h.Sum64() | 1
	next := func() uint64 { // xorshift: cheap, and need not be unpredictable
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	word := func() {
		for n := 1 + next()%3; n > 0; n-- {
			w.WriteString(syllables[next()%uint64(len(syllables))])
		}
	}
	w.WriteString("<!doctype html>\n<html lang=\"zxx\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"robots\" content=\"noindex, nofollow\">\n<title>")
	word()
	w.WriteString("</title>\n</head>\n<body>\n")
	for p := 0; p < mazeParagraphs; p++ {
		w.WriteString("<p>")
		for i := 0; i < mazeWords; i++ {
			if i > 0 {
				w.WriteByte(' ')
			}
			word()
		}
		w.WriteString(".</p>\n")
	}
	w.WriteString("<ul>\n")
	for i := 0; i < mazeLinks; i++ {
		w.WriteString("<li><a rel=\"nofollow\" href=\"")
		w.WriteString(template.HTMLEscapeString(base + "/" + strconv.FormatUint(next(), 36)))
		w.WriteString("\">")
		word()
		w.WriteString("</a></li>\n")
	}
	w.WriteString("</ul>\n</body>\n</html>\n")
}

// Report is the state of the trap at one moment. It holds no address.
type Report struct {
	// Hits is how many requests were a catch since the start.
	Hits uint64 `json:"hits"`
	// Ignored is how many requests to the trap's addresses were no catch:
	// not the address made for that client, or caused by another website.
	Ignored uint64 `json:"ignored"`
	// Clients is how many clients are remembered right now.
	Clients int `json:"clients"`
	// Maze says whether the maze is on.
	Maze bool `json:"maze"`
}

// Report returns the current state.
func (t *Trap) Report() Report {
	now := t.opts.Now()
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for _, until := range t.clients {
		if now.Before(until) {
			n++
		}
	}
	return Report{Hits: t.hits.Load(), Ignored: t.strangers.Load(), Clients: n, Maze: t.opts.Maze}
}

// ReportHandler serves the report as JSON for the operations listener.
func (t *Trap) ReportHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(t.Report())
	})
}
