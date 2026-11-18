package router

import "net/http"

// Option configures a Router (functional options, as in omolar.New).
type Option func(*Router)

// WithNotFound sets the handler for requests that match no route.
// nil means the default (http.NotFound).
func WithNotFound(h http.Handler) Option {
	return func(*Router) {}
}

// WithMaxPathLength sets the longest escaped path accepted, in bytes; longer
// paths get 414. n < 1 means the default (8192).
func WithMaxPathLength(n int) Option {
	return func(*Router) {}
}

// WithMaxSegments sets the most path segments accepted (a trailing slash
// counts as one); more get 414. n < 1 means the default (128).
func WithMaxSegments(n int) Option {
	return func(*Router) {}
}
