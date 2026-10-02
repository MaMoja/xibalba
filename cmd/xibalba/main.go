// Command xibalba is the Xibalba server.
//
// main only wires the parts together: it loads the configuration, builds the
// components, hands them to the supervisor and waits for a signal or a
// failure. All behaviour lives in the internal packages.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/MaMoja/xibalba/internal/buildinfo"
	"github.com/MaMoja/xibalba/internal/config"
	"github.com/MaMoja/xibalba/internal/health"
	"github.com/MaMoja/xibalba/internal/httpserver"
	"github.com/MaMoja/xibalba/internal/lifecycle"
	"github.com/MaMoja/xibalba/internal/logging"
)

// Exit codes.
const (
	exitOK     = 0 // clean shutdown
	exitFailed = 1 // bad configuration, failed start, or a component failed while running
	exitUsage  = 2 // bad command line
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is the whole program. It takes its inputs as arguments so tests can call it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("xibalba", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "xibalba.yaml", "path to the configuration file")
	checkOnly := flags.Bool("check", false, "validate the configuration file and exit")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	if *showVersion {
		_, _ = fmt.Fprintln(stdout, buildinfo.Get())
		return exitOK
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitFailed
	}
	if *checkOnly {
		_, _ = fmt.Fprintf(stdout, "configuration %s is valid\n", *configPath)
		return exitOK
	}

	log := logging.New(cfg.Log, stderr)
	registry := health.NewRegistry()
	supervisor := lifecycle.New(log)

	// Operations listener: health and version. Metrics join it in a later milestone.
	opsMux := http.NewServeMux()
	opsMux.Handle("GET /healthz", registry.Handler())
	opsMux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(buildinfo.Get())
	})
	ops := httpserver.New(httpserver.Options{
		Name:      "ops",
		Addr:      cfg.Ops.Listen,
		Handler:   opsMux,
		Log:       log,
		OnFailure: supervisor.Reporter("ops"),
	})
	registry.Register(ops.Name(), ops.Health)
	supervisor.Add(ops)

	if err := supervisor.Start(ctx); err != nil {
		log.Error("start-up failed", "error", err.Error())
		return exitFailed
	}
	log.Info("xibalba started", "version", buildinfo.Get().Version, "ops", ops.Addr())

	code := exitOK
	select {
	case <-ctx.Done():
		log.Info("shutdown requested")
	case failure := <-supervisor.Failures():
		log.Error("component failed, shutting down", "component", failure.Component, "error", failure.Err.Error())
		code = exitFailed
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := supervisor.Stop(stopCtx); err != nil {
		log.Error("shutdown was not clean", "error", err.Error())
		code = exitFailed
	}
	log.Info("xibalba stopped")
	return code
}
