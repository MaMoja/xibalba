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

// Options configures a Gate.
type Options struct {
	// Engine is the compiled rule set.
	Engine Evaluator
	// DryRun evaluates and counts every decision but passes every request on.
	DryRun bool
	// FailOpen says what happens if evaluating a request fails inside
	// Xibalba: true passes the request on, false refuses it.
	FailOpen bool
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
	since   time.Time

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
	return &Gate{
		opts:    opts,
		log:     opts.Log.With("component", "rules"),
		sources: sources,
		counts:  make([]atomic.Uint64, len(sources)),
		since:   opts.Now(),
	}
}

// ServeHTTP decides what happens to the request and carries it out.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	decision, ok := g.decide(r)
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

	// Until the challenge exists (milestone M3), a request that would be
	// challenged is counted and passed on.
	if g.opts.DryRun || decision.Action != rules.Deny {
		g.opts.Next.ServeHTTP(w, r)
		return
	}
	g.opts.Blocked(w, r, g.sources[decision.Source].Reference)
}

// decide evaluates the request. The engine is built so that it cannot fail,
// but this stage stands in front of someone's website: if it fails anyway,
// the failure is contained here and the configured answer applies.
func (g *Gate) decide(r *http.Request) (decision rules.Decision, ok bool) {
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
	return g.opts.Engine.Evaluate(&req), true
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
	// Sources lists every rule, threshold and the default with its count.
	Sources []SourceCount `json:"sources"`
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
