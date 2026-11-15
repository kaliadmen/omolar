package routetree

// Tests for Task 4 (docs/tasks/04-routes-traversal.md). Make these pass by
// implementing Routes in walk.go. Don't change these tests; if you think one
// is wrong, raise it in review.
//
//   go test ./internal/routetree -run Routes -v

import (
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
)

// exampleRoutes is the task card's worked example, deliberately inserted in a
// scrambled order.
var exampleRoutes = []string{
	"GET /users/{id}", "POST /users", "/health", "GET /static/{p...}",
	"GET /users/me", "DELETE /users/{id}", "GET /", "PUT /static/{p...}",
	"GET /users/{id}/posts", "GET /static/app.js", "OPTIONS /health", "GET /users",
}

// exampleOrder is the order Routes must return them in.
var exampleOrder = []RouteInfo{
	{"GET", "GET /"},
	{"OPTIONS", "OPTIONS /health"},
	{"", "/health"},
	{"GET", "GET /static/app.js"},
	{"GET", "GET /static/{p...}"},
	{"PUT", "PUT /static/{p...}"},
	{"GET", "GET /users"},
	{"POST", "POST /users"},
	{"GET", "GET /users/me"},
	{"DELETE", "DELETE /users/{id}"},
	{"GET", "GET /users/{id}"},
	{"GET", "GET /users/{id}/posts"},
}

func TestRoutes_Empty(t *testing.T) {
	var tr Tree[string]
	if got := tr.Routes(); len(got) != 0 {
		t.Errorf("empty tree: Routes() = %v, want none", got)
	}
}

func TestRoutes_Order(t *testing.T) {
	tr := newTree(t, exampleRoutes...)
	got := tr.Routes()
	if !slices.Equal(got, exampleOrder) {
		t.Errorf("Routes() order is wrong.\n got:  %v\n want: %v", got, exampleOrder)
	}
}

func TestRoutes_SameForAnyInsertOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 4)) // fixed seed: failures reproduce
	for run := range 20 {
		shuffled := slices.Clone(exampleRoutes)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got := newTree(t, shuffled...).Routes()
		if !slices.Equal(got, exampleOrder) {
			t.Fatalf("run %d (inserted as %q):\n got:  %v\n want: %v", run, shuffled, got, exampleOrder)
		}
	}
}

func TestRoutes_ReportsPatternsAsRegistered(t *testing.T) {
	// {$} is stored as a trailing slash internally, but Routes shows what the
	// developer actually wrote.
	tr := newTree(t, "GET /{$}", "GET /docs/{$}")
	want := []RouteInfo{{"GET", "GET /{$}"}, {"GET", "GET /docs/{$}"}}
	if got := tr.Routes(); !slices.Equal(got, want) {
		t.Errorf("Routes() = %v, want %v", got, want)
	}
}

func TestRoutes_WalksTheTree(t *testing.T) {
	// The tree also keeps a plain list of routes (for conflict checks). This
	// task is about traversal, so Routes must find them by walking the nodes.
	tr := newTree(t, exampleRoutes...)
	tr.routes = nil
	if got := tr.Routes(); !slices.Equal(got, exampleOrder) {
		t.Errorf("Routes() must walk the nodes, not read t.routes.\n got:  %v\n want: %v", got, exampleOrder)
	}
}

func TestRoutes_FindsEveryRoute(t *testing.T) {
	// A bigger tree: every kind of node, several levels deep.
	tr := newTree(t, orderRoutes...)
	got := tr.Routes()
	if len(got) != len(orderRoutes) {
		t.Fatalf("Routes() returned %d routes, want %d", len(got), len(orderRoutes))
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.Pattern] {
			t.Errorf("%q listed twice", r.Pattern)
		}
		seen[r.Pattern] = true
	}
	for _, p := range orderRoutes {
		if !seen[p] {
			t.Errorf("%q is missing", p)
		}
	}
}

func TestRoutes_ReturnsACopy(t *testing.T) {
	tr := newTree(t, exampleRoutes...)
	first := tr.Routes()
	if len(first) == 0 {
		t.Fatal("no routes returned")
	}
	first[0] = RouteInfo{"HACKED", "HACKED /"}
	if second := tr.Routes(); !slices.Equal(second, exampleOrder) {
		t.Error("changing the returned slice changed what the next call returns")
	}
}

func TestRoutes_ReadOnly(t *testing.T) {
	// Routes may be called while the server is handling requests, so it must
	// not change the tree, not even to sort something in place. Run with -race
	// (make docker-test does) to catch that.
	tr := newTree(t, exampleRoutes...)
	segs, _ := splitPath("/users/42")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				tr.Routes()
				tr.Match("GET", segs, nil)
			}
		})
	}
	wg.Wait()
	if got := tr.Routes(); !slices.Equal(got, exampleOrder) {
		t.Errorf("Routes() changed after concurrent use: %v", got)
	}
}
