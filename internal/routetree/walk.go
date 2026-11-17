package routetree

import (
	"slices"
	"strings"
)

// RouteInfo describes one registered route, for listing and logging.
type RouteInfo struct {
	Method  string // "" for a method-less route
	Pattern string // the pattern exactly as registered
}

// Routes lists every route in the tree by walking it depth-first, in the
// order described in docs/tasks/04-routes-traversal.md.
func (t *Tree[V]) Routes() []RouteInfo {
	var routes []RouteInfo

	// A helper to recursively walk the nodes and tables
	var walk func(n *node[V])
	walk = func(n *node[V]) {
		if n == nil {
			return
		}

		// 1. Visit leaf node routes
		if n.leaf != nil {
			collectTable(n.leaf, &routes)
		}

		// 2. Visit literal children
		for _, child := range n.statics {
			walk(child)
		}

		// 3. Visit {name} child (if any)
		if n.param != nil {
			walk(n.param)
		}

		// 4. Visit {name...} routes (if any)
		if n.rest != nil {
			collectTable(n.rest, &routes)
		}
	}

	walk(&t.root)
	return routes
}

// collectTable appends all RouteInfo items from a table to the slice.

func collectTable[V any](tb *table[V], routes *[]RouteInfo) {
	if tb == nil {
		return
	}

	// Make a copy so we don't modify the original tree.
	sorted := slices.Clone(tb.byMethod)

	// Sort by HTTP method name.
	slices.SortFunc(sorted, func(a, b *route[V]) int {
		return strings.Compare(a.pat.method, b.pat.method)
	})

	// Collect routes in alphabetical method order.
	for _, r := range sorted {
		*routes = append(*routes, RouteInfo{
			Method:  r.pat.method,
			Pattern: r.pat.raw,
		})
	}

	// Method-less routes always come last.
	if tb.any != nil {
		*routes = append(*routes, RouteInfo{
			Method:  "",
			Pattern: tb.any.pat.raw,
		})
	}
}
