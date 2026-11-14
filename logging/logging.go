// Package logging builds the framework's structured logger.
//
// "Structured" means each log line is a message plus key=value pairs
// (app=omolar addr=:4000) instead of free text. That makes logs searchable
// and machine-readable, which matters a lot once you ship logs to a SIEM.
package logging

import (
	"errors"
	"io"
	"log/slog"
)

var errNotImplemented = errors.New("not implemented")

// Options controls how the logger formats and filters output.
type Options struct {
	Level   string // debug, info, warn or error
	Format  string // text (human-friendly) or json (machine-friendly)
	AppName string // added to every line as app=<name>; omitted if empty
}

// New returns a logger that writes to w.
func New(w io.Writer, opts Options) (*slog.Logger, error) {
	return nil, errNotImplemented
}

// ParseLevel converts a level name (case-insensitive) into an slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	return 0, errNotImplemented
}
