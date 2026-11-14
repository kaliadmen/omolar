// Package logging builds the framework's structured logger.
//
// "Structured" means each log line is a message plus key=value pairs
// (app=omolar addr=:4000) instead of free text. That makes logs searchable
// and machine-readable, which matters a lot once you ship logs to a SIEM.
//
// We use log/slog from the standard library (Go 1.21+), so there's no
// third-party logging dependency.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Options controls how the logger formats and filters output.
type Options struct {
	Level   string // debug, info, warn or error
	Format  string // text (human-friendly) or json (machine-friendly)
	AppName string // added to every line as app=<name>; omitted if empty
}

// New returns a logger that writes to w.
func New(w io.Writer, opts Options) (*slog.Logger, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	hopts := &slog.HandlerOptions{Level: level}

	// GO CONCEPT: interfaces. slog.Handler is an interface: "anything that can
	// handle a log record". TextHandler and JSONHandler are two different
	// types that both satisfy it, so `h` can hold either one.
	var h slog.Handler
	switch strings.ToLower(opts.Format) {
	case "", "text":
		h = slog.NewTextHandler(w, hopts)
	case "json":
		h = slog.NewJSONHandler(w, hopts)
	default:
		return nil, fmt.Errorf("logging: unknown format %q (use text or json)", opts.Format)
	}

	logger := slog.New(h)
	if opts.AppName != "" {
		// With returns a new logger that adds these attributes to every line.
		logger = logger.With("app", opts.AppName)
	}
	return logger, nil
}

// ParseLevel converts a level name (case-insensitive) into an slog.Level.
// An empty string means info.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: unknown level %q (use debug, info, warn or error)", s)
	}
}

// TRY: in logging_test.go, change TestNew_JSONFormat to log a nested group:
//   log.Info("login", slog.Group("user", "id", 7, "role", "admin"))
// and print buf.String(). Notice how JSON nests it as {"user":{"id":7,...}}.
