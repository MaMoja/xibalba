// Package lifecycle starts and stops the parts of Xibalba in a fixed order and
// attributes every failure to the part that caused it.
//
// Each part of the program (a listener, the statistics store, a background
// refresher) is a Component. The Supervisor starts components in the order
// they were added and stops them in reverse. If one fails to start, the ones
// already running are stopped again and the error names the failing component.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Component is one independently startable part of the program.
type Component interface {
	// Name identifies the component in logs, errors and the health report.
	Name() string
	// Start brings the component up and returns promptly. Long-running work
	// belongs in a goroutine that ends when Stop is called.
	Start(ctx context.Context) error
	// Stop shuts the component down, waiting no longer than ctx allows.
	Stop(ctx context.Context) error
}

// Failure is an error that a running component reported after it had started.
type Failure struct {
	Component string
	Err       error
}

func (f Failure) Error() string { return fmt.Sprintf("component %q failed: %v", f.Component, f.Err) }
func (f Failure) Unwrap() error { return f.Err }

// Supervisor owns the components. It is not safe for concurrent use; call Add,
// Start and Stop from one goroutine. Reporter functions may be called from any goroutine.
type Supervisor struct {
	log        *slog.Logger
	components []Component
	started    []Component
	failures   chan Failure
}

// New returns a Supervisor that logs to log.
func New(log *slog.Logger) *Supervisor {
	return &Supervisor{log: log, failures: make(chan Failure, 16)}
}

// Add registers a component. Order matters: components start in this order.
func (s *Supervisor) Add(c Component) { s.components = append(s.components, c) }

// Reporter returns the function a component calls when it fails while running.
// Reporting never blocks; if many failures arrive at once the first ones win.
func (s *Supervisor) Reporter(name string) func(error) {
	return func(err error) {
		select {
		case s.failures <- Failure{Component: name, Err: err}:
		default:
		}
	}
}

// Failures delivers failures reported by running components.
func (s *Supervisor) Failures() <-chan Failure { return s.failures }

// Start starts all components in order. If one fails, the components already
// started are stopped in reverse order and the error names the one that failed.
func (s *Supervisor) Start(ctx context.Context) error {
	for _, c := range s.components {
		if err := safely(func() error { return c.Start(ctx) }); err != nil {
			startErr := fmt.Errorf("component %q could not start: %w", c.Name(), err)
			if stopErr := s.Stop(ctx); stopErr != nil {
				return errors.Join(startErr, stopErr)
			}
			return startErr
		}
		s.started = append(s.started, c)
		s.log.Info("component started", "component", c.Name())
	}
	return nil
}

// Stop stops all started components in reverse order. It always tries every
// component and returns all errors, each naming its component.
func (s *Supervisor) Stop(ctx context.Context) error {
	var errs []error
	for i := len(s.started) - 1; i >= 0; i-- {
		c := s.started[i]
		if err := safely(func() error { return c.Stop(ctx) }); err != nil {
			errs = append(errs, fmt.Errorf("component %q did not stop cleanly: %w", c.Name(), err))
			continue
		}
		s.log.Info("component stopped", "component", c.Name())
	}
	s.started = nil
	return errors.Join(errs...)
}

// safely turns a panic into an error so one broken component cannot take the
// whole start-up or shutdown sequence down with it.
func safely(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return fn()
}
