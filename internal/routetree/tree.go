package routetree

// Tree is a trie of path segments that maps route patterns to values of any
// type V. The zero value is an empty tree ready to use.
//
// A Tree is not safe for concurrent Insert calls, or for Insert concurrently
// with Match. The router guards Insert with a mutex and freezes the tree
// before serving (design §7). Concurrent Match calls on a tree that is no
// longer changing are safe.
type Tree[V any] struct{}

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
	return errNotImplemented
}

// Match finds the most specific route for method and the request's path
// segments (already validated and decoded; a trailing slash is a final "").
// Parameter values are appended to buf, so a caller can pass a buffer that
// lives on its stack and avoid allocating.
func (t *Tree[V]) Match(method string, segs []string, buf []string) (Result[V], bool) {
	return Result[V]{}, false
}

// Allowed returns the methods of every route whose path matches segs, for a
// 405 response's Allow header. It may contain duplicates and is unsorted.
// Call it only after Match has failed: it's the slow path.
func (t *Tree[V]) Allowed(segs []string) []string {
	return nil
}
