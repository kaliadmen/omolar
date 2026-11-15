package routetree

// RouteInfo describes one registered route, for listing and logging.
type RouteInfo struct {
	Method  string // "" for a method-less route
	Pattern string // the pattern exactly as registered
}

// Routes lists every route in the tree by walking it depth-first, in the
// order described in docs/tasks/04-routes-traversal.md.
//
// TODO(quinze): Task 4. Replace this stub with your implementation.
func (t *Tree[V]) Routes() []RouteInfo {
	return nil
}
