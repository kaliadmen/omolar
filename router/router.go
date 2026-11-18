package router

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kaliadmen/omolar/internal/routetree"
)

// Router is an http.Handler that dispatches requests to the most specific
// matching route. Create one with New, register routes with Handle, then
// call Freeze (omolar.App does this at startup). A router that was never
// frozen freezes itself on its first request.
//
// Lifecycle (design §7): while building, Handle may be called from any
// goroutine. After Freeze the route table never changes, so requests read it
// with no lock at all.
type Router struct {
	mu     sync.Mutex  // guards tree, routes and the frozen transition
	frozen atomic.Bool // read lock-free on every request
	tree   routetree.Tree[http.Handler]
	routes []Route // snapshot taken by Freeze; nil until then

	notFound http.Handler
	limits   limits
}

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
	rt := &Router{
		notFound: http.NotFoundHandler(),
		limits:   limits{maxPathLength: defaultMaxPathLength, maxSegments: defaultMaxSegments},
	}
	for _, opt := range opts {
		opt(rt)
	}
	return rt
}

// Handle registers h for pattern, e.g. "GET /users/{id}". This is the checked
// registration API: it returns a *PatternError for a malformed pattern, a
// *ConflictError if the pattern is ambiguous with an existing route, or
// ErrFrozen after Freeze. A failed Handle leaves the router unchanged.
func (rt *Router) Handle(pattern string, h http.Handler) error {
	if h == nil {
		return errors.New("router: nil handler for " + pattern)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	// Checked under the lock: see "Why freezing takes the mutex" in §7.
	if rt.frozen.Load() {
		return ErrFrozen
	}
	// routetree checks pattern syntax and conflicts before changing anything,
	// so an error here leaves the tree exactly as it was.
	return rt.tree.Insert(pattern, h)
}

// HandleFunc registers fn as the handler for pattern. See Handle.
func (rt *Router) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request)) error {
	if fn == nil {
		return errors.New("router: nil handler func for " + pattern)
	}
	return rt.Handle(pattern, http.HandlerFunc(fn))
}

// Freeze ends the building phase: the route table becomes read-only and
// later Handle calls return ErrFrozen. Calling it again does nothing.
func (rt *Router) Freeze() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.frozen.Load() {
		return
	}
	// One-time finalising work, done here rather than on a request.
	rt.routes = rt.listRoutes()
	// The store comes last. Everything written above (and every Insert before
	// it, under the same mutex) happens-before any request that loads true.
	rt.frozen.Store(true)
}

// Frozen reports whether Freeze has been called.
func (rt *Router) Frozen() bool { return rt.frozen.Load() }

// Routes lists every registered route, in the order described in
// docs/tasks/04-routes-traversal.md.
func (rt *Router) Routes() []Route {
	if rt.frozen.Load() {
		// The snapshot never changes, but callers may modify what they get,
		// so hand out a copy.
		return append([]Route(nil), rt.routes...)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.listRoutes()
}

// listRoutes converts the tree's listing. Call with rt.mu held.
func (rt *Router) listRoutes() []Route {
	infos := rt.tree.Routes()
	out := make([]Route, len(infos))
	for i, ri := range infos {
		out[i] = Route{Method: ri.Method, Pattern: ri.Pattern}
	}
	return out
}

// Match reports which route a request for method and escaped path would
// reach, without serving it. It returns false for no match, and for paths
// ServeHTTP would refuse or redirect. It doesn't freeze the router.
func (rt *Router) Match(method, escapedPath string) (Match, bool) {
	if !rt.frozen.Load() {
		// Still building: Handle may be running, so read under the lock.
		rt.mu.Lock()
		defer rt.mu.Unlock()
	}
	var segBuf [32]string
	np := normalizePath(escapedPath, rt.limits, segBuf[:0])
	if np.status != pathOK {
		return Match{}, false
	}
	res, ok := rt.tree.Match(method, np.segs, nil)
	if !ok {
		return Match{}, false
	}
	m := Match{Pattern: res.Pattern}
	for i, name := range res.ParamNames {
		m.Params = append(m.Params, Param{name, res.ParamValues[i]})
	}
	if res.RestName != "" {
		m.Params = append(m.Params, Param{res.RestName, restValue(escapedPath, np, res.Rest)})
	}
	return m, true
}

// ServeHTTP routes a request: validate and normalise the path (phase 1),
// match it (phase 2), set the path values, and call the route's handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The safety net for standalone routers. With omolar.App the router is
	// already frozen and this is one atomic load.
	if !rt.frozen.Load() {
		rt.Freeze()
	}

	// Phase 1 (design §5.4). These buffers live on this function's stack, so
	// a static route costs no allocations (checked in step 12).
	escaped := r.URL.EscapedPath()
	var segBuf [32]string
	np := normalizePath(escaped, rt.limits, segBuf[:0])
	switch np.status {
	case pathTooLong:
		http.Error(w, http.StatusText(http.StatusRequestURITooLong), http.StatusRequestURITooLong)
		return
	case pathNotCanonical:
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			w.Header().Set("Location", redirectTarget(np.canonical, r.URL.RawQuery))
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		// Q3: a body-carrying request to a non-canonical path is a client
		// bug or a probe. Refuse it rather than bounce it.
		fallthrough
	case pathBad:
		// A generic message, never np.reason or the path: echoing crafted
		// input back is how reflection and log-forging attacks start.
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	// Phase 2.
	var paramBuf [8]string
	res, ok := rt.tree.Match(r.Method, np.segs, paramBuf[:0])
	if !ok {
		// Step 8 adds 405 Method Not Allowed here, when the path matches a
		// route for another method. Step 9 adds trailing-slash redirects.
		rt.notFound.ServeHTTP(w, r)
		return
	}

	for i, name := range res.ParamNames {
		r.SetPathValue(name, res.ParamValues[i])
	}
	if res.RestName != "" {
		r.SetPathValue(res.RestName, restValue(escaped, np, res.Rest))
	}
	r.Pattern = res.Pattern
	res.Value.ServeHTTP(w, r)
}

// restValue builds a {name...} value: the remaining segments joined by "/".
//
// When no segment needed decoding, the joined value is exactly the end of the
// escaped path (the path is canonical, so no "//" or "." in it), so we can
// return a substring of it: no copy, no allocation. Otherwise strings.Join
// builds it from the decoded segments.
func restValue(escaped string, np normalizedPath, rest []string) string {
	if len(rest) == 1 {
		return rest[0]
	}
	if !np.decoded {
		n := len(rest) - 1 // the slashes between the segments
		for _, s := range rest {
			n += len(s)
		}
		return escaped[len(escaped)-n:]
	}
	return strings.Join(rest, "/")
}
