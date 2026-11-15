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
	"sync"
	"sync/atomic"
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

// ErrServerUsed is returned by Serve when the Server has already been started.
// A Server runs once; create a new one to serve again.
var ErrServerUsed = errors.New("server: Serve already called; create a new Server")

// Options configures a Server. A zero timeout means "use the default";
// a negative one is an error.
type Options struct {
	Addr            string        // e.g. ":4000"; ":0" lets the OS pick a free port
	ReadTimeout     time.Duration // max time to read a whole request
	WriteTimeout    time.Duration // max time to write a response
	IdleTimeout     time.Duration // max time a keep-alive connection sits idle
	ShutdownTimeout time.Duration // how long in-flight requests get to finish
	Logger          *slog.Logger  // where to log start/stop; nil discards logs
}

// Server wraps http.Server. Create one with New. A Server serves once.
type Server struct {
	srv  *http.Server
	opts Options

	// GO CONCEPT: sync/atomic. atomic.Bool can be read and changed safely from
	// many goroutines at once without a lock. CompareAndSwap(false, true)
	// means "if it's false, set it to true, and tell me whether I won", as one
	// indivisible step, so two simultaneous Serve calls can't both win.
	started atomic.Bool

	ready chan struct{} // closed once accepting connections

	mu   sync.Mutex // guards addr
	addr net.Addr

	// Every request's context descends from baseCtx. Cancelling it tells
	// context-aware handlers to give up. We only do that when a shutdown has
	// run out of time, never at the start of a graceful shutdown (that would
	// abort the very requests we're trying to let finish).
	baseCtx    context.Context
	cancelBase context.CancelFunc
}

// New creates a Server that sends every request to handler.
//
// PATTERN: Constructor function with safe defaults. Go has no constructors, so
// packages export New(...). Doing all setup here means nobody can create a
// Server that forgot its timeouts. Without it, callers would build
// http.Server{} themselves, and the zero value has NO timeouts.
func New(handler http.Handler, opts Options) (*Server, error) {
	// Security: a nil Handler makes http.Server fall back to the global
	// http.DefaultServeMux, which any imported package can register routes
	// on (net/http/pprof does exactly that). Refuse rather than fall back.
	if handler == nil {
		return nil, errors.New("server: handler must not be nil")
	}

	var errs []error
	for _, t := range []struct {
		name string
		d    time.Duration
	}{
		{"ReadTimeout", opts.ReadTimeout},
		{"WriteTimeout", opts.WriteTimeout},
		{"IdleTimeout", opts.IdleTimeout},
		{"ShutdownTimeout", opts.ShutdownTimeout},
	} {
		if t.d < 0 {
			errs = append(errs, fmt.Errorf("server: %s must not be negative, got %s", t.name, t.d))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	opts.ReadTimeout = orDefault(opts.ReadTimeout, DefaultReadTimeout)
	opts.WriteTimeout = orDefault(opts.WriteTimeout, DefaultWriteTimeout)
	opts.IdleTimeout = orDefault(opts.IdleTimeout, DefaultIdleTimeout)
	opts.ShutdownTimeout = orDefault(opts.ShutdownTimeout, DefaultShutdownTimeout)
	if opts.Logger == nil {
		// A logger that throws everything away, so the rest of the code can
		// always call s.opts.Logger.Info without checking for nil.
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	baseCtx, cancelBase := context.WithCancel(context.Background())

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
		ErrorLog:    slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}

	return &Server{
		srv:        srv,
		opts:       opts,
		ready:      make(chan struct{}),
		baseCtx:    baseCtx,
		cancelBase: cancelBase,
	}, nil
}

// Ready returns a channel that is closed once the server is accepting
// connections. It is never closed if Serve returns before that, so wait on it
// together with something else (Serve returning, or a timeout).
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// Addr returns the address the server is listening on, or nil if it isn't
// listening yet. Useful with Addr ":0", where the OS picks the port.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down
// gracefully. Serve takes ownership of ln: it is closed on every return path.
//
// It returns:
//
//	nil                      ctx was cancelled and every request finished in
//	                         time (also when ctx was cancelled before Serve
//	                         started, or when the listener failed after ctx
//	                         was cancelled: cancellation wins)
//	error wrapping
//	  context.DeadlineExceeded  requests were still running after
//	                         ShutdownTimeout; their contexts were cancelled
//	                         and their connections closed
//	error wrapping the
//	  listener's error       the listener failed while ctx was still live
//	                         (open requests are drained first)
//	ErrServerUsed            Serve was already called on this Server
//
// GO CONCEPT: context.Context. A context carries a "please stop" signal (and
// optionally a deadline) through a program. Whoever creates it can cancel
// it; everyone holding it can notice via ctx.Done(). Here the caller cancels
// the context to tell the server it's time to shut down.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if ln == nil {
		return errors.New("server: listener must not be nil")
	}
	if !s.started.CompareAndSwap(false, true) {
		ln.Close()
		return ErrServerUsed
	}
	// However Serve exits, release the request contexts. It's a no-op if they
	// were already cancelled, and it frees the context's resources.
	defer s.cancelBase()

	log := s.opts.Logger

	// Already told to stop? Then don't start at all.
	if ctx.Err() != nil {
		ln.Close()
		log.Info("server not started: context already cancelled")
		return nil
	}

	s.mu.Lock()
	s.addr = ln.Addr()
	s.mu.Unlock()
	addr := ln.Addr().String()

	// Run the blocking Serve call in its own goroutine so this function can
	// watch for cancellation at the same time. It closes ln when it returns.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.srv.Serve(ln)
	}()
	// The listener is already bound, so the OS queues connections from this
	// point on even if the goroutine above hasn't reached Accept yet.
	close(s.ready)
	log.Info("server started", "addr", addr)

	select {
	case err := <-serveErr:
		// The listener stopped. If ctx was cancelled too (the two raced),
		// cancellation wins and this is a normal shutdown.
		if ctx.Err() != nil {
			return s.shutdown(addr)
		}
		// Otherwise it failed on its own. Still drain open requests, so no
		// connection or goroutine is left behind, then report the failure.
		log.Error("listener failed", "addr", addr, "error", err)
		if shutdownErr := s.shutdown(addr); shutdownErr != nil {
			return errors.Join(fmt.Errorf("server: listener: %w", err), shutdownErr)
		}
		return fmt.Errorf("server: listener: %w", err)

	case <-ctx.Done():
		err := s.shutdown(addr)
		// Wait for the Serve goroutine, so it never outlives this call. After
		// Shutdown it returns http.ErrServerClosed, or the listener's error if
		// the listener happened to fail during shutdown. Either way we were
		// stopping anyway, so it doesn't change the result.
		<-serveErr
		return err
	}
}

// shutdown stops accepting, waits up to ShutdownTimeout for active requests,
// and then forces anything left to stop.
func (s *Server) shutdown(addr string) error {
	log := s.opts.Logger
	log.Info("server stopping", "timeout", s.opts.ShutdownTimeout.String())

	// We need a FRESH context for the timeout: the caller's ctx is already
	// cancelled, so passing it to Shutdown would mean "give up immediately".
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
	defer cancel()

	err := s.srv.Shutdown(ctx)
	switch {
	case err == nil:
		// Every request finished in time.

	case errors.Is(err, net.ErrClosed):
		// Shutdown drained every request but reports that the listener was
		// already closed (it failed, or was closed by someone else). The
		// requests still finished, so this is a clean stop.
		// Found by TestServe_CancelThenListenerCloseIsClean under -race.

	case errors.Is(err, context.DeadlineExceeded):
		// Out of time. First tell context-aware handlers to stop, then close
		// every remaining connection.
		s.cancelBase()
		s.srv.Close()
		log.Warn("server forced to stop", "error", err)
		return fmt.Errorf("server: shutdown: %w", err)

	default:
		s.srv.Close()
		return fmt.Errorf("server: shutdown: %w", err)
	}
	log.Info("server stopped", "addr", addr)
	return nil
}

// ListenAndServe listens on opts.Addr and then behaves like Serve. If ctx is
// already cancelled it returns nil without opening a port.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if ctx.Err() != nil {
		s.opts.Logger.Info("server not started: context already cancelled")
		return nil
	}
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("server: listen on %q: %w", s.opts.Addr, err)
	}
	return s.Serve(ctx, ln)
}

// orDefault returns v, or def when v is zero.
func orDefault(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}

// TRY: in server_test.go, TestServe_GracefulShutdownLetsInFlightRequestsFinish,
// change ShutdownTimeout from 5*time.Second to 50*time.Millisecond and run it.
// The handler sleeps 300ms, so shutdown now gives up first. Read the error.
//
// TRY: in Serve, delete the `<-serveErr` line in the ctx.Done() case and run
//   go test -race -count=20 ./server
// Nothing may fail, but now a goroutine can outlive Serve. That's a leak
// tests rarely catch, which is why the line is there.
