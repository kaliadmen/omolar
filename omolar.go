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
	Config config.Config
	Logger *slog.Logger

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
		return nil, err
	}

	app := &App{
		Config: cfg,
		Logger: logger,
		mux:    http.NewServeMux(),
	}

	// Built-in routes every app gets.
	app.mux.Handle("/health", server.Health(Version))

	return app, nil
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
	host := a.Config.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(a.Config.Port))
	return server.Probe(ctx, "http://"+addr+"/health")
}

// server builds a server.Server from the app's config.
func (a *App) server() (*server.Server, error) {
	return server.New(a.Handler(), server.Options{
		Addr:            a.Config.Addr(),
		ReadTimeout:     a.Config.ReadTimeout,
		WriteTimeout:    a.Config.WriteTimeout,
		IdleTimeout:     a.Config.IdleTimeout,
		ShutdownTimeout: a.Config.ShutdownTimeout,
		Logger:          a.Logger,
	})
}
