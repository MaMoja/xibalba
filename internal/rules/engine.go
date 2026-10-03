package rules

import (
	"net/netip"
	"path"
	"regexp"
	"strings"
)

// Engine is a compiled rule set. Build one with Compile.
type Engine struct {
	rules         []compiledRule
	thresholds    []threshold // highest weight first
	defaultSource int
	sources       []Source
	usesCrawlers  bool
	usesTrap      bool
}

// UsesTrap reports whether any rule has a trapped condition.
func (e *Engine) UsesTrap() bool { return e.usesTrap }

// UsesCrawlers reports whether any rule has a crawler condition. If none
// has, nobody needs to find out which crawler a request claims to be.
func (e *Engine) UsesCrawlers() bool { return e.usesCrawlers }

type compiledRule struct {
	match  matcher
	action Action
	weight int
	source int // index into Engine.sources; -1 for weigh rules
}

type threshold struct {
	weight int
	source int
}

// Evaluate decides what happens to req. It never fails and does not allocate.
func (e *Engine) Evaluate(req *Request) Decision {
	score := 0
	for i := range e.rules {
		rule := &e.rules[i]
		if !rule.match.match(req) {
			continue
		}
		if rule.action == Weigh {
			score += rule.weight
			continue
		}
		return Decision{Action: rule.action, Source: rule.source, Weight: score}
	}
	for _, t := range e.thresholds {
		if score >= t.weight {
			return Decision{Action: e.sources[t.source].Action, Source: t.source, Weight: score}
		}
	}
	return Decision{Action: e.sources[e.defaultSource].Action, Source: e.defaultSource, Weight: score}
}

// Sources returns everything that can make a decision, in a stable order:
// the deciding rules in rule order, then the thresholds, then the default.
// Decision.Source is an index into this list. The caller must not change it.
func (e *Engine) Sources() []Source { return e.sources }

// Uses reports whether any rule, threshold or the default takes action.
func (e *Engine) Uses(action Action) bool {
	for _, s := range e.sources {
		if s.Action == action {
			return true
		}
	}
	return false
}

// Len returns the number of rules, including weigh rules.
func (e *Engine) Len() int { return len(e.rules) }

// NormalizePath returns the form of a request path that rules are tested
// against. The website behind Xibalba may treat "/a//b", "/a/./b", "/x/../a/b"
// and "\a\b" as the same place as "/a/b"; a rule that protects "/a/b" must
// not be dodged by spelling it differently. So the path is reduced to its
// shortest equivalent form: backslashes become slashes, path parameters
// (";..." within a segment, which some application servers strip before
// routing) are removed, repeated slashes collapse, and "." and ".." segments
// are resolved. A trailing slash is kept.
//
// Normalisation only ever makes more spellings match a rule, never fewer. The
// website still receives the path exactly as the client sent it.
//
// p is the path after percent-decoding (http.Request.URL.Path).
func NormalizePath(p string) string {
	if strings.IndexByte(p, '\\') >= 0 {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	if strings.IndexByte(p, ';') >= 0 {
		p = stripPathParams(p)
	}
	if p == "" || p[0] != '/' {
		p = "/" + p
	}
	if isClean(p) {
		return p // the common case, without allocating
	}
	cleaned := path.Clean(p)
	if cleaned != "/" && strings.HasSuffix(p, "/") {
		cleaned += "/"
	}
	return cleaned
}

// stripPathParams removes everything from a ";" to the end of its segment.
func stripPathParams(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	skip := false
	for i := 0; i < len(p); i++ {
		switch {
		case p[i] == '/':
			skip = false
			b.WriteByte('/')
		case p[i] == ';':
			skip = true
		case !skip:
			b.WriteByte(p[i])
		}
	}
	return b.String()
}

// isClean reports whether a path starting with "/" has no empty, "." or ".." segment.
func isClean(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] != '/' {
			continue
		}
		rest := p[i+1:]
		switch {
		case strings.HasPrefix(rest, "/"):
			return false
		case rest == "." || rest == "..":
			return false
		case strings.HasPrefix(rest, "./") || strings.HasPrefix(rest, "../"):
			return false
		}
	}
	return true
}

// NormalizeHost returns the form of a Host header that rules are tested
// against: lower case, without port, without a trailing dot.
func NormalizeHost(host string) string {
	if strings.HasPrefix(host, "[") { // IPv6 literal, with or without port
		if end := strings.IndexByte(host, ']'); end > 0 {
			host = host[1:end]
		}
	} else if i := strings.LastIndexByte(host, ':'); i >= 0 && strings.IndexByte(host, ':') == i {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ".")
	if isLowerASCII(host) {
		return host
	}
	return asciiLower(host)
}

// matcher is one compiled condition.
type matcher interface {
	match(*Request) bool
}

type never struct{}

func (never) match(*Request) bool { return false }

type allOf []matcher

func (a allOf) match(r *Request) bool {
	for _, m := range a {
		if !m.match(r) {
			return false
		}
	}
	return true
}

type anyOf []matcher

func (a anyOf) match(r *Request) bool {
	for _, m := range a {
		if m.match(r) {
			return true
		}
	}
	return false
}

type notOf struct{ inner matcher }

func (n notOf) match(r *Request) bool { return !n.inner.match(r) }

type methodIn []string

func (m methodIn) match(r *Request) bool {
	for _, method := range m {
		if method == r.Method {
			return true
		}
	}
	return false
}

type ipIn []netip.Prefix

func (p ipIn) match(r *Request) bool {
	if !r.Client.IsValid() {
		return false
	}
	for _, prefix := range p {
		if prefix.Contains(r.Client) {
			return true
		}
	}
	return false
}

// trappedIs tests whether the client followed the trap link.
type trappedIs bool

func (t trappedIs) match(r *Request) bool { return r.Trapped == bool(t) }

// crawlerMatch tests the crawler a request claims to be.
type crawlerMatch struct {
	classes []string // nil: any class
	names   []string // nil: any name
	any     bool     // the claim alone is enough
	status  CrawlerStatus
}

func (m crawlerMatch) match(r *Request) bool {
	cr := &r.Crawler
	if cr.Status == CrawlerNone {
		return false
	}
	if !m.any && cr.Status != m.status {
		return false
	}
	if m.classes != nil && !inList(m.classes, cr.Class) {
		return false
	}
	return m.names == nil || inList(m.names, cr.Name)
}

func inList(list []string, s string) bool {
	for _, entry := range list {
		if entry == s {
			return true
		}
	}
	return false
}

type field int

const (
	fieldHost field = iota
	fieldPath
	fieldUserAgent
)

type fieldMatch struct {
	field field
	text  textMatcher
}

func (f fieldMatch) match(r *Request) bool {
	switch f.field {
	case fieldHost:
		return f.text.matches(r.Host)
	case fieldPath:
		return f.text.matches(r.Path)
	default:
		return f.text.matches(r.UserAgent)
	}
}

type headerMatch struct {
	name string // canonical form
	text textMatcher
}

func (h headerMatch) match(r *Request) bool {
	values := r.Header[h.name]
	switch h.text.op {
	case opPresent:
		return len(values) > 0
	case opAbsent:
		return len(values) == 0
	}
	for _, v := range values {
		if h.text.matches(v) {
			return true
		}
	}
	return false
}

type op int

const (
	opEquals op = iota
	opContains
	opPrefix
	opSuffix
	opRegex
	opPresent
	opAbsent
)

// textMatcher tests one string. With fold set, needle is lower case and the
// comparison ignores the case of ASCII letters.
type textMatcher struct {
	op     op
	fold   bool
	needle string
	re     *regexp.Regexp
}

func (t textMatcher) matches(s string) bool {
	switch t.op {
	case opRegex:
		return t.re.MatchString(s)
	case opEquals:
		if t.fold {
			return equalFold(s, t.needle)
		}
		return s == t.needle
	case opContains:
		if t.fold {
			return containsFold(s, t.needle)
		}
		return strings.Contains(s, t.needle)
	case opPrefix:
		if t.fold {
			return len(s) >= len(t.needle) && equalFold(s[:len(t.needle)], t.needle)
		}
		return strings.HasPrefix(s, t.needle)
	case opSuffix:
		if t.fold {
			return len(s) >= len(t.needle) && equalFold(s[len(s)-len(t.needle):], t.needle)
		}
		return strings.HasSuffix(s, t.needle)
	}
	return false
}

// The helpers below compare without regard to the case of ASCII letters and
// without allocating. Bytes outside ASCII must be equal. That is the right
// rule for HTTP: methods, header names, hosts and nearly all user agents are
// ASCII, and it cannot be confused by Unicode case-folding surprises.

func lowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func isLowerASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return false
		}
	}
	return true
}

func asciiLower(s string) string {
	if isLowerASCII(s) {
		return s
	}
	b := []byte(s)
	for i := range b {
		b[i] = lowerByte(b[i])
	}
	return string(b)
}

// equalFold reports whether s equals lower, which must already be lower case.
func equalFold(s, lower string) bool {
	if len(s) != len(lower) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if lowerByte(s[i]) != lower[i] {
			return false
		}
	}
	return true
}

// containsFold reports whether s contains lower, which must already be lower case.
func containsFold(s, lower string) bool {
	n := len(lower)
	if n == 0 {
		return true
	}
	first := lower[0]
	for i := 0; i+n <= len(s); i++ {
		if lowerByte(s[i]) == first && equalFold(s[i:i+n], lower) {
			return true
		}
	}
	return false
}
