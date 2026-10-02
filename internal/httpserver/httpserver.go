// Package httpserver wraps net/http's server as a lifecycle component with
// safe defaults: timeouts on every phase, a size limit on headers, and
// recovery from panics in handlers.
//
// Every HTTP listener in Xibalba (operations, and later the public proxy and
// the admin interface) is an instance of Server, so they all behave the same
// way when something goes wrong.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

// Options configures a Server.
type Options struct {
	// Name identifies this listener in logs, errors and the health report.
	Name string
	// Addr is the host:port to listen on. Port 0 picks a free port.
	Addr string
	// Handler serves the requests.
	Handler http.Handler
	// Log receives the server's messages.
	Log *slog.Logger
	// OnFailure is called if the server stops serving without being asked to.
	OnFailure func(error)

	// The timeouts below default to values suited to small internal
	// endpoints when left at zero. Set one to NoTimeout to switch it off.

	// ReadHeaderTimeout bounds how long a client may take to send its headers.
	// Default 5s. It is the main defence against slow-header attacks and
	// cannot be switched off.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading the whole request including the body. Default 30s.
	ReadTimeout time.Duration
	// WriteTimeout bounds writing the whole response. Default 30s. A listener
	// that serves downloads, streams or long-lived responses needs NoTimeout.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long a keep-alive connection may sit unused. Default 90s.
	IdleTimeout time.Duration
}

// NoTimeout switches a timeout off.
const NoTimeout time.Duration = -1

// timeout returns the effective value of a timeout option.
func timeout(value, fallback time.Duration) time.Duration {
	switch {
	case value == 0:
		return fallback
	case value < 0:
		return 0 // net/http: zero means no timeout
	default:
		return value
	}
}

// Server is one HTTP listener.
type Server struct {
	opts Options
	srv  *http.Server

	mu     sync.RWMutex
	ln     net.Listener
	failed error
}

// New returns a Server that is not yet listening.
func New(opts Options) *Server {
	log := opts.Log.With("component", opts.Name)
	readHeader := opts.ReadHeaderTimeout
	if readHeader <= 0 {
		readHeader = 5 * time.Second
	}
	return &Server{
		opts: opts,
		srv: &http.Server{
			Handler:           Recover(log, opts.Handler),
			ReadHeaderTimeout: readHeader,
			ReadTimeout:       timeout(opts.ReadTimeout, 30*time.Second),
			WriteTimeout:      timeout(opts.WriteTimeout, 30*time.Second),
			IdleTimeout:       timeout(opts.IdleTimeout, 90*time.Second),
			MaxHeaderBytes:    64 << 10,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
	}
}

// Name implements lifecycle.Component.
func (s *Server) Name() string { return s.opts.Name }

// Start binds the address and begins serving in the background. Binding
// happens here, synchronously, so "address already in use" is a start-up
// error and not something discovered later.
func (s *Server) Start(context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.opts.Addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	go func() {
		err := s.srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		s.mu.Lock()
		s.failed = err
		s.mu.Unlock()
		if s.opts.OnFailure != nil {
			s.opts.OnFailure(err)
		}
	}()
	return nil
}

// Stop stops accepting connections and waits for running requests to finish,
// up to the deadline of ctx.
func (s *Server) Stop(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	s.mu.Lock()
	s.ln = nil
	s.mu.Unlock()
	return err
}

// Addr returns the address the server is listening on, or "" if it is not.
// With port 0 in the options this is how callers learn the real port.
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Health reports whether the server is listening.
func (s *Server) Health() health.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch {
	case s.failed != nil:
		return health.Status{State: health.Down, Detail: "stopped serving: " + s.failed.Error()}
	case s.ln == nil:
		return health.Status{State: health.Down, Detail: "not listening"}
	default:
		return health.Status{State: health.OK}
	}
}

// Recover wraps a handler so that a panic while serving one request fails only
// that request. The panic is logged with its stack; the client gets a plain
// 500 with no internal detail.
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if p == http.ErrAbortHandler { // deliberate abort, not a bug
				panic(p)
			}
			log.Error("panic while serving request",
				"panic", fmt.Sprint(p),
				"method", r.Method,
				"path", r.URL.Path,
				"stack", string(debug.Stack()),
			)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}
