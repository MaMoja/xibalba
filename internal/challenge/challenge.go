// Package challenge makes a client prove it behaves like a browser before
// its requests are passed on, and remembers that it did.
//
// A client that has to be challenged gets a small page instead of the page it
// asked for. The page carries a signed task. A browser solves the task by
// itself (proof of work: find a number whose hash starts with enough zero
// bits) and sends the answer back; a browser without JavaScript can instead
// wait a few seconds and press a button, if the site allows that. A correct
// answer is rewarded with a pass, a signed cookie, and the client is sent on
// to the page it wanted. While the pass is valid the client is not asked again.
//
// Nothing is stored on the server. The task and the pass are signed tokens
// (internal/token); both are tied to the client's network and user agent, so
// a pass obtained once cannot be handed to a fleet of other machines.
//
// Everything the client sends back is checked before it is believed: the
// signature and expiry of the task, that it was issued to this client, the
// answer itself, and the address the client asks to be sent on to.
package challenge

import (
	"crypto/sha256"
	"log/slog"
	"math/bits"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
	"github.com/MaMoja/xibalba/internal/token"
)

// Prefix is the part of the public address space Xibalba keeps for itself.
// Requests under it are answered by Xibalba and never reach the website.
const Prefix = "/.xibalba/"

// VerifyPath is where a client sends its answer.
const VerifyPath = Prefix + "verify"

// Limits.
const (
	// MinDifficulty and MaxDifficulty bound the proof of work, in leading
	// zero bits. Each extra bit doubles the client's work.
	MinDifficulty = 8
	MaxDifficulty = 24

	maxFormBytes   = 8 << 10
	maxReturnLen   = 2048
	maxSolutionLen = 15
)

// The names of the ways a client can answer.
const (
	// MethodPoW is the proof of work, solved by JavaScript in the browser.
	MethodPoW = "pow"
	// MethodButton is the path without JavaScript: wait, then press a button.
	MethodButton = "button"
)

// Message tells the challenge page what, if anything, to say about the
// client's previous attempt.
type Message string

const (
	// MessageNone: this is the client's first challenge.
	MessageNone Message = ""
	// MessageTooEarly: the button was pressed before the waiting time was over.
	MessageTooEarly Message = "too_early"
	// MessageRetry: the previous answer was not accepted and a new task was issued.
	MessageRetry Message = "retry"
)

// View is what the challenge page needs to show one task.
type View struct {
	// Action is where the form is sent.
	Action string
	// Token is the signed task.
	Token string
	// Return is where the client is sent after passing.
	Return string
	// Nonce and Difficulty describe the proof of work.
	Nonce      string
	Difficulty int
	// AllowButton says whether the page offers the path without JavaScript.
	AllowButton bool
	// Message is a note about the previous attempt.
	Message Message
}

// Options configures a Challenge.
type Options struct {
	// Signer signs the tasks and passes.
	Signer *token.Signer
	// Difficulty is the proof of work in leading zero bits.
	Difficulty int
	// AllowButton offers the path without JavaScript.
	AllowButton bool
	// Wait is how long a client on that path has to wait before the button counts.
	Wait time.Duration
	// ChallengeLifetime is how long a client has to answer a task.
	ChallengeLifetime time.Duration
	// PassLifetime is how long a client is not asked again after passing.
	PassLifetime time.Duration
	// BindNetwork ties tasks and passes to the client's network as well as
	// its user agent.
	BindNetwork bool
	// CookieName is the name of the pass cookie.
	CookieName string
	// Page writes the challenge page.
	Page func(w http.ResponseWriter, r *http.Request, v View)
	// Log receives the challenge's messages.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Challenge issues tasks, checks answers and recognises passes.
// It is safe for concurrent use.
type Challenge struct {
	opts Options
	log  *slog.Logger

	solved atomic.Uint64
	failed atomic.Uint64
}

// New returns a Challenge for opts.
func New(opts Options) *Challenge {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Challenge{opts: opts, log: opts.Log.With("component", "challenge")}
}

// Passed reports whether the request carries a valid pass issued to this client.
func (c *Challenge) Passed(r *http.Request) bool {
	cookie, err := r.Cookie(c.opts.CookieName)
	if err != nil {
		return false
	}
	claims, err := c.opts.Signer.Verify(token.Pass, cookie.Value, c.opts.Now())
	if err != nil {
		return false
	}
	return token.SameBinding(claims.Binding, c.binding(r))
}

// Serve answers the request with a challenge page. After passing, the client
// is sent back to the address it asked for.
func (c *Challenge) Serve(w http.ResponseWriter, r *http.Request) {
	c.issue(w, r, SafeReturn(r.URL.RequestURI()), MessageNone)
}

// issue signs a new task for this client and writes the page for it.
func (c *Challenge) issue(w http.ResponseWriter, r *http.Request, ret string, msg Message) {
	nonce, err := token.NewNonce()
	if err != nil {
		// The system's random source failed. Nothing sensible can be issued.
		c.log.Error("no random numbers available", "error", err.Error())
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	now := c.opts.Now()
	task := c.opts.Signer.Sign(token.Challenge, token.Claims{
		Expires:    now.Add(c.opts.ChallengeLifetime),
		NotBefore:  now.Add(c.opts.Wait),
		Binding:    c.binding(r),
		Nonce:      nonce,
		Difficulty: c.opts.Difficulty,
	})
	c.opts.Page(w, r, View{
		Action:      VerifyPath,
		Token:       task,
		Return:      ret,
		Nonce:       nonce,
		Difficulty:  c.opts.Difficulty,
		AllowButton: c.opts.AllowButton,
		Message:     msg,
	})
}

// Handler answers requests under Prefix: it checks a client's answer and, if
// it is right, hands out the pass.
func (c *Challenge) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != VerifyPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			// Someone reloaded or bookmarked the address. Send them home.
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		form := r.PostForm
		ret := SafeReturn(form.Get("return"))

		// Is this a task we issued, to this client, and still open?
		now := c.opts.Now()
		claims, err := c.opts.Signer.Verify(token.Challenge, form.Get("token"), now)
		if err != nil || !token.SameBinding(claims.Binding, c.binding(r)) {
			c.reject(w, r, ret, MessageRetry)
			return
		}

		switch form.Get("method") {
		case MethodPoW:
			if !SolvesPoW(claims.Nonce, form.Get("solution"), claims.Difficulty) {
				c.reject(w, r, ret, MessageRetry)
				return
			}
		case MethodButton:
			if !c.opts.AllowButton {
				c.reject(w, r, ret, MessageRetry)
				return
			}
			if now.Before(claims.NotBefore) {
				c.reject(w, r, ret, MessageTooEarly)
				return
			}
		default:
			c.reject(w, r, ret, MessageRetry)
			return
		}

		c.solved.Add(1)
		pass := c.opts.Signer.Sign(token.Pass, token.Claims{
			Expires: now.Add(c.opts.PassLifetime),
			Binding: claims.Binding,
		})
		http.SetCookie(w, &http.Cookie{
			Name:     c.opts.CookieName,
			Value:    pass,
			Path:     "/",
			MaxAge:   int(c.opts.PassLifetime / time.Second),
			HttpOnly: true,
			Secure:   viaHTTPS(r),
			SameSite: http.SameSiteLaxMode,
		})
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, ret, http.StatusSeeOther)
	})
}

// reject counts a wrong answer and gives the client a fresh task. A client is
// never left at a dead end: whatever went wrong, it can simply try again.
func (c *Challenge) reject(w http.ResponseWriter, r *http.Request, ret string, msg Message) {
	c.failed.Add(1)
	c.issue(w, r, ret, msg)
}

// Counts returns how many answers were accepted and how many were rejected.
func (c *Challenge) Counts() (solved, failed uint64) {
	return c.solved.Load(), c.failed.Load()
}

// StripPass removes the pass cookie from requests before they are passed on,
// so the website never sees it. Other cookies are left exactly as sent.
func (c *Challenge) StripPass(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lines := r.Header["Cookie"]; len(lines) > 0 {
			kept := lines[:0:0]
			for _, line := range lines {
				if rest := removeCookie(line, c.opts.CookieName); rest != "" {
					kept = append(kept, rest)
				}
			}
			if len(kept) == 0 {
				r.Header.Del("Cookie")
			} else {
				r.Header["Cookie"] = kept
			}
		}
		next.ServeHTTP(w, r)
	})
}

// removeCookie returns a Cookie header line without the cookie called name.
func removeCookie(line, name string) string {
	if !strings.Contains(line, name) {
		return line // the common case, untouched
	}
	parts := strings.Split(line, ";")
	kept := parts[:0]
	for _, part := range parts {
		key, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if key != name && strings.TrimSpace(part) != "" {
			kept = append(kept, strings.TrimSpace(part))
		}
	}
	return strings.Join(kept, "; ")
}

// binding ties a token to this client: always to its user agent, and to its
// network if configured. A network rather than the single address is used so
// a visitor whose address changes within their provider's range is not asked
// again every time.
func (c *Challenge) binding(r *http.Request) string {
	network := "any"
	if c.opts.BindNetwork {
		network = "unknown"
		if info, ok := clientip.FromContext(r.Context()); ok && info.Client.IsValid() {
			network = networkOf(info.Client)
		}
	}
	return c.opts.Signer.Bind(network, r.Header.Get("User-Agent"))
}

// networkOf returns the /24 of an IPv4 address or the /64 of an IPv6 address.
func networkOf(addr netip.Addr) string {
	prefixBits := 64
	if addr.Is4() {
		prefixBits = 24
	}
	prefix, err := addr.Prefix(prefixBits)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

// viaHTTPS reports whether the visitor reached the site over HTTPS, which
// decides whether the pass cookie is marked Secure. Behind a trusted proxy
// that terminates TLS, the proxy's account is used.
func viaHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	info, ok := clientip.FromContext(r.Context())
	return ok && info.PeerTrusted && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SolvesPoW reports whether solution answers the proof of work: the SHA-256
// hash of nonce followed by solution must start with at least difficulty
// zero bits. solution is a decimal number written by the client.
func SolvesPoW(nonce, solution string, difficulty int) bool {
	if nonce == "" || solution == "" || len(solution) > maxSolutionLen {
		return false
	}
	if difficulty < MinDifficulty || difficulty > MaxDifficulty {
		return false
	}
	for i := 0; i < len(solution); i++ {
		if solution[i] < '0' || solution[i] > '9' {
			return false
		}
	}
	sum := sha256.Sum256([]byte(nonce + solution))
	first := uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
	return bits.LeadingZeros32(first) >= difficulty
}

// SafeReturn turns the address a client asks to be sent on to into one that
// is certain to be on this website. Anything that could lead elsewhere
// ("//other.example", "https://other.example", "/\other.example"), that
// contains control characters, or that points back into Xibalba's own
// address space becomes "/".
func SafeReturn(target string) string {
	if target == "" || len(target) > maxReturnLen || target[0] != '/' {
		return "/"
	}
	for i := 0; i < len(target); i++ {
		if target[i] < 0x20 || target[i] == 0x7f || target[i] == '\\' {
			return "/"
		}
	}
	if strings.HasPrefix(target, "//") {
		return "/"
	}
	u, err := url.Parse(target)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	if strings.HasPrefix(u.Path, "//") || strings.HasPrefix(u.Path, strings.TrimSuffix(Prefix, "/")) {
		return "/"
	}
	return target
}
