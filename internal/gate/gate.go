// Package gate enforces rule decisions on live requests.
//
// It is the stage of the request pipeline between client identity and the
// website. For every request it asks the rule engine for a decision, counts
// it, and carries it out: the request is passed on, or the visitor gets the
// "blocked" page.
//
// The gate records which rule decided how often, and nothing about who was
// decided upon. No address, path or user agent is stored.
package gate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
	"github.com/MaMoja/xibalba/internal/health"
	"github.com/MaMoja/xibalba/internal/rules"
)

// errorWindow is how long after an evaluation failure the gate reports itself
// as degraded.
const errorWindow = 5 * time.Minute

// Evaluator decides about requests. *rules.Engine is the implementation; the
// interface exists so the gate depends only on what it uses.
type Evaluator interface {
	// Evaluate returns the decision for one request.
	Evaluate(*rules.Request) rules.Decision
	// Sources lists everything that can decide. Decision.Source indexes it.
	Sources() []rules.Source
}

// Challenger makes a client pass a check before it is let through.
// *challenge.Challenge is the implementation.
type Challenger interface {
	// Passed reports whether the request carries a valid pass that was
	// earned with a check at least as demanding as want (nil: the default).
	Passed(r *http.Request, want *rules.ChallengeSpec) bool
	// Serve answers the request with the challenge page for the check
	// want (nil: the default).
	Serve(w http.ResponseWriter, r *http.Request, want *rules.ChallengeSpec)
	// Counts returns how many answers were accepted and rejected.
	Counts() (solved, failed uint64)
}

// Options configures a Gate.
type Options struct {
	// Engine is the compiled rule set.
	Engine Evaluator
	// DryRun evaluates and counts every decision but passes every request on.
	DryRun bool
	// FailOpen says what happens if evaluating a request fails inside
	// Xibalba: true passes the request on, false refuses it.
	FailOpen bool
	// Identify says which crawler a request claims to be. If nil, crawler
	// conditions never match.
	Identify func(userAgent string, client netip.Addr) rules.Crawler
	// Peek is Identify without side effects, for Explain. If nil, Explain
	// uses Identify.
	Peek func(userAgent string, client netip.Addr) rules.Crawler
	// Country says which country a client's address is registered in, and
	// whether a country database is loaded at all. If nil, or while no
	// database is loaded, rules with a country condition are skipped.
	Country func(client netip.Addr) (code [2]byte, loaded bool)
	// Trapped says whether a client recently followed the hidden trap
	// link. If nil, trapped conditions see "no".
	Trapped func(client netip.Addr) bool
	// Limit counts a request from a client and reports whether the client
	// is over a request limit and, if so, whether further requests are
	// refused (deny) or have to pass the check. If nil, nothing is limited.
	// Requests let through by a rule marked exempt_from_limits are neither
	// counted nor limited.
	Limit func(client netip.Addr) (over, deny bool, retryAfter time.Duration)
	// Page is told when the website answered a counted request with a
	// page (a successful answer of type text/html), for limits that count
	// different pages. If nil, answers are not looked at.
	Page func(client netip.Addr, path, query string)
	// Origin is told the client and the outcome ("allow", "challenge" or
	// "deny") of every evaluated request, for counts per network of
	// origin. If nil, nothing is told.
	Origin func(client netip.Addr, outcome string)
	// Limited writes the page for a request refused by a limit.
	Limited func(w http.ResponseWriter, r *http.Request, retryAfter time.Duration)
	// Challenge handles requests whose decision is "challenge". If nil,
	// such requests are passed on.
	Challenge Challenger
	// Next receives the requests that are passed on.
	Next http.Handler
	// Blocked writes the page for a denied request. reference identifies
	// the rule that denied it.
	Blocked func(w http.ResponseWriter, r *http.Request, reference string)
	// Unavailable writes the page shown when a request is refused because
	// of a failure inside Xibalba.
	Unavailable func(w http.ResponseWriter, r *http.Request, status int)
	// Log receives the gate's messages.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Gate is the http.Handler that enforces decisions.
type Gate struct {
	opts  Options
	log   *slog.Logger
	set   atomic.Pointer[ruleSet] // the rule set in force; replaced as a whole by Swap
	since time.Time

	challengesServed atomic.Uint64 // challenge pages shown
	challengesPassed atomic.Uint64 // requests let through on a valid pass

	failures atomic.Uint64
	mu       sync.Mutex
	lastFail time.Time
	lastMsg  string
}

// ruleSet is one compiled rule set with its counters.
type ruleSet struct {
	engine  Evaluator
	sources []rules.Source
	counts  []atomic.Uint64 // one per source, same order
	trusted []bool          // per source: exempt from the request limits
}

func newRuleSet(engine Evaluator) *ruleSet {
	sources := engine.Sources()
	set := &ruleSet{engine: engine, sources: sources, counts: make([]atomic.Uint64, len(sources)), trusted: make([]bool, len(sources))}
	for i, s := range sources {
		set.trusted[i] = s.ExemptFromLimits
	}
	return set
}

// Swap puts a new rule set in force without interrupting requests: one
// already being decided finishes under the old set, the next one uses the
// new. Counters of rules that exist in both, with the same outcome, carry on.
func (g *Gate) Swap(engine Evaluator) {
	next, old := newRuleSet(engine), g.set.Load()
	type key struct {
		id     string
		action rules.Action
	}
	before := make(map[key]uint64, len(old.sources))
	for i, s := range old.sources {
		before[key{s.ID, s.Action}] = old.counts[i].Load()
	}
	for i, s := range next.sources {
		next.counts[i].Store(before[key{s.ID, s.Action}])
	}
	g.set.Store(next)
}

// New returns a Gate for opts.
func New(opts Options) *Gate {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	g := &Gate{opts: opts, log: opts.Log.With("component", "rules"), since: opts.Now()}
	g.set.Store(newRuleSet(opts.Engine))
	return g
}

// What became of a request, as Serve and Page report it.
const (
	OutcomePass        = "pass"        // handed to the next handler
	OutcomeChallenge   = "challenge"   // answered with the security check
	OutcomeDeny        = "deny"        // answered with the block page
	OutcomeLimited     = "limited"     // answered with "too many requests"
	OutcomeUnavailable = "unavailable" // refused because evaluating failed
)

// ServeHTTP decides what happens to the request and carries it out.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.Serve(w, r, g.opts.Next) }

// Serve decides what happens to the request and carries it out: it writes
// a page, or hands the request to next. It returns which (see the Outcome
// constants).
func (g *Gate) Serve(w http.ResponseWriter, r *http.Request, next http.Handler) string {
	decision, client, set, ok := g.decide(r, g.opts.Identify)
	if !ok {
		if g.opts.FailOpen {
			next.ServeHTTP(w, r)
			return OutcomePass
		}
		g.opts.Unavailable(w, r, http.StatusServiceUnavailable)
		return OutcomeUnavailable
	}

	set.counts[decision.Source].Add(1)
	if g.log.Enabled(r.Context(), slog.LevelDebug) {
		g.log.Debug("decision",
			"action", string(decision.Action),
			"source", set.sources[decision.Source].ID,
			"weight", decision.Weight,
			"enforced", !g.opts.DryRun,
		)
	}

	// Request limits. Only a request let through by a rule that the site
	// owner marked as exempt is not counted. A limit can only make the
	// outcome stricter.
	var over, refuse bool
	var retryAfter time.Duration
	if g.opts.Limit != nil && !set.trusted[decision.Source] {
		over, refuse, retryAfter = g.opts.Limit(client)
		if g.opts.Page != nil {
			// Whether this is a page is known when the website answers.
			w = &pageWatch{ResponseWriter: w, found: func() {
				g.opts.Page(client, rules.NormalizePath(r.URL.Path), r.URL.RawQuery)
			}}
		}
	}

	action := decision.Action
	limited := over && refuse && action != rules.Deny
	if over && action != rules.Deny {
		action = rules.Challenge
	}
	if g.opts.Origin != nil {
		// Counted as decided, also while nothing is enforced.
		switch {
		case limited || action == rules.Deny:
			g.opts.Origin(client, "deny")
		case action == rules.Challenge:
			g.opts.Origin(client, "challenge")
		default:
			g.opts.Origin(client, "allow")
		}
	}
	if g.opts.DryRun {
		next.ServeHTTP(w, r)
		return OutcomePass
	}
	if limited {
		g.opts.Limited(w, r, retryAfter)
		return OutcomeLimited
	}
	// Which security check: the one the deciding rule asks for. A check
	// that a request limit brought about is the default one.
	var want *rules.ChallengeSpec
	if decision.Action == rules.Challenge {
		want = set.sources[decision.Source].Challenge
	}
	switch action {
	case rules.Deny:
		g.opts.Blocked(w, r, set.sources[decision.Source].Reference)
		return OutcomeDeny
	case rules.Challenge:
		switch {
		case g.opts.Challenge == nil:
		case g.opts.Challenge.Passed(r, want):
			g.challengesPassed.Add(1)
		default:
			g.challengesServed.Add(1)
			g.opts.Challenge.Serve(w, r, want)
			return OutcomeChallenge
		}
	}
	next.ServeHTTP(w, r)
	return OutcomePass
}

// Page writes the page for a request that Serve has already decided not to
// let through, without deciding or counting it a second time. It is for
// set-ups in which the answer of Serve cannot be shown to the visitor and
// the page is asked for separately (see internal/verdict).
//
// The rules are evaluated again, quietly: nothing is counted, no limit is
// touched, no lookup is started. What the limits said the first time cannot
// be learned again without counting, so the caller passes it on: limited
// and retryAfter. If the request would be let through by now (the visitor
// passed the check meanwhile), it goes to next.
func (g *Gate) Page(w http.ResponseWriter, r *http.Request, limited bool, retryAfter time.Duration, next http.Handler) string {
	identify := g.opts.Peek
	if identify == nil {
		identify = g.opts.Identify
	}
	decision, _, set, ok := g.decide(r, identify)
	switch {
	case !ok && g.opts.FailOpen, g.opts.DryRun:
		next.ServeHTTP(w, r)
		return OutcomePass
	case !ok:
		g.opts.Unavailable(w, r, http.StatusServiceUnavailable)
		return OutcomeUnavailable
	case decision.Action == rules.Deny:
		g.opts.Blocked(w, r, set.sources[decision.Source].Reference)
		return OutcomeDeny
	case limited:
		g.opts.Limited(w, r, retryAfter)
		return OutcomeLimited
	}
	var want *rules.ChallengeSpec
	if decision.Action == rules.Challenge {
		want = set.sources[decision.Source].Challenge
	}
	// An allowed request gets here only if a limit asked for the check.
	if g.opts.Challenge == nil || g.opts.Challenge.Passed(r, want) {
		next.ServeHTTP(w, r)
		return OutcomePass
	}
	g.opts.Challenge.Serve(w, r, want)
	return OutcomeChallenge
}

// pageWatch looks at the answer the website gives and reports a page: a
// successful answer of type text/html. Going by the answer instead of the
// request means a client cannot disguise the pages it reads as something
// else, and an address that merely looks like a page does not count.
type pageWatch struct {
	http.ResponseWriter
	found func()
	done  bool
}

func (p *pageWatch) WriteHeader(status int) {
	if !p.done && status >= 200 { // 1xx answers come before the real one
		p.done = true
		if status < 300 && isHTML(p.Header().Get("Content-Type")) {
			p.found()
		}
	}
	p.ResponseWriter.WriteHeader(status)
}

func (p *pageWatch) Write(b []byte) (int, error) {
	if !p.done {
		p.WriteHeader(http.StatusOK)
	}
	return p.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer, so streaming
// and upgraded connections (websockets) work as without the watch.
func (p *pageWatch) Unwrap() http.ResponseWriter { return p.ResponseWriter }

func isHTML(contentType string) bool {
	const html = "text/html"
	return len(contentType) >= len(html) && strings.EqualFold(contentType[:len(html)], html)
}

// decide evaluates the request. The engine is built so that it cannot fail,
// but this stage stands in front of someone's website: if it fails anyway,
// the failure is contained here and the configured answer applies.
func (g *Gate) decide(r *http.Request, identify func(string, netip.Addr) rules.Crawler) (decision rules.Decision, client netip.Addr, set *ruleSet, ok bool) {
	defer func() {
		if p := recover(); p != nil {
			g.recordFailure(fmt.Sprint(p))
			ok = false
		}
	}()

	info, _ := clientip.FromContext(r.Context())
	req := g.request(r, info.Client, identify)
	set = g.set.Load()
	return set.engine.Evaluate(&req), req.Client, set, true
}

// request gathers what the rules may ask about a request from client.
func (g *Gate) request(r *http.Request, client netip.Addr, identify func(string, netip.Addr) rules.Crawler) rules.Request {
	req := rules.Request{
		Method:      strings.ToUpper(r.Method),
		Host:        rules.NormalizeHost(r.Host),
		Path:        rules.NormalizePath(r.URL.Path),
		PathAltered: rules.PathAltered(r.URL.Path, r.URL.RawPath),
		Query:       r.URL.RawQuery,
		UserAgent:   r.Header.Get("User-Agent"),
		Header:      r.Header,
		Client:      client,
	}
	req.NoCountryData = true
	if g.opts.Country != nil {
		var loaded bool
		req.Country, loaded = g.opts.Country(req.Client)
		req.NoCountryData = !loaded
	}
	if g.opts.Trapped != nil {
		req.Trapped = g.opts.Trapped(req.Client)
	}
	if identify != nil {
		req.Crawler = identify(req.UserAgent, req.Client)
	}
	return req
}

// Explain says what would happen to r if it came from client, under engine
// (nil: the rule set in force). Nothing is counted and nothing is carried
// out; request limits are not looked at. ok is false if evaluating failed.
func (g *Gate) Explain(r *http.Request, client netip.Addr, engine Evaluator) (action rules.Action, source string, weight int, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if engine == nil {
		engine = g.set.Load().engine
	}
	identify := g.opts.Peek
	if identify == nil {
		identify = g.opts.Identify
	}
	req := g.request(r, client, identify)
	decision := engine.Evaluate(&req)
	return decision.Action, engine.Sources()[decision.Source].ID, decision.Weight, true
}

func (g *Gate) recordFailure(msg string) {
	g.failures.Add(1)
	g.mu.Lock()
	first := g.lastFail.IsZero() || g.opts.Now().Sub(g.lastFail) > errorWindow
	g.lastFail, g.lastMsg = g.opts.Now(), msg
	g.mu.Unlock()
	// One log line per burst, not per request: a failure that hits every
	// request must not flood the log.
	if first {
		g.log.Error("a request could not be evaluated", "error", msg, "requests_are", g.failureAnswer())
	}
}

func (g *Gate) failureAnswer() string {
	if g.opts.FailOpen {
		return "allowed"
	}
	return "refused"
}

// Health reports whether requests are being evaluated. After a failure the
// gate stays "degraded" for a few minutes so the problem is visible.
func (g *Gate) Health() health.Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastFail.IsZero() || g.opts.Now().Sub(g.lastFail) > errorWindow {
		return health.Status{State: health.OK}
	}
	return health.Status{
		State: health.Degraded,
		Detail: fmt.Sprintf("%d requests could not be evaluated and were %s (rules.on_error); last cause: %s",
			g.failures.Load(), g.failureAnswer(), g.lastMsg),
	}
}

// Snapshot is the state of the counters at one moment.
type Snapshot struct {
	// DryRun says whether decisions are only counted and not enforced.
	DryRun bool `json:"dry_run"`
	// Since is when counting started, which is when Xibalba started.
	Since time.Time `json:"since"`
	// Totals holds the number of decisions per action.
	Totals map[rules.Action]uint64 `json:"totals"`
	// Failures is the number of requests that could not be evaluated.
	Failures uint64 `json:"failures"`
	// Challenge says what became of the requests decided as "challenge".
	Challenge ChallengeCounts `json:"challenge"`
	// Sources lists every rule, threshold and the default with its count.
	Sources []SourceCount `json:"sources"`
}

// ChallengeCounts says what became of challenged requests.
type ChallengeCounts struct {
	// Served is how often the challenge page was shown.
	Served uint64 `json:"served"`
	// Passed is how many requests were let through on a valid pass.
	Passed uint64 `json:"passed"`
	// Solved is how many answers to a challenge were accepted.
	Solved uint64 `json:"solved"`
	// Failed is how many answers were rejected.
	Failed uint64 `json:"failed"`
}

// SourceCount says how often one source decided.
type SourceCount struct {
	Source    string       `json:"source"`
	Action    rules.Action `json:"action"`
	Reference string       `json:"reference"`
	Count     uint64       `json:"count"`
}

// Snapshot returns the current counters.
func (g *Gate) Snapshot() Snapshot {
	set := g.set.Load()
	s := Snapshot{
		DryRun:   g.opts.DryRun,
		Since:    g.since.UTC().Truncate(time.Second),
		Totals:   map[rules.Action]uint64{rules.Allow: 0, rules.Challenge: 0, rules.Deny: 0},
		Failures: g.failures.Load(),
		Sources:  make([]SourceCount, len(set.sources)),
		Challenge: ChallengeCounts{
			Served: g.challengesServed.Load(),
			Passed: g.challengesPassed.Load(),
		},
	}
	if g.opts.Challenge != nil {
		s.Challenge.Solved, s.Challenge.Failed = g.opts.Challenge.Counts()
	}
	for i, src := range set.sources {
		n := set.counts[i].Load()
		s.Totals[src.Action] += n
		s.Sources[i] = SourceCount{Source: src.ID, Action: src.Action, Reference: src.Reference, Count: n}
	}
	return s
}

// Handler serves the counters as JSON for the operations listener.
func (g *Gate) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(g.Snapshot())
	})
}
