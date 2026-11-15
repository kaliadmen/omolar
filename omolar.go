// Package omolar is a security-minded web framework for Go.
//
// An App bundles the pieces every web app needs: configuration, a structured
// logger, a router, and a server that shuts down gracefully.
//
//	cfg, _ := config.Load(".env")
//	app, _ := omolar.New(cfg)
//	app.HandleFunc("GET /{$}", home)
//	app.Run(ctx)
package omolar

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/kaliadmen/omolar/config"
	"github.com/kaliadmen/omolar/logging"
	"github.com/kaliadmen/omolar/server"
)

// Version is the framework version, reported by /health.
const Version = "0.1.0-dev"

// App is an Omolar application.
//
// PATTERN: Facade. App gives one simple front door (New, HandleFunc, Run) to
// several packages: config, logging, server and routing. Without it, every
// app's main() would wire those together by hand and get the details wrong
// differently each time (forgotten timeouts, logger not passed to the server).
type App struct {
	// These are unexported so nothing outside can change them after New has
	// validated them. Config() and Logger() hand out read-only views.
	cfg    config.Config
	logger *slog.Logger

	// mux routes requests to handlers. For Milestone 1 this is the standard
	// library's http.ServeMux (which understands "GET /users/{id}" patterns
	// since Go 1.22). Milestone 2 replaces it with our own trie router.
	mux *http.ServeMux
}

// Option customises New.
//
// GO CONCEPT: the "functional options" pattern. Instead of a constructor with
// ten parameters, New takes any number of Option functions. Each one tweaks
// the settings. Callers only mention what they want to change:
//
//	omolar.New(cfg)                               // defaults
//	omolar.New(cfg, omolar.WithLogOutput(file))   // one change
//
// Adding a new option later never breaks existing callers.
//
// PATTERN: Functional options. Problem: constructors that need optional
// settings. Without it you get New(cfg, nil, nil, 0, "") style calls, or a
// new constructor (NewWithLogger, NewWithLoggerAndX...) for every combination.
type Option func(*settings)

// settings holds what Options can change.
type settings struct {
	logOutput io.Writer
}

// WithLogOutput sends logs somewhere other than standard output.
func WithLogOutput(w io.Writer) Option {
	// This returned function is a closure: it remembers w.
	return func(s *settings) { s.logOutput = w }
}

// New creates an App from a validated Config.
func New(cfg config.Config, opts ...Option) (*App, error) {
	// Every app goes through here, so this is where a Config built by hand
	// (instead of loaded with config.Load) gets the same checks.
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("omolar: invalid config:\n%w", err)
	}

	st := settings{logOutput: os.Stdout}
	for _, opt := range opts {
		opt(&st)
	}

	logger, err := logging.New(st.logOutput, logging.Options{
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
		AppName: cfg.AppName,
	})
	if err != nil {
		// Can't happen with a validated Config today (Validate checks the
		// level and format). Kept so a future logging option can't fail
		// silently. This is why this function isn't at 100% coverage.
		return nil, err
	}

	app := &App{
		cfg:    cfg,
		logger: logger,
		mux:    http.NewServeMux(),
	}

	// Built-in routes every app gets.
	app.mux.Handle("/health", server.Health(Version))

	return app, nil
}

// Config returns a copy of the app's configuration. Changing the copy has no
// effect on the app: settings are fixed once New has validated them.
//
// GO CONCEPT: Go copies structs on assignment and when returning them. Because
// every field of config.Config is a plain value (string, int, Duration), this
// copy shares nothing with the original. If Config ever gains a slice or map
// field, this method must clone it, since copying a struct only copies the
// slice/map header and both would point at the same data.
func (a *App) Config() config.Config {
	return a.cfg
}

// Logger returns the app's structured logger. *slog.Logger is safe to share:
// its methods never change it (With returns a new logger instead).
func (a *App) Logger() *slog.Logger {
	return a.logger
}

// Handle registers a handler for a pattern such as "GET /users/{id}".
func (a *App) Handle(pattern string, h http.Handler) {
	a.mux.Handle(pattern, h)
}

// HandleFunc registers a function as a handler.
func (a *App) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request)) {
	a.mux.HandleFunc(pattern, fn)
}

// Handler returns the app's root http.Handler.
//
// Today it's just the router. In Milestone 3 this is where the middleware
// chain (security headers, request logging, panic recovery) wraps it.
func (a *App) Handler() http.Handler {
	return a.mux
}

// Run listens on the configured address until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	srv, err := a.server()
	if err != nil {
		return err
	}
	return srv.ListenAndServe(ctx)
}

// Serve is like Run but uses an existing listener (handy in tests).
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv, err := a.server()
	if err != nil {
		return err
	}
	return srv.Serve(ctx, ln)
}

// Healthcheck probes this app's own /health endpoint on the configured port.
// Used by `<binary> healthcheck` inside Docker; see examples/hello.
func (a *App) Healthcheck(ctx context.Context) error {
	// The app listens on all interfaces when Host is empty (the Docker
	// default), so the probe talks to it over loopback.
	host := a.cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(a.cfg.Port))
	return server.Probe(ctx, "http://"+addr+"/health")
}

// server builds a server.Server from the app's config. Its error can't
// happen with a validated Config (timeouts are positive, the handler isn't
// nil), but it's still checked rather than ignored.
func (a *App) server() (*server.Server, error) {
	return server.New(a.Handler(), server.Options{
		Addr:            a.cfg.Addr(),
		ReadTimeout:     a.cfg.ReadTimeout,
		WriteTimeout:    a.cfg.WriteTimeout,
		IdleTimeout:     a.cfg.IdleTimeout,
		ShutdownTimeout: a.cfg.ShutdownTimeout,
		Logger:          a.logger,
	})
}
