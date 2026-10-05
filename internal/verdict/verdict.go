// Package verdict answers a web server's question "may this request pass?".
//
// Normally Xibalba stands in the path: the web server hands it every request
// and Xibalba hands the good ones to the website. Some operators would
// rather keep their web server talking to the website directly and only ask
// Xibalba for a verdict on each request: nginx calls this auth_request,
// Caddy forward_auth, Traefik forwardAuth. This package is that mode.
//
// The web server sends a small request of its own to CheckPath. It carries
// the visitor's headers, and in X-Forwarded-Method, X-Forwarded-Uri and
// X-Forwarded-Host what the visitor asked for. The answer is 204 (let it
// pass), 401 (the security check is due) or 403 (refused); with 401 and 403
// comes the page to show. Caddy and Traefik show that page themselves.
// nginx throws it away, so it asks a second time at PagePath for the page
// alone; that second request is neither decided nor counted again.
//
// The web server routes requests by its own reading of an address, so
// nothing is passed because of how an address is written: every question
// is decided by the rules.
//
// The questions are believed only from a trusted proxy: anyone else could
// claim to ask on behalf of any address.
package verdict

import (
	"bytes"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
)

// The addresses of this package, inside Xibalba's own address space.
const (
	CheckPath = "/.xibalba/check"
	PagePath  = "/.xibalba/page"
)

// Headers.
const (
	// HeaderVerdict carries the outcome on the answer to a check ("pass",
	// "challenge", "deny", "limited", "unavailable"), and nginx hands it
	// back with the request for the page.
	HeaderVerdict = "X-Xibalba-Verdict"
	// HeaderRetry carries, with the request for the page, the Retry-After
	// value of the check's answer.
	HeaderRetry = "X-Xibalba-Retry-After"
)

const (
	maxURI    = 8192
	maxAnswer = 256 << 10 // the pages are a few KiB
	ownPrefix = "/.xibalba/"
	maxRetry  = 24 * time.Hour
	outPass   = "pass"
	outCheck  = "challenge"
	outDeny   = "deny"
	outLimit  = "limited"
	outBroken = "unavailable"
)

// Decider decides about requests. *gate.Gate is one.
type Decider interface {
	// Serve decides about r, counts it, and writes a page or calls next.
	// It returns "pass", "challenge", "deny", "limited" or "unavailable".
	Serve(w http.ResponseWriter, r *http.Request, next http.Handler) string
	// Page writes the page for a request Serve has decided about, without
	// counting it again.
	Page(w http.ResponseWriter, r *http.Request, limited bool, retryAfter time.Duration, next http.Handler) string
}

// Handler serves CheckPath and PagePath.
type Handler struct {
	decide Decider

	pass, challenge, deny, limited, unavailable, refused atomic.Uint64
}

// New returns a Handler that asks decide.
func New(decide Decider) *Handler { return &Handler{decide: decide} }

// Counts is how the checks ended.
type Counts struct {
	Pass, Challenge, Deny, Limited, Unavailable uint64
	// Refused are questions that were not answered: not from a trusted
	// proxy, or without a usable address.
	Refused uint64
}

// Counts returns how the checks ended so far.
func (h *Handler) Counts() Counts {
	return Counts{
		Pass: h.pass.Load(), Challenge: h.challenge.Load(), Deny: h.deny.Load(),
		Limited: h.limited.Load(), Unavailable: h.unavailable.Load(), Refused: h.refused.Load(),
	}
}

// ServeHTTP answers a check or a request for the page.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != CheckPath && r.URL.Path != PagePath {
		http.NotFound(w, r)
		return
	}
	asked, problem := original(r)
	if problem != "" {
		// Refused, and nothing more is said: with some web servers the
		// visitor gets to see this answer. Why is in the header, for the
		// operator who looks.
		h.refused.Add(1)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set(HeaderVerdict, "refused: "+problem)
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if r.URL.Path == PagePath {
		h.page(w, asked, r)
		return
	}
	// Every address is decided about, also one that looks like Xibalba's
	// own ("/.xibalba/../admin" is not): the web server decides where a
	// request goes, by rules of its own, and must not be told "pass" for
	// an address on the strength of how it is written. A web server set
	// up as in the examples never asks about Xibalba's own addresses.

	// The page goes into memory first: which status the answer gets is
	// known only when the decision is.
	rec := &recorder{header: http.Header{}}
	outcome := h.decide.Serve(rec, asked, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	switch outcome {
	case outPass:
		h.pass.Add(1)
		answer(w, nil, http.StatusNoContent, outcome)
	case outCheck:
		h.challenge.Add(1)
		answer(w, rec, http.StatusUnauthorized, outcome)
	case outDeny:
		h.deny.Add(1)
		answer(w, rec, http.StatusForbidden, outcome)
	case outLimit:
		h.limited.Add(1)
		answer(w, rec, http.StatusForbidden, outcome)
	default:
		h.unavailable.Add(1)
		status := rec.status
		if status < 500 {
			status = http.StatusServiceUnavailable
		}
		answer(w, rec, status, outBroken)
	}
}

// page writes the page for the visitor, with the status the page has
// anywhere else. A visitor who may pass by now is sent to where they
// wanted to go, where the web server asks again.
func (h *Handler) page(w http.ResponseWriter, asked, r *http.Request) {
	// A page is only due if the question was answered with a refusal. The
	// web server may land here for refusals of its own (a directory
	// without an index, its own access rules); those are not ours to
	// explain, and sending the visitor back would send them in a circle.
	switch r.Header.Get(HeaderVerdict) {
	case outCheck, outDeny, outLimit, outBroken:
	default:
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	limited := r.Header.Get(HeaderVerdict) == outLimit
	var retry time.Duration
	if seconds, err := strconv.Atoi(r.Header.Get(HeaderRetry)); err == nil && seconds > 0 {
		retry = min(time.Duration(seconds)*time.Second, maxRetry)
	}
	h.decide.Page(w, asked, limited, retry, http.HandlerFunc(func(w http.ResponseWriter, asked *http.Request) {
		target := asked.URL.RequestURI()
		if strings.HasPrefix(asked.URL.Path, ownPrefix) || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\\\r\n") {
			target = "/"
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusSeeOther)
	}))
}

// original rebuilds the request the visitor made from the web server's
// question about it. problem says why it cannot, or is empty.
func original(r *http.Request) (asked *http.Request, problem string) {
	info, ok := clientip.FromContext(r.Context())
	if !ok || !info.PeerTrusted {
		return nil, "checks are answered only for the web server in front (server.trusted_proxies)"
	}
	if len(r.Header.Values("X-Forwarded-Uri")) > 1 || len(r.Header.Values("X-Forwarded-Method")) > 1 {
		return nil, "the question names more than one address or method"
	}
	uri := r.Header.Get("X-Forwarded-Uri")
	if uri == "" || uri[0] != '/' || len(uri) > maxURI {
		return nil, "the header X-Forwarded-Uri has to hold the path the visitor asked for"
	}
	target, err := url.ParseRequestURI(uri)
	if err != nil || target.Host != "" || target.Opaque != "" || !strings.HasPrefix(target.Path, "/") {
		return nil, "the header X-Forwarded-Uri has to hold the path the visitor asked for"
	}
	method := r.Header.Get("X-Forwarded-Method")
	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(method)
	if !plainMethod(method) {
		return nil, "the header X-Forwarded-Method does not hold a request method"
	}
	asked = r.Clone(r.Context())
	asked.Method, asked.URL, asked.RequestURI = method, target, uri
	if host := r.Header.Get("X-Forwarded-Host"); host != "" && len(host) <= 255 {
		asked.Host = host
	}
	// A check carries no body, whatever the visitor's request says.
	asked.Body, asked.ContentLength = http.NoBody, 0
	for _, name := range []string{"X-Forwarded-Uri", "X-Forwarded-Method", "X-Forwarded-Host", HeaderVerdict, HeaderRetry} {
		asked.Header.Del(name)
	}
	return asked, ""
}

func plainMethod(method string) bool {
	if len(method) > 32 {
		return false
	}
	for i := 0; i < len(method); i++ {
		c := method[i]
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return method != ""
}

// answer writes the answer to a check: the recorded page, if any, under
// the status the web servers understand.
func answer(w http.ResponseWriter, rec *recorder, status int, outcome string) {
	h := w.Header()
	if rec != nil {
		for name, values := range rec.header {
			h[name] = values
		}
	}
	h.Set(HeaderVerdict, outcome)
	h.Set("Cache-Control", "no-store")
	if rec == nil || rec.body.Len() == 0 {
		h.Del("Content-Length")
		w.WriteHeader(status)
		return
	}
	h.Set("Content-Length", strconv.Itoa(rec.body.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(rec.body.Bytes())
}

// recorder keeps an answer in memory.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if room := maxAnswer - r.body.Len(); len(b) > room {
		r.body.Write(b[:max(room, 0)])
		return len(b), nil
	}
	return r.body.Write(b)
}
