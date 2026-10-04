package main

import (
	"time"

	"github.com/MaMoja/xibalba/internal/buildinfo"
	"github.com/MaMoja/xibalba/internal/crawlers"
	"github.com/MaMoja/xibalba/internal/gate"
	"github.com/MaMoja/xibalba/internal/health"
	"github.com/MaMoja/xibalba/internal/limit"
	"github.com/MaMoja/xibalba/internal/metrics"
	"github.com/MaMoja/xibalba/internal/trap"
)

// parts are the sources of the numbers served at /metrics. limiter and
// snare are nil when the feature is off.
type parts struct {
	started   time.Time
	health    *health.Registry
	decisions *gate.Gate
	crawlers  *crawlers.Registry
	limiter   *limit.Limiter
	snare     *trap.Trap
}

// collect hands every part's numbers to the metrics registry. The numbers
// are the ones the JSON endpoints show; nothing here holds an address.
func collect(m *metrics.Registry, p parts) {
	m.Add(func(w *metrics.Writer) {
		w.Gauge("xibalba_build_info", "The running build; the value is always 1.", 1, "version", buildinfo.Get().Version)
		w.Gauge("xibalba_start_time_seconds", "When Xibalba started, as seconds since 1970.", float64(p.started.Unix()))

		report := p.health.Report()
		for _, name := range sortedKeys(report.Components) {
			state := 2.0 // down, also for a state not known here
			switch report.Components[name].State {
			case health.OK:
				state = 0
			case health.Degraded:
				state = 1
			}
			w.Gauge("xibalba_component_state", "State of each part: 0 ok, 1 degraded, 2 down.", state, "component", name)
		}
	})

	m.Add(func(w *metrics.Writer) {
		s := p.decisions.Snapshot()
		for _, src := range s.Sources {
			w.Counter("xibalba_decisions_total", "Decisions by what decided (rule, threshold or default) and the action taken.",
				float64(src.Count), "source", src.Source, "action", string(src.Action))
		}
		w.Counter("xibalba_evaluation_failures_total", "Requests that could not be evaluated.", float64(s.Failures))
		for _, c := range []struct {
			result string
			n      uint64
		}{{"served", s.Challenge.Served}, {"passed", s.Challenge.Passed}, {"solved", s.Challenge.Solved}, {"failed", s.Challenge.Failed}} {
			w.Counter("xibalba_challenge_total",
				"The security check: pages served, requests let through on a pass, answers solved and failed.", float64(c.n), "result", c.result)
		}
		dry := 0.0
		if s.DryRun {
			dry = 1
		}
		w.Gauge("xibalba_dry_run", "1 if decisions are only counted and not enforced.", dry)
	})

	m.Add(func(w *metrics.Writer) {
		for _, c := range p.crawlers.Reports() {
			for _, s := range []struct {
				status string
				n      uint64
			}{{"verified", c.Requests.Verified}, {"impostor", c.Requests.Unverified}, {"unverifiable", c.Requests.Unverifiable}, {"pending", c.Requests.Pending}} {
				w.Counter("xibalba_crawler_requests_total", "Requests that carried a known crawler's name, by what was found out about them.",
					float64(s.n), "crawler", c.Name, "class", string(c.Class), "status", s.status)
			}
		}
	})

	if p.limiter != nil {
		m.Add(func(w *metrics.Writer) {
			r := p.limiter.Report()
			w.Gauge("xibalba_limit_clients", "Clients whose requests are being counted right now.", float64(r.Clients))
			w.Counter("xibalba_limit_exempt_requests_total", "Requests from addresses on the exempt list.", float64(r.Exempt))
			for _, l := range r.Limits {
				w.Counter("xibalba_limit_over_total", "Requests that were over a limit.", float64(l.Over),
					"per", l.Per, "count", l.Count, "action", l.Action)
			}
		})
	}
	if p.snare != nil {
		m.Add(func(w *metrics.Writer) {
			r := p.snare.Report()
			w.Counter("xibalba_trap_hits_total", "Requests that followed the hidden link.", float64(r.Hits))
			w.Counter("xibalba_trap_ignored_total", "Requests to the trap's addresses that were no catch.", float64(r.Ignored))
			w.Gauge("xibalba_trap_clients", "Clients remembered as caught right now.", float64(r.Clients))
		})
	}
}

func sortedKeys(m map[string]health.Status) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ { // few entries; insertion sort
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
