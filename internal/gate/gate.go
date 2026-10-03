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
	// Passed reports whether the request carries a valid pass.
	Passed(r *http.Request) bool
	// Serve answers the request with the challenge page.
	Serve(w http.ResponseWriter, r *http.Request)
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
	// Trapped says whether a client recently followed the hidden trap
	// link. If nil, trapped conditions see "no".
	Trapped func(client netip.Addr) bool
	// Limit counts a request from a client and reports whether the client
	// is over a request limit and, if so, whether further requests are
	// refused (deny) or have to pass the check. If nil, nothing is limited.
	// Requests that a rule explicitly allows are neither counted nor limited.
	Limit func(client netip.Addr) (over, deny bool, retryAfter time.Duration)
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
	opts    Options
	log     *slog.Logger
	sources []rules.Source
	counts  []atomic.Uint64 // one per source, same order
	trusted []bool          // per source: a rule that allows
	since   time.Time

	challengesServed atomic.Uint64 // challenge pages shown
	challengesPassed atomic.Uint64 // requests let through on a valid pass

	failures atomic.Uint64
	mu       sync.Mutex
	lastFail time.Time
	lastMsg  string
}

// New returns a Gate for opts.
func New(opts Options) *Gate {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	sources := opts.Engine.Sources()
	trusted := make([]bool, len(sources))
	for i, s := range sources {
		trusted[i] = s.Action == rules.Allow && strings.HasPrefix(s.ID, "rule:")
	}
	return &Gate{
		trusted: trusted,
		opts:    opts,
		log:     opts.Log.With("component", "rules"),
		sources: sources,
		counts:  make([]atomic.Uint64, len(sources)),
		since:   opts.Now(),
	}
}

// ServeHTTP decides what happens to the request and carries it out.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	decision, client, ok := g.decide(r)
	if !ok {
		if g.opts.FailOpen {
			g.opts.Next.ServeHTTP(w, r)
		} else {
			g.opts.Unavailable(w, r, http.StatusServiceUnavailable)
		}
		return
	}

	g.counts[decision.Source].Add(1)
	if g.log.Enabled(r.Context(), slog.LevelDebug) {
		g.log.Debug("decision",
			"action", string(decision.Action),
			"source", g.sources[decision.Source].ID,
			"weight", decision.Weight,
			"enforced", !g.opts.DryRun,
		)
	}

	// Request limits. A request that a rule of the site owner explicitly
	// allows is trusted and not counted; so is nothing else. A limit can
	// only make the outcome stricter.
	var over, refuse bool
	var retryAfter time.Duration
	if g.opts.Limit != nil && !g.trusted[decision.Source] {
		over, refuse, retryAfter = g.opts.Limit(client)
	}

	if g.opts.DryRun {
		g.opts.Next.ServeHTTP(w, r)
		return
	}
	action := decision.Action
	if over && action != rules.Deny {
		if refuse {
			g.opts.Limited(w, r, retryAfter)
			return
		}
		action = rules.Challenge
	}
	switch action {
	case rules.Deny:
		g.opts.Blocked(w, r, g.sources[decision.Source].Reference)
	case rules.Challenge:
		switch {
		case g.opts.Challenge == nil:
			g.opts.Next.ServeHTTP(w, r)
		case g.opts.Challenge.Passed(r):
			g.challengesPassed.Add(1)
			g.opts.Next.ServeHTTP(w, r)
		default:
			g.challengesServed.Add(1)
			g.opts.Challenge.Serve(w, r)
		}
	default:
		g.opts.Next.ServeHTTP(w, r)
	}
}

// decide evaluates the request. The engine is built so that it cannot fail,
// but this stage stands in front of someone's website: if it fails anyway,
// the failure is contained here and the configured answer applies.
func (g *Gate) decide(r *http.Request) (decision rules.Decision, client netip.Addr, ok bool) {
	defer func() {
		if p := recover(); p != nil {
			g.recordFailure(fmt.Sprint(p))
			ok = false
		}
	}()

	info, _ := clientip.FromContext(r.Context())
	req := rules.Request{
		Method:    strings.ToUpper(r.Method),
		Host:      rules.NormalizeHost(r.Host),
		Path:      rules.NormalizePath(r.URL.Path),
		UserAgent: r.Header.Get("User-Agent"),
		Header:    r.Header,
		Client:    info.Client,
	}
	if g.opts.Trapped != nil {
		req.Trapped = g.opts.Trapped(req.Client)
	}
	if g.opts.Identify != nil {
		req.Crawler = g.opts.Identify(req.UserAgent, req.Client)
	}
	return g.opts.Engine.Evaluate(&req), req.Client, true
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
	s := Snapshot{
		DryRun:   g.opts.DryRun,
		Since:    g.since.UTC().Truncate(time.Second),
		Totals:   map[rules.Action]uint64{rules.Allow: 0, rules.Challenge: 0, rules.Deny: 0},
		Failures: g.failures.Load(),
		Sources:  make([]SourceCount, len(g.sources)),
		Challenge: ChallengeCounts{
			Served: g.challengesServed.Load(),
			Passed: g.challengesPassed.Load(),
		},
	}
	if g.opts.Challenge != nil {
		s.Challenge.Solved, s.Challenge.Failed = g.opts.Challenge.Counts()
	}
	for i, src := range g.sources {
		n := g.counts[i].Load()
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
