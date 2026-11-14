// Package server runs an http.Server with safe timeouts and graceful shutdown.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

var errNotImplemented = errors.New("not implemented")

// Options configures a Server. Zero values fall back to safe defaults.
type Options struct {
	Addr            string        // e.g. ":4000"
	ReadTimeout     time.Duration // max time to read a whole request
	WriteTimeout    time.Duration // max time to write a response
	IdleTimeout     time.Duration // max time a keep-alive connection sits idle
	ShutdownTimeout time.Duration // how long in-flight requests get to finish
	Logger          *slog.Logger  // where to log start/stop; nil discards logs
}

// Server wraps http.Server.
type Server struct {
	srv  *http.Server
	opts Options
}

// New creates a Server that sends every request to handler.
func New(handler http.Handler, opts Options) *Server {
	return &Server{srv: &http.Server{}, opts: opts}
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down
// gracefully. It returns nil after a clean shutdown.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	return errNotImplemented
}

// ListenAndServe listens on opts.Addr and then behaves like Serve.
func (s *Server) ListenAndServe(ctx context.Context) error {
	return errNotImplemented
}
