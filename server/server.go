// Package server runs an http.Server with safe timeouts and graceful shutdown.
//
// Graceful shutdown, in plain words: when it's time to stop (Ctrl+C, a deploy,
// `docker stop`), the server stops accepting NEW connections but lets requests
// that are already running finish, up to a time limit. Without this, a deploy
// can cut a user off halfway through saving a form.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Default timeouts used when an Options field is zero. A zero timeout in
// net/http means "wait forever", which lets slow or malicious clients hold
// connections open indefinitely, so we never leave them at zero.
const (
	DefaultReadTimeout       = 15 * time.Second
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
	DefaultShutdownTimeout   = 10 * time.Second
)

// Options configures a Server. Zero values fall back to safe defaults.
type Options struct {
	Addr            string        // e.g. ":4000"
	ReadTimeout     time.Duration // max time to read a whole request
	WriteTimeout    time.Duration // max time to write a response
	IdleTimeout     time.Duration // max time a keep-alive connection sits idle
	ShutdownTimeout time.Duration // how long in-flight requests get to finish
	Logger          *slog.Logger  // where to log start/stop; nil discards logs
}

// ErrServerUsed is returned by Serve when the Server has already been started.
// A Server runs once; create a new one to serve again.
var ErrServerUsed = errors.New("server: Serve already called; create a new Server")

// Server wraps http.Server.
type Server struct {
	srv   *http.Server
	opts  Options
	ready chan struct{}
}

// New creates a Server that sends every request to handler.
//
// PATTERN: Constructor function with safe defaults. Go has no constructors, so
// packages export New(...). Doing all setup here means nobody can create a
// Server that forgot its timeouts. Without it, callers would build
// http.Server{} themselves, and the zero value has NO timeouts.
func New(handler http.Handler, opts Options) (*Server, error) {
	opts.ReadTimeout = orDefault(opts.ReadTimeout, DefaultReadTimeout)
	opts.WriteTimeout = orDefault(opts.WriteTimeout, DefaultWriteTimeout)
	opts.IdleTimeout = orDefault(opts.IdleTimeout, DefaultIdleTimeout)
	opts.ShutdownTimeout = orDefault(opts.ShutdownTimeout, DefaultShutdownTimeout)
	if opts.Logger == nil {
		// A logger that throws everything away, so the rest of the code can
		// always call s.opts.Logger.Info without checking for nil.
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	srv := &http.Server{
		Addr:        opts.Addr,
		Handler:     handler,
		ReadTimeout: opts.ReadTimeout,
		// Security: limits how long a client may take to send headers.
		// Defends against "Slowloris", where an attacker opens many
		// connections and trickles headers to exhaust the server.
		ReadHeaderTimeout: min(DefaultReadHeaderTimeout, opts.ReadTimeout),
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		// Route net/http's internal errors (bad TLS handshakes etc.) through
		// our structured logger instead of printing to stderr.
		ErrorLog: slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
	}
	return &Server{srv: srv, opts: opts, ready: make(chan struct{})}, nil
}

// Ready returns a channel that is closed once the server is accepting
// connections. It is never closed if Serve returns before that.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// Addr returns the address the server is listening on, or nil if it isn't
// listening yet. Useful with Addr ":0", where the OS picks the port.
func (s *Server) Addr() net.Addr {
	return nil
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down
// gracefully. It returns nil after a clean shutdown.
//
// GO CONCEPT: context.Context. A context carries a "please stop" signal (and
// optionally a deadline) through a program. Whoever creates it can cancel
// it; everyone holding it can notice via ctx.Done(). Here the caller cancels
// the context to tell the server it's time to shut down.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	log := s.opts.Logger
	addr := ln.Addr().String()

	// Run the blocking Serve call in its own goroutine so this function can
	// watch for cancellation at the same time.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.srv.Serve(ln)
	}()
	log.Info("server started", "addr", addr)

	select {
	case err := <-serveErr:
		// The server stopped on its own, e.g. the listener broke. That's
		// always an error, since we didn't ask it to stop.
		return fmt.Errorf("server: %w", err)

	case <-ctx.Done():
		// Time to shut down. Give in-flight requests ShutdownTimeout to finish.
		log.Info("server stopping", "timeout", s.opts.ShutdownTimeout.String())

		// We need a FRESH context for the timeout: ctx is already cancelled,
		// so passing it to Shutdown would mean "give up immediately".
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
		defer cancel()

		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			// Shutdown timed out with requests still running. Close forces
			// the remaining connections shut.
			s.srv.Close()
			log.Warn("server forced to stop", "error", err)
			return fmt.Errorf("server: shutdown: %w", err)
		}

		// After a successful Shutdown, srv.Serve returns http.ErrServerClosed.
		// That's the expected, happy outcome, so it isn't reported as an error.
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server: %w", err)
		}
		log.Info("server stopped", "addr", addr)
		return nil
	}
}

// ListenAndServe listens on opts.Addr and then behaves like Serve.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("server: listen on %q: %w", s.opts.Addr, err)
	}
	return s.Serve(ctx, ln)
}

// orDefault returns v, or def when v is zero or negative.
func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

// TRY: in server_test.go, TestServe_GracefulShutdownLetsInFlightRequestsFinish,
// change ShutdownTimeout from 5*time.Second to 50*time.Millisecond and run it.
// The handler sleeps 300ms, so shutdown now gives up first. Read the error.
