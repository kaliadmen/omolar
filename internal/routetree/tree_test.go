package routetree

// Tree behaviour: design §5.4, §5.4.1, §6.1 and test plan §11.2–11.3.
// Each route's stored value is its own pattern, so failures read clearly.

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func newTree(t *testing.T, patterns ...string) *Tree[string] {
	t.Helper()
	var tr Tree[string]
	for _, p := range patterns {
		if err := tr.Insert(p, p); err != nil {
			t.Fatalf("Insert(%q): %v", p, err)
		}
	}
	return &tr
}

// lookup splits a request path the same way the router will (a trailing
// slash becomes a final "") and describes the result as one string, e.g.
// "GET /users/{id} id=42" or "GET /static/{p...} p=a/" or "no match".
func lookup(t *testing.T, tr *Tree[string], method, path string) string {
	t.Helper()
	segs, err := splitPath(path)
	if err != nil {
		t.Fatalf("bad request path %q in test: %v", path, err)
	}
	res, ok := tr.Match(method, segs, make([]string, 0, 8))
	if !ok {
		return "no match"
	}
	if res.Value != res.Pattern {
		return "WRONG VALUE " + res.Value + " for " + res.Pattern
	}
	if len(res.ParamNames) != len(res.ParamValues) {
		return "MISMATCHED PARAMS " + strings.Join(res.ParamNames, ",") + " = " + strings.Join(res.ParamValues, ",")
	}
	out := res.Pattern
	for i, name := range res.ParamNames {
		out += " " + name + "=" + res.ParamValues[i]
	}
	if res.RestName != "" {
		out += " " + res.RestName + "=" + strings.Join(res.Rest, "/")
	} else if res.Rest != nil {
		return "REST SEGMENTS WITHOUT A REST NAME on " + res.Pattern
	}
	return out
}

func allowed(t *testing.T, tr *Tree[string], path string) string {
	t.Helper()
	segs, err := splitPath(path)
	if err != nil {
		t.Fatalf("bad request path %q in test: %v", path, err)
	}
	m := slices.Clone(tr.Allowed(segs))
	slices.Sort(m)
	return strings.Join(slices.Compact(m), ",")
}

type matchCase struct {
	id     string
	routes []string
	method string
	path   string
	want   string
}

func runMatchCases(t *testing.T, cases []matchCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.id+" "+tc.method+" "+tc.path, func(t *testing.T) {
			tr := newTree(t, tc.routes...)
			if got := lookup(t, tr, tc.method, tc.path); got != tc.want {
				t.Errorf("routes %q\n  %s %s\n  got  %s\n  want %s", tc.routes, tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// §11.2: precedence and wildcard backtracking.
func TestMatch_PrecedenceAndBacktracking(t *testing.T) {
	runMatchCases(t, []matchCase{
		{"P1", []string{"GET /users/me", "GET /users/{id}"}, "GET", "/users/me", "GET /users/me"},
		{"P2", []string{"GET /users/me", "GET /users/{id}"}, "GET", "/users/42", "GET /users/{id} id=42"},
		{"P3", []string{"GET /users/{id}"}, "GET", "/users/", "no match"},
		{"P4", []string{"GET /a/b/z", "GET /a/{x}/c"}, "GET", "/a/b/c", "GET /a/{x}/c x=b"},
		{"P5", []string{"GET /a/b/c/z", "GET /a/b/{y}/w", "GET /a/{x}/c/d"}, "GET", "/a/b/c/d", "GET /a/{x}/c/d x=b"},
		{"P6", []string{"GET /a/{x}/c", "GET /a/{y...}"}, "GET", "/a/b/d", "GET /a/{y...} y=b/d"},
		{"P7", []string{"GET /files/{name}/meta", "GET /files/{p...}"}, "GET", "/files/a/b", "GET /files/{p...} p=a/b"},
		{"P8", []string{"GET /static/app.js", "GET /static/{p...}"}, "GET", "/static/app.css", "GET /static/{p...} p=app.css"},
		{"P9a", []string{"GET /static/{name}", "GET /static/{p...}"}, "GET", "/static/app.js", "GET /static/{name} name=app.js"},
		{"P9b", []string{"GET /static/{name}", "GET /static/{p...}"}, "GET", "/static/css/app.css", "GET /static/{p...} p=css/app.css"},
		{"P11", []string{"GET /{a}/x/{b}"}, "GET", "/1/x/2", "GET /{a}/x/{b} a=1 b=2"},
		{"P12a", []string{"GET /users/{id}", "GET /users/{uid}/posts"}, "GET", "/users/7", "GET /users/{id} id=7"},
		{"P12b", []string{"GET /users/{id}", "GET /users/{uid}/posts"}, "GET", "/users/7/posts", "GET /users/{uid}/posts uid=7"},
		{"P13a", []string{"GET /{$}", "GET /{p...}"}, "GET", "/", "GET /{$}"},
		{"P13b", []string{"GET /{$}", "GET /{p...}"}, "GET", "/x", "GET /{p...} p=x"},
		{"empty", nil, "GET", "/", "no match"},
		{"root", []string{"GET /"}, "GET", "/", "GET /"},
		{"root only", []string{"GET /"}, "GET", "/x", "no match"},
		{"no slash vs slash", []string{"GET /a"}, "GET", "/a/", "no match"},
		{"slash vs no slash", []string{"GET /a/"}, "GET", "/a", "no match"},
		{"longer path", []string{"GET /a"}, "GET", "/a/b", "no match"},
	})
}

// §5.4.1: terminal {rest...}, exactly.
func TestMatch_TerminalRest(t *testing.T) {
	static := []string{"GET /static/{p...}"}
	runMatchCases(t, []matchCase{
		{"R1", static, "GET", "/static", "no match"},
		{"R2", static, "GET", "/static/", "GET /static/{p...} p="},
		{"R3", static, "GET", "/static/a", "GET /static/{p...} p=a"},
		{"R4", static, "GET", "/static/a/", "GET /static/{p...} p=a/"},
		{"R5", static, "GET", "/static/a/b", "GET /static/{p...} p=a/b"},
		{"R6", []string{"GET /{p...}"}, "GET", "/", "GET /{p...} p="},
		{"R7", []string{"GET /{p...}"}, "GET", "/x/y/", "GET /{p...} p=x/y/"},
		{"R8", []string{"GET /files/{p...}", "GET /files/{$}"}, "GET", "/files/", "GET /files/{$}"},
		{"R9", []string{"GET /files/{p...}", "GET /files/{name}"}, "GET", "/files/a", "GET /files/{name} name=a"},
		{"R10", []string{"GET /files/{p...}", "GET /files/{name}"}, "GET", "/files/a/", "GET /files/{p...} p=a/"},
		{"R11", []string{"GET /files/{p...}", "PUT /files/{name}"}, "GET", "/files/a", "GET /files/{p...} p=a"},
		{"R12", []string{"GET /files/{p...}", "PUT /files/{name}"}, "PUT", "/files/a/b", "no match"},
		{"rest per method", []string{"GET /files/{p...}", "PUT /files/{q...}"}, "PUT", "/files/x/y", "PUT /files/{q...} q=x/y"},
		{"rest with params before", []string{"GET /u/{id}/files/{p...}"}, "GET", "/u/7/files/a/b", "GET /u/{id}/files/{p...} id=7 p=a/b"},
	})
}

// §11.3: method precedence (the tree's part: pick rules and fallback).
func TestMatch_MethodPrecedence(t *testing.T) {
	gx := []string{"GET /x", "/x"}
	users := []string{"GET /users/{id}", "POST /users/new"}
	files := []string{"GET /files/{p...}", "PUT /files/{name}"}
	runMatchCases(t, []matchCase{
		{"M1", gx, "GET", "/x", "GET /x"},
		{"M2", gx, "PUT", "/x", "/x"},
		{"M3", gx, "HEAD", "/x", "GET /x"},
		{"M4a", []string{"HEAD /x", "GET /x"}, "HEAD", "/x", "HEAD /x"},
		{"M4b", []string{"HEAD /x", "GET /x"}, "GET", "/x", "GET /x"},
		{"M5a", []string{"/x"}, "OPTIONS", "/x", "/x"},
		{"M5b", []string{"/x"}, "HEAD", "/x", "/x"},
		{"M5c", []string{"/x"}, "PURGE", "/x", "/x"},
		{"M6", users, "GET", "/users/new", "GET /users/{id} id=new"},
		{"M7", users, "POST", "/users/new", "POST /users/new"},
		{"M8", users, "HEAD", "/users/new", "GET /users/{id} id=new"},
		{"M9", users, "DELETE", "/users/new", "no match"},
		{"M10", files, "GET", "/files/a", "GET /files/{p...} p=a"},
		{"M11", files, "PUT", "/files/a", "PUT /files/{name} name=a"},
		{"M12", files, "PUT", "/files/a/b", "no match"},
		{"M13", files, "DELETE", "/files/a", "no match"},
		{"M14a", []string{"OPTIONS /x", "GET /x"}, "OPTIONS", "/x", "OPTIONS /x"},
		{"M14b", []string{"OPTIONS /x", "GET /x"}, "POST", "/x", "no match"},
		{"M15a", []string{"PURGE /cache"}, "PURGE", "/cache", "PURGE /cache"},
		{"M15b", []string{"PURGE /cache"}, "GET", "/cache", "no match"},
		{"M16", []string{"GET /x"}, "get", "/x", "no match"},
		{"HEAD on rest", []string{"GET /static/{p...}"}, "HEAD", "/static/a/", "GET /static/{p...} p=a/"},
		{"HEAD not served by POST", []string{"POST /x"}, "HEAD", "/x", "no match"},
	})
}

// The methods a 405 response's Allow header is built from (design §6.4).
func TestAllowed(t *testing.T) {
	cases := []struct {
		id     string
		routes []string
		path   string
		want   string // sorted, de-duplicated
	}{
		{"M9", []string{"GET /users/{id}", "POST /users/new"}, "/users/new", "GET,POST"},
		{"M12", []string{"GET /files/{p...}", "PUT /files/{name}"}, "/files/a/b", "GET"},
		{"M13", []string{"GET /files/{p...}", "PUT /files/{name}"}, "/files/a", "GET,PUT"},
		{"M14", []string{"OPTIONS /x", "GET /x"}, "/x", "GET,OPTIONS"},
		{"M15", []string{"PURGE /cache"}, "/cache", "PURGE"},
		{"no route for the path", []string{"GET /x"}, "/y", ""},
		{"only the exact path counts", []string{"GET /tree/"}, "/tree", ""},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			tr := newTree(t, tc.routes...)
			if got := allowed(t, tr, tc.path); got != tc.want {
				t.Errorf("Allowed(%s) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestMatch_ValuesUseCallersBuffer(t *testing.T) {
	// The allocation budget (design §8) depends on this: parameter values go
	// into the buffer the router passes in, which can live on its stack.
	tr := newTree(t, "GET /orgs/{org}/repos/{repo}")
	segs, _ := splitPath("/orgs/acme/repos/omolar")
	buf := make([]string, 0, 8)
	res, ok := tr.Match("GET", segs, buf)
	if !ok || len(res.ParamValues) != 2 {
		t.Fatalf("no match or wrong params: %+v", res)
	}
	if &res.ParamValues[0] != &buf[:1][0] {
		t.Error("ParamValues should be built in the caller's buffer")
	}
}

func TestMatch_StaticRouteAllocatesNothing(t *testing.T) {
	tr := newTree(t, "GET /api/v1/status", "GET /api/v1/users/{id}", "POST /api/v1/users")
	segs, _ := splitPath("/api/v1/status")
	var arr [8]string
	allocs := testing.AllocsPerRun(200, func() {
		tr.Match("GET", segs, arr[:0])
	})
	if allocs != 0 {
		t.Errorf("Match on a static route made %.1f allocations, want 0", allocs)
	}
}

func TestMatch_FailedBranchesAllocateNothing(t *testing.T) {
	// GET /users/new first reaches POST /users/new (wrong method), then
	// backtracks to {id}. Noting "POST is allowed here" on the way would cost
	// an allocation on a request that succeeds, so it mustn't happen.
	tr := newTree(t, "GET /users/{id}", "POST /users/new")
	segs, _ := splitPath("/users/new")
	var arr [8]string
	allocs := testing.AllocsPerRun(200, func() {
		tr.Match("GET", segs, arr[:0])
	})
	if allocs != 0 {
		t.Errorf("Match made %.1f allocations, want 0", allocs)
	}
}

func TestInsert_ReturnsPatternErrors(t *testing.T) {
	var tr Tree[string]
	err := tr.Insert("get /x", "v")
	var pe *PatternError
	if !errors.As(err, &pe) {
		t.Fatalf("Insert of a bad pattern returned %v, want *PatternError", err)
	}
}

// P14: registration order never matters (design §6.1).
// orderRoutes is a realistic, conflict-free route set used by several tests.
var orderRoutes = []string{
	"GET /", "GET /users", "GET /users/", "GET /users/me", "POST /users/new",
	"GET /users/{id}", "PUT /users/{id}", "GET /users/{id}/posts",
	"GET /users/{id}/posts/{post}", "DELETE /users/{id}/posts/{post}",
	"GET /static/{p...}", "GET /static/app.js", "HEAD /static/app.js",
	"GET /files/{name}", "GET /files/{$}", "PUT /files/{p...}",
	"GET /a/b/z", "GET /a/{x}/c", "GET /a/b/{y}/w", "GET /a/{x}/c/d",
	"GET /health", "OPTIONS /health",
	"GET /orgs/{org}/repos/{repo}", "GET /orgs/{org}/repos/{repo}/issues/{n}",
	"POST /orgs/{org}/repos", "GET /docs/{p...}", "GET /docs/{section}/{page}",
	"GET /{p...}",
}

func TestMatch_OrderIndependent(t *testing.T) {
	routes := orderRoutes
	requests := []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/users"}, {"GET", "/users/"}, {"GET", "/users/me"},
		{"GET", "/users/new"}, {"POST", "/users/new"}, {"PUT", "/users/9"},
		{"GET", "/users/9/posts"}, {"DELETE", "/users/9/posts/3"}, {"HEAD", "/users/9/posts/3"},
		{"GET", "/static/app.js"}, {"HEAD", "/static/app.js"}, {"GET", "/static/a/b/"},
		{"GET", "/files/"}, {"GET", "/files/x"}, {"PUT", "/files/x"}, {"PUT", "/files/x/y"},
		{"GET", "/a/b/c"}, {"GET", "/a/b/c/d"}, {"GET", "/a/b/q/w"}, {"OPTIONS", "/health"},
		{"GET", "/orgs/acme/repos/omolar/issues/12"}, {"POST", "/orgs/acme/repos"},
		{"GET", "/docs/guide/intro"}, {"GET", "/docs/guide"}, {"GET", "/nothing/here"},
		{"DELETE", "/users/9"}, {"PATCH", "/health"},
	}

	reference := newTree(t, routes...)
	want := make([]string, len(requests))
	for i, rq := range requests {
		want[i] = lookup(t, reference, rq.method, rq.path)
	}

	rng := rand.New(rand.NewPCG(2026, 11)) // fixed seed: failures reproduce
	for run := range 50 {
		shuffled := slices.Clone(routes)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		tr := newTree(t, shuffled...)
		for i, rq := range requests {
			if got := lookup(t, tr, rq.method, rq.path); got != want[i] {
				t.Fatalf("run %d: %s %s = %s, but %s in the original order\norder: %q",
					run, rq.method, rq.path, got, want[i], shuffled)
			}
		}
	}

	// Spot-check the reference so the test can't pass by being wrong the
	// same way every time.
	spot := map[string]string{
		"GET /users/new":    "GET /users/{id} id=new",
		"GET /static/a/b/":  "GET /static/{p...} p=a/b/",
		"GET /a/b/c/d":      "GET /a/{x}/c/d x=b",
		"GET /docs/guide":   "GET /docs/{p...} p=guide",
		"GET /nothing/here": "GET /{p...} p=nothing/here",
		"PATCH /health":     "no match",
	}
	for i, rq := range requests {
		if w, ok := spot[rq.method+" "+rq.path]; ok && want[i] != w {
			t.Errorf("%s %s = %s, want %s", rq.method, rq.path, want[i], w)
		}
	}
}
