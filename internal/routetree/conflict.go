package routetree

// ConflictError reports a route that could match the same request as an
// existing one, where neither is more specific (design §6.2).
type ConflictError struct {
	New      string // the pattern being added
	Existing string // the pattern already in the tree
	Example  string // a request both match, e.g. "GET /a/b"
}

func (e *ConflictError) Error() string {
	return ""
}

// relationship describes how the requests two patterns match compare.
type relationship uint8

const (
	equivalent   relationship = iota // they match exactly the same requests
	moreSpecific                     // the first matches a strict subset of the second's requests
	moreGeneral                      // the first matches a strict superset
	disjoint                         // no request matches both
	overlaps                         // some requests match both, but neither contains the other
)

// relate compares two patterns, method and path together.
func relate(p1, p2 *pattern) relationship {
	return disjoint
}

// conflictExample builds a request that both patterns match. Only meaningful
// when relate(p1, p2) is equivalent or overlaps.
func conflictExample(p1, p2 *pattern) string {
	return ""
}

// checkConflict returns a *ConflictError if p is ambiguous with any route
// already in the tree.
func (t *Tree[V]) checkConflict(p *pattern) error {
	return nil
}
