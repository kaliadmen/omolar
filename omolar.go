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
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/kaliadmen/omolar/config"
)

// Version is the framework version, reported by /health.
const Version = "0.1.0-dev"

var errNotImplemented = errors.New("not implemented")

// App is an Omolar application.
type App struct {
	Config config.Config
	Logger *slog.Logger
}

// Option customises New.
type Option func(*settings)

// settings holds what Options can change.
type settings struct {
	logOutput io.Writer
}

// WithLogOutput sends logs somewhere other than standard output.
func WithLogOutput(w io.Writer) Option {
	return func(s *settings) {}
}

// New creates an App from a validated Config.
func New(cfg config.Config, opts ...Option) (*App, error) {
	return nil, errNotImplemented
}

// Handle registers a handler for a pattern such as "GET /users/{id}".
func (a *App) Handle(pattern string, h http.Handler) {}

// HandleFunc registers a function as a handler.
func (a *App) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request)) {}

// Handler returns the app's root http.Handler.
func (a *App) Handler() http.Handler {
	return http.NotFoundHandler()
}

// Run listens on the configured address until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	return errNotImplemented
}

// Serve is like Run but uses an existing listener (handy in tests).
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	return errNotImplemented
}
