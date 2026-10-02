// Package logging builds the program's structured logger from the configuration.
//
// Xibalba logs with log/slog. Every component adds a "component" attribute to
// its logger, so each line says which part of the program wrote it.
package logging

import (
	"io"
	"log/slog"

	"github.com/MaMoja/xibalba/internal/config"
)

// New returns a logger that writes to w in the configured format and level.
// The configuration has already been validated, so unknown values cannot occur;
// they fall back to info and JSON rather than failing.
func New(cfg config.Log, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level(cfg.Level)}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func level(name string) slog.Level {
	switch name {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
