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
	"crypto/subtle"
	"encoding/hex"
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

// The kinds of check.
const (
	// MethodPoW is the proof of work, solved by JavaScript in the browser.
	MethodPoW = "pow"
	// MethodScript only asks the browser to run a small script and wait.
	MethodScript = "script"
	// MethodWait asks for nothing but patience: wait, then press a button.
	// It works without JavaScript.
	MethodWait = "wait"
	// MethodRefresh is MethodWait without the button: the page sends the
	// browser on by itself when the wait is over.
	MethodRefresh = "refresh"
)

// Methods lists the kinds of check.
var Methods = []string{MethodPoW, MethodScript, MethodWait, MethodRefresh}

// The extra checks that can be added to a method that runs JavaScript.
const (
	// CheckCSS makes sure the browser loads and applies a style sheet.
	CheckCSS = "css"
	// CheckHeadless looks for signs that a program steers the browser.
	CheckHeadless = "headless"
)

// Checks lists the extra checks.
var Checks = []string{CheckCSS, CheckHeadless}

const (
	bitCSS      uint8 = 1 << iota // the pass was earned with the style sheet check
	bitHeadless                   // … with the check for automation
	bitButton                     // task only: the path without JavaScript is open
)

// answerButton is what the form says when it is sent without JavaScript.
const answerButton = "button"

// CSSPath is where the style sheet of CheckCSS is fetched from.
const CSSPath = Prefix + "check.css"

// Message tells the page what to say about a previous attempt.
type Message string

// Messages.
const (
	MessageNone Message = ""
	// MessageTooEarly: the answer came before the wait was over.
	MessageTooEarly Message = "too_early"
	// MessageRetry: the answer was wrong or the task had expired.
	MessageRetry Message = "retry"
	// MessageAutomated: the browser says a program steers it.
	MessageAutomated Message = "automated"
)

// Profile is one way of checking a client: what a rule asks for.
type Profile struct {
	// Method is the kind of check.
	Method string
	// Difficulty is the proof of work in leading zero bits (MethodPoW).
	Difficulty int
	// Wait is how long the client has to wait: the whole check for
	// MethodWait and MethodRefresh, the time before the script answers
	// for MethodScript, and the time before the button counts for the
	// path without JavaScript.
	Wait time.Duration
	// Checks are the extra checks. They need JavaScript.
	Checks []string
	// AllowButton opens a path without JavaScript for MethodPoW and
	// MethodScript: wait, then press a button. It cannot be combined with
	// Checks.
	AllowButton bool
}

// usesScript reports whether the method is answered by JavaScript.
func (p Profile) usesScript() bool { return p.Method == MethodPoW || p.Method == MethodScript }

// bits returns the extra checks as bits.
func (p Profile) bits() (mask uint8) {
	if !p.usesScript() {
		return 0
	}
	for _, check := range p.Checks {
		switch check {
		case CheckCSS:
			mask |= bitCSS
		case CheckHeadless:
			mask |= bitHeadless
		}
	}
	return mask
}

// button reports whether the path without JavaScript is open.
func (p Profile) button() bool { return p.usesScript() && p.AllowButton && p.bits() == 0 }

// level orders checks by how much they ask of a client. A pass earned at
// one level also counts where a lower one is asked for.
func (p Profile) level() int {
	switch p.Method {
	case MethodPoW:
		return 10 + p.Difficulty
	case MethodScript:
		return 2
	default:
		return 1
	}
}

// View is what the challenge page needs to show one task.
type View struct {
	// Action is where the form is sent.
	Action string
	// Token is the signed task.
	Token string
	// Return is where the client is sent after passing.
	Return string
	// Method is the kind of check.
	Method string
	// Nonce and Difficulty describe the proof of work.
	Nonce      string
	Difficulty int
	// WaitSeconds is how long the client has to wait, rounded up.
	WaitSeconds int
	// AllowButton says whether the page offers a button: always for
	// MethodWait, and as the path without JavaScript otherwise.
	AllowButton bool
	// RefreshURL, for MethodRefresh, is where the page sends the browser
	// by itself after WaitSeconds.
	RefreshURL string
	// StyleURL, for CheckCSS, is the style sheet the page has to load.
	StyleURL string
	// Headless says that the script has to report on automation.
	Headless bool
	// Message is a note about the previous attempt.
	Message Message
}

// Options configures a Challenge.
type Options struct {
	// Signer signs tasks and passes.
	Signer *token.Signer
	// Default is the check used where a rule asks for none in particular.
	Default Profile
	// ChallengeLifetime is how long a client has to answer.
	ChallengeLifetime time.Duration
	// PassLifetime is how long a client is not asked again after passing.
	PassLifetime time.Duration
	// BindNetwork ties tasks and passes to the client's network as well
	// as its user agent.
	BindNetwork bool
	// CookieName is the name of the pass cookie.
	CookieName string
	// Page writes the challenge page.
	Page func(w http.ResponseWriter, r *http.Request, v View)
	// Log receives messages.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Challenge issues tasks, checks answers and recognises passes.
// It is safe for concurrent use.
type Challenge struct {
	opts Options
	log  *slog.Logger

	solved    atomic.Uint64
	failed    atomic.Uint64
	automated atomic.Uint64
}

// New returns a Challenge for opts.
func New(opts Options) *Challenge {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Challenge{opts: opts, log: opts.Log.With("component", "challenge")}
}

// profile returns p, or the default if p is nil.
func (c *Challenge) profile(p *Profile) Profile {
	if p == nil {
		return c.opts.Default
	}
	return *p
}

// pass returns what the request's pass states, if it carries a valid one
// issued to this client.
func (c *Challenge) pass(r *http.Request) (token.Claims, bool) {
	cookie, err := r.Cookie(c.opts.CookieName)
	if err != nil {
		return token.Claims{}, false
	}
	claims, err := c.opts.Signer.Verify(token.Pass, cookie.Value, c.opts.Now())
	if err != nil || !token.SameBinding(claims.Binding, c.binding(r)) {
		return token.Claims{}, false
	}
	return claims, true
}

// Passed reports whether the request carries a valid pass, issued to this
// client, that was earned with a check at least as demanding as want (nil:
// the default check).
func (c *Challenge) Passed(r *http.Request, want *Profile) bool {
	claims, ok := c.pass(r)
	if !ok {
		return false
	}
	p := c.profile(want)
	return claims.Level >= p.level() && claims.Checks&p.bits() == p.bits()
}

// Serve answers the request with a challenge page for the check want (nil:
// the default check). After passing, the client is sent back to the address
// it asked for.
func (c *Challenge) Serve(w http.ResponseWriter, r *http.Request, want *Profile) {
	c.issue(w, r, SafeReturn(r.URL.RequestURI()), MessageNone, c.profile(want))
}

// issue signs a new task for this client and writes the page for it.
func (c *Challenge) issue(w http.ResponseWriter, r *http.Request, ret string, msg Message, p Profile) {
	nonce, err := token.NewNonce()
	if err != nil {
		// The system's random source failed. Nothing sensible can be issued.
		c.log.Error("no random numbers available", "error", err.Error())
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	now := c.opts.Now()
	checks := p.bits()
	if p.button() {
		checks |= bitButton
	}
	task := c.opts.Signer.Sign(token.Challenge, token.Claims{
		Expires:    now.Add(c.opts.ChallengeLifetime),
		NotBefore:  now.Add(p.Wait),
		Binding:    c.binding(r),
		Nonce:      nonce,
		Difficulty: p.Difficulty,
		Method:     p.Method,
		Checks:     checks,
		Level:      p.level(),
	})
	v := View{
		Action:      VerifyPath,
		Token:       task,
		Return:      ret,
		Method:      p.Method,
		Nonce:       nonce,
		Difficulty:  p.Difficulty,
		WaitSeconds: int((p.Wait + time.Second - 1) / time.Second),
		AllowButton: p.button() || !p.usesScript(),
		Headless:    checks&bitHeadless != 0,
		Message:     msg,
	}
	if p.Method == MethodRefresh {
		v.RefreshURL = VerifyPath + "?" + url.Values{"token": {task}, "return": {ret}}.Encode()
	}
	if checks&bitCSS != 0 {
		v.StyleURL = CSSPath + "?" + url.Values{"n": {nonce}}.Encode()
	}
	c.opts.Page(w, r, v)
}

// scriptAnswer is what the script of MethodScript has to send: something a
// browser that runs the script arrives at, and a client that only reads
// the page does not.
func scriptAnswer(nonce string) string {
	sum := sha256.Sum256([]byte(nonce + "0"))
	return hex.EncodeToString(sum[:4])
}

// Handler answers requests under Prefix: it checks a client's answer and, if
// it is right, hands out the pass. It also serves the style sheet of
// CheckCSS.
func (c *Challenge) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == CSSPath && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			c.style(w, r)
			return
		}
		if r.URL.Path != VerifyPath {
			http.NotFound(w, r)
			return
		}
		var form url.Values
		switch r.Method {
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
			if err := r.ParseForm(); err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}
			form = r.PostForm
		case http.MethodGet:
			// MethodRefresh: the browser was sent here by the page itself.
			// Anything else is someone who reloaded or bookmarked the
			// address; send them home.
			form = r.URL.Query()
			if form.Get("token") == "" || len(r.URL.RawQuery) > maxFormBytes {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			form.Set("method", MethodRefresh)
		default:
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		ret := SafeReturn(form.Get("return"))

		// Is this a task we issued, to this client, and still open?
		now := c.opts.Now()
		claims, err := c.opts.Signer.Verify(token.Challenge, form.Get("token"), now)
		if err != nil || !token.SameBinding(claims.Binding, c.binding(r)) {
			// The task says nothing we can believe, so the kind of check
			// cannot be taken from it: the client gets the default one. If
			// a rule asks for more, the next request says so.
			c.reject(w, r, ret, MessageRetry, c.opts.Default)
			return
		}
		// What the task was: taken from the signed task, never from the form.
		p := Profile{Method: claims.Method, Difficulty: claims.Difficulty, Wait: claims.NotBefore.Sub(now),
			AllowButton: claims.Checks&bitButton != 0}
		if claims.Checks&bitCSS != 0 {
			p.Checks = append(p.Checks, CheckCSS)
		}
		if claims.Checks&bitHeadless != 0 {
			p.Checks = append(p.Checks, CheckHeadless)
		}
		if p.Wait < time.Second {
			p.Wait = time.Second // for the fresh task after a wrong answer
		}
		answer := form.Get("method")
		early := now.Before(claims.NotBefore)

		switch {
		case r.Method == http.MethodGet && claims.Method != MethodRefresh:
			c.reject(w, r, ret, MessageRetry, p)
			return
		case answer == answerButton || claims.Method == MethodWait || claims.Method == MethodRefresh:
			// Waiting is the whole check: by design for wait and refresh,
			// as the path without JavaScript otherwise.
			if p.usesScript() && claims.Checks&bitButton == 0 {
				c.reject(w, r, ret, MessageRetry, p)
				return
			}
			if early {
				c.reject(w, r, ret, MessageTooEarly, p)
				return
			}
		case answer == MethodPoW && claims.Method == MethodPoW:
			if !SolvesPoW(claims.Nonce, form.Get("solution"), claims.Difficulty) {
				c.reject(w, r, ret, MessageRetry, p)
				return
			}
		case answer == MethodScript && claims.Method == MethodScript:
			if subtle.ConstantTimeCompare([]byte(form.Get("solution")), []byte(scriptAnswer(claims.Nonce))) != 1 {
				c.reject(w, r, ret, MessageRetry, p)
				return
			}
			if early {
				c.reject(w, r, ret, MessageTooEarly, p)
				return
			}
		default:
			c.reject(w, r, ret, MessageRetry, p)
			return
		}

		// The extra checks. They belong to the answers given by the script.
		if claims.Checks&bitCSS != 0 {
			want := c.opts.Signer.MAC("css", claims.Nonce)
			if subtle.ConstantTimeCompare([]byte(form.Get("css")), []byte(want)) != 1 {
				c.reject(w, r, ret, MessageRetry, p)
				return
			}
		}
		if claims.Checks&bitHeadless != 0 {
			// The script reports what it found ("ok" or the signs it saw)
			// and the browser's own idea of its name, which must be the
			// name the request carries.
			report, seen := form.Get("probe"), form.Get("agent")
			if report != "ok" || seen != r.Header.Get("User-Agent") {
				c.automated.Add(1)
				c.reject(w, r, ret, MessageAutomated, p)
				return
			}
		}

		// A pass already held keeps what it was earned with: a client that
		// met a harder check elsewhere is not asked for it again because an
		// easier one came in between.
		level, checks := claims.Level, claims.Checks&^bitButton
		if held, ok := c.pass(r); ok {
			level, checks = max(level, held.Level), checks|held.Checks
		}
		c.solved.Add(1)
		pass := c.opts.Signer.Sign(token.Pass, token.Claims{
			Expires: now.Add(c.opts.PassLifetime),
			Binding: claims.Binding,
			Level:   level,
			Checks:  checks,
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

// style serves the style sheet of CheckCSS: one value, which the script
// reads back from the page once the browser has applied it.
func (c *Challenge) style(w http.ResponseWriter, r *http.Request) {
	nonce := r.URL.Query().Get("n")
	if nonce == "" || len(nonce) > 64 {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/css; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(":root{--xibalba-check:\"" + c.opts.Signer.MAC("css", nonce) + "\"}\n"))
}

// reject counts a wrong answer and gives the client a fresh task. A client is
// never left at a dead end: whatever went wrong, it can simply try again.
func (c *Challenge) reject(w http.ResponseWriter, r *http.Request, ret string, msg Message, p Profile) {
	c.failed.Add(1)
	c.issue(w, r, ret, msg, p)
}

// Automated returns how many answers were rejected because the browser
// reported that a program steers it.
func (c *Challenge) Automated() uint64 { return c.automated.Load() }

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
