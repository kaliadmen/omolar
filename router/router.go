package router

import (
	"errors"
	"net/http"
)

var errNotImplemented = errors.New("not implemented")

// Router is an http.Handler that dispatches requests to the most specific
// matching route. Create one with New, register routes with Handle, then
// call Freeze (omolar.App does this at startup). A router that was never
// frozen freezes itself on its first request.
type Router struct{}

// Route describes one registered route.
type Route struct {
	Method  string // "" for a method-less route
	Pattern string // as registered
}

// Match describes the route a request would reach.
type Match struct {
	Pattern string
	Params  []Param // {name} wildcards in pattern order, then a {name...} if any
}

// Param is one path parameter.
type Param struct{ Name, Value string }

// New creates an empty router.
func New(opts ...Option) *Router {
	return &Router{}
}

// Handle registers h for pattern, e.g. "GET /users/{id}". This is the checked
// registration API: it returns a *PatternError for a malformed pattern, a
// *ConflictError if the pattern is ambiguous with an existing route, or
// ErrFrozen after Freeze. A failed Handle leaves the router unchanged.
func (rt *Router) Handle(pattern string, h http.Handler) error {
	return errNotImplemented
}

// HandleFunc registers fn as the handler for pattern. See Handle.
func (rt *Router) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request)) error {
	return errNotImplemented
}

// Freeze ends the building phase: the route table becomes read-only and
// later Handle calls return ErrFrozen. Calling it again does nothing.
func (rt *Router) Freeze() {}

// Frozen reports whether Freeze has been called.
func (rt *Router) Frozen() bool { return false }

// Routes lists every registered route, in the order described in
// docs/tasks/04-routes-traversal.md.
func (rt *Router) Routes() []Route { return nil }

// Match reports which route a request for method and escaped path would
// reach, without serving it. It returns false for no match, and for paths
// ServeHTTP would refuse or redirect. It doesn't freeze the router.
func (rt *Router) Match(method, escapedPath string) (Match, bool) {
	return Match{}, false
}

// ServeHTTP routes a request.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {}
