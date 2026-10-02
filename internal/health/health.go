// Package health collects the state of every component and reports it in one place.
//
// Each component registers a check under its own name. The report lists every
// component separately, so when something breaks the report says which part
// broke and why, instead of a single red light for the whole program.
package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// State is how a component is doing.
type State string

const (
	// OK means the component works.
	OK State = "ok"
	// Degraded means the component works with reduced function, for example
	// on stale data. Xibalba keeps serving.
	Degraded State = "degraded"
	// Down means the component does not work.
	Down State = "down"
)

// rank orders states from best to worst.
func (s State) rank() int {
	switch s {
	case OK:
		return 0
	case Degraded:
		return 1
	default:
		return 2
	}
}

// Status is the state of one component with an optional explanation.
type Status struct {
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// Check returns the current status of a component. It must be fast and must
// not block: it is called on every health request.
type Check func() Status

// Report is the state of the whole program.
type Report struct {
	// State is the worst state of any component.
	State State `json:"state"`
	// Components holds the status of each component by name.
	Components map[string]Status `json:"components"`
}

// Registry holds the checks of all components. It is safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	checks map[string]Check
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{checks: map[string]Check{}}
}

// Register adds the check of a component. Registering the same name twice is a
// programming error and panics at start-up, where it is cheap to find.
func (r *Registry) Register(name string, check Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.checks[name]; exists {
		panic(fmt.Sprintf("health: component %q registered twice", name))
	}
	r.checks[name] = check
}

// Report runs every check and returns the result. A check that panics marks
// only its own component as down; the other checks still run.
func (r *Registry) Report() Report {
	r.mu.RLock()
	defer r.mu.RUnlock()

	report := Report{State: OK, Components: make(map[string]Status, len(r.checks))}
	for name, check := range r.checks {
		status := runCheck(check)
		report.Components[name] = status
		if status.State.rank() > report.State.rank() {
			report.State = status.State
		}
	}
	return report
}

func runCheck(check Check) (status Status) {
	defer func() {
		if p := recover(); p != nil {
			status = Status{State: Down, Detail: fmt.Sprintf("health check panicked: %v", p)}
		}
	}()
	status = check()
	if status.State != OK && status.State != Degraded && status.State != Down {
		return Status{State: Down, Detail: fmt.Sprintf("health check returned unknown state %q", status.State)}
	}
	return status
}

// Handler serves the report as JSON. It answers 200 while Xibalba can serve
// (ok or degraded) and 503 when a component is down, so load balancers and
// container runtimes can act on the status code alone.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		report := r.Report()
		code := http.StatusOK
		if report.State == Down {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	})
}
