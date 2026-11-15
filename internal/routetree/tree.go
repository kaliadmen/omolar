package routetree

import (
	"fmt"
	"slices"
)

// Tree is a trie of path segments that maps route patterns to values of any
// type V. The zero value is an empty tree ready to use.
//
// GO CONCEPT: generics. [V any] makes Tree a type with a parameter: a
// Tree[http.Handler] in the router, a Tree[string] in the tests. Like a C++
// template<typename V>, but checked once for every possible V, and the
// compiler shares one copy of the code between types with the same memory
// "shape" (all pointer types, for example) instead of stamping out one per type.
//
// A Tree is not safe for concurrent Insert calls, or for Insert concurrently
// with Match. The router guards Insert with a mutex and freezes the tree
// before serving (design §7). Concurrent Match calls on a tree that is no
// longer changing are safe: Match only reads.
type Tree[V any] struct {
	root   node[V]
	routes []*route[V] // every route, in insertion order (conflict checks)
}

// node is one position in the trie: everything that can come next after the
// segments that led here.
//
// PATTERN: Composite. A node holds child nodes of the same type, so the whole
// route table is one recursive structure, and match is one recursive function
// that works the same at every level.
type node[V any] struct {
	// Literal children, sorted by label so lookup can binary-search.
	// labels[i] is the segment text for statics[i]. Two parallel slices
	// rather than one slice of structs: the search loop only reads labels,
	// so they sit together in memory (cache-friendly).
	labels  []string
	statics []*node[V]

	// At most one {name} child. Wildcard names live on each route, not here,
	// so "/users/{id}" and "/users/{uid}/posts" can share this node.
	param *node[V]

	// Routes whose last segment is {name...} at this position. A method
	// table, because "GET /f/{p...}" and "PUT /f/{p...}" share the slot.
	rest *table[V]

	// Routes that end exactly at this node.
	leaf *table[V]
}

// table holds the routes for one position, by method.
type table[V any] struct {
	byMethod []*route[V] // routes with a method; a handful at most, so a slice beats a map
	any      *route[V]   // the route registered with no method, if any
}

// route is one registered pattern and its value.
type route[V any] struct {
	pat        *pattern
	value      V
	paramNames []string // the {name} wildcards, in order
	restName   string   // the {name...} wildcard, "" if none
}

// Result describes a successful Match.
type Result[V any] struct {
	Value   V      // the value stored with the route
	Pattern string // the route's pattern as registered
	Method  string // the route's method; "" for a method-less route

	// ParamNames and ParamValues are the {name} wildcards, in pattern order.
	// ParamNames belongs to the route and must not be modified. ParamValues is
	// built in the buf slice passed to Match.
	ParamNames  []string
	ParamValues []string

	// RestName is the name of a terminal {name...} wildcard, "" if none. Rest
	// holds the segments it matched (a sub-slice of Match's segs). Joined with
	// "/" they give the value, e.g. ["a", ""] -> "a/".
	RestName string
	Rest     []string
}

// Insert parses pattern and adds it with value.
func (t *Tree[V]) Insert(pattern string, value V) error {
	p, err := parsePattern(pattern)
	if err != nil {
		return err
	}

	r := &route[V]{pat: p, value: value}
	for _, sg := range p.segments {
		switch sg.kind {
		case segParam:
			r.paramNames = append(r.paramNames, sg.text)
		case segRest:
			r.restName = sg.text
		}
	}

	// Find the table this route belongs in, creating nodes on the way. A
	// {name...} segment is always last, so it ends the walk.
	n := &t.root
	var slot **table[V]
	for _, sg := range p.segments {
		switch sg.kind {
		case segLiteral:
			n = n.literalChild(sg.text, true)
		case segParam:
			if n.param == nil {
				n.param = &node[V]{}
			}
			n = n.param
		case segRest:
			slot = &n.rest
		}
	}
	if slot == nil {
		slot = &n.leaf
	}
	// GO CONCEPT: a pointer to a pointer. slot points at the n.rest or n.leaf
	// field itself, so *slot = ... fills in whichever field it is, the way a
	// `table **` lets a C function set the caller's pointer.
	if *slot == nil {
		*slot = &table[V]{}
	}
	if err := (*slot).add(r); err != nil {
		return err
	}
	t.routes = append(t.routes, r)
	return nil
}

// literalChild returns the child for label. With create set, it adds the
// child if it's missing, keeping labels sorted.
func (n *node[V]) literalChild(label string, create bool) *node[V] {
	// GO CONCEPT: slices.BinarySearch (Go 1.21+) is std::lower_bound: it
	// returns where label is, or where it would go to keep the slice sorted.
	i, found := slices.BinarySearch(n.labels, label)
	if found {
		return n.statics[i]
	}
	if !create {
		return nil
	}
	child := &node[V]{}
	n.labels = slices.Insert(n.labels, i, label)
	n.statics = slices.Insert(n.statics, i, child)
	return child
}

// add puts r in the table. Conflict detection normally rejects a duplicate
// before it gets here; this check is a last line of defence so a route is
// never silently replaced.
func (tb *table[V]) add(r *route[V]) error {
	m := r.pat.method
	if m == "" {
		if tb.any != nil {
			return fmt.Errorf("routetree: %q duplicates %q", r.pat.raw, tb.any.pat.raw)
		}
		tb.any = r
		return nil
	}
	for _, existing := range tb.byMethod {
		if existing.pat.method == m {
			return fmt.Errorf("routetree: %q duplicates %q", r.pat.raw, existing.pat.raw)
		}
	}
	tb.byMethod = append(tb.byMethod, r)
	return nil
}

// matcher carries the state of one Match call down the recursion. It lives on
// Match's stack and is passed by pointer, so the recursion never copies it.
type matcher struct {
	method string
	vals   []string // {name} values found so far (pushed and popped)
	rest   []string // segments taken by a {name...}

	// collect switches on "Allowed mode": record every method seen on a
	// matching path instead of stopping at the first match.
	collect bool
	allowed []string
}

// Match finds the most specific route for method and the request's path
// segments (already validated and decoded; a trailing slash is a final "").
// Parameter values are appended to buf, so a caller can pass a buffer that
// lives on its stack and avoid allocating.
func (t *Tree[V]) Match(method string, segs []string, buf []string) (Result[V], bool) {
	m := matcher{method: method, vals: buf[:0]}
	r := t.root.match(segs, &m)
	if r == nil {
		return Result[V]{}, false
	}
	res := Result[V]{
		Value:       r.value,
		Pattern:     r.pat.raw,
		Method:      r.pat.method,
		ParamNames:  r.paramNames,
		ParamValues: m.vals,
		RestName:    r.restName,
	}
	if r.restName != "" {
		res.Rest = m.rest
	}
	return res, true
}

// Allowed returns the methods of every route whose path matches segs, for a
// 405 response's Allow header. It may contain duplicates and is unsorted.
// Call it only after Match has failed: it's the slow path.
//
// Why a second pass instead of recording methods during Match? Match often
// passes a wrong-method leaf on the way to a successful match (GET /users/new
// reaches POST /users/new before {id}). Recording there would allocate on
// requests that succeed. Doing it only once Match has failed keeps the common
// path allocation-free; 405s are rare and may cost a little more.
func (t *Tree[V]) Allowed(segs []string) []string {
	var buf [8]string
	m := matcher{vals: buf[:0], collect: true}
	t.root.match(segs, &m)
	return m.allowed
}

// match is the depth-first search from design §5.4. It returns the first
// route found trying literal, then {name}, then {name...} at each level,
// which (because conflicting routes are rejected at Insert) is the most
// specific matching route.
func (n *node[V]) match(segs []string, m *matcher) *route[V] {
	if len(segs) == 0 {
		return n.leaf.pick(m) // the path ends exactly here
	}
	seg, tail := segs[0], segs[1:]

	// 1. Literal child. This includes the "" child made by a pattern ending
	//    in "/" or {$}, so a request's trailing slash is matched here.
	if child := n.literalChild(seg, false); child != nil {
		if r := child.match(tail, m); r != nil {
			return r
		}
	}

	// 2. {name}: exactly one non-empty segment.
	if n.param != nil && seg != "" {
		m.vals = append(m.vals, seg)
		if r := n.param.match(tail, m); r != nil {
			return r
		}
		// Backtrack: this branch failed, so its value must not survive into
		// whatever branch matches instead.
		m.vals = m.vals[:len(m.vals)-1]
	}

	// 3. {name...}: everything that's left. We only get here with at least
	//    one segment remaining, so a {name...} always comes after a slash:
	//    "/static/{p...}" matches "/static/" but never "/static".
	if r := n.rest.pick(m); r != nil {
		m.rest = segs
		return r
	}
	return nil
}

// pick chooses the route for m.method from a table, the same way for an
// ordinary leaf and for a {name...} slot: exact method, then HEAD served by
// GET, then the method-less route.
//
// GO CONCEPT: calling a method on a nil pointer is legal in Go, as long as
// the method checks for nil before touching fields. That lets match write
// n.leaf.pick(m) without first asking whether n.leaf exists.
func (tb *table[V]) pick(m *matcher) *route[V] {
	if tb == nil {
		return nil
	}
	if m.collect {
		for _, r := range tb.byMethod {
			m.allowed = append(m.allowed, r.pat.method)
		}
		return nil // keep searching: Allowed wants every matching route
	}
	for _, r := range tb.byMethod {
		if r.pat.method == m.method {
			return r
		}
	}
	if m.method == "HEAD" {
		for _, r := range tb.byMethod {
			if r.pat.method == "GET" {
				return r
			}
		}
	}
	return tb.any
}

// TRY: in tree_test.go, run TestMatch_FailedBranchesAllocateNothing, then
// change pick so the non-collect path also appends to m.allowed before
// returning tb.any. Run it again: the allocation it reports is the reason
// Allowed is a separate pass.
