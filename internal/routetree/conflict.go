package routetree

import (
	"fmt"
	"strings"
)

// ConflictError reports a route that could match the same request as an
// existing one, where neither is more specific (design §6.2).
type ConflictError struct {
	New      string // the pattern being added
	Existing string // the pattern already in the tree
	Example  string // a request both match, e.g. "GET /a/b"
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("route %q conflicts with %q: a request such as %q matches both, and neither is more specific",
		e.New, e.Existing, e.Example)
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

// checkConflict returns a *ConflictError if p is ambiguous with any route
// already in the tree. It compares p with every route: O(routes × segments),
// at startup only (design §9).
func (t *Tree[V]) checkConflict(p *pattern) error {
	for _, r := range t.routes {
		switch relate(p, r.pat) {
		case equivalent, overlaps:
			return &ConflictError{New: p.raw, Existing: r.pat.raw, Example: conflictExample(p, r.pat)}
		}
	}
	return nil
}

// relate compares two patterns, method and path together.
//
// Why this rule is the right one: match picks the first route found trying
// literal → {name} → {name...} left to right. That is "the most specific"
// only if any two routes that can match the same request are ordered:
// one's requests a subset of the other's. relate finds the pairs that
// aren't (equivalent or overlaps), and Insert refuses them.
// TestRelate_AgreesWithBruteForce checks relate against real matching.
func relate(p1, p2 *pattern) relationship {
	return combine(relateMethods(p1.method, p2.method), relatePaths(p1.segments, p2.segments))
}

// relateMethods compares the methods two patterns accept. "" accepts every
// method. GET also accepts HEAD (match serves HEAD with a GET route), so a
// HEAD route is more specific than a GET one.
func relateMethods(m1, m2 string) relationship {
	switch {
	case m1 == m2:
		return equivalent
	case m1 == "":
		return moreGeneral
	case m2 == "":
		return moreSpecific
	case m1 == "HEAD" && m2 == "GET":
		return moreSpecific
	case m1 == "GET" && m2 == "HEAD":
		return moreGeneral
	default:
		return disjoint
	}
}

// relatePaths compares two paths segment by segment.
func relatePaths(s1, s2 []segment) relationship {
	rel := equivalent
	for i := 0; ; i++ {
		end1, end2 := i >= len(s1), i >= len(s2)
		if end1 && end2 {
			return rel
		}
		if end1 || end2 {
			// One path is longer. Without a {name...} (handled below, before
			// either runs out) different lengths never match the same request.
			return disjoint
		}
		a, b := s1[i], s2[i]

		// A {name...} accepts everything from here on (one or more segments).
		// The other pattern has a segment here too, so whatever it accepts
		// from here is a subset: it's more specific, unless it's also a rest.
		if a.kind == segRest || b.kind == segRest {
			switch {
			case a.kind == segRest && b.kind == segRest:
				return combine(rel, equivalent)
			case a.kind == segRest:
				return combine(rel, moreGeneral)
			default:
				return combine(rel, moreSpecific)
			}
		}

		rel = combine(rel, relateSegments(a, b))
		if rel == disjoint {
			return disjoint
		}
	}
}

// relateSegments compares two non-rest segments.
func relateSegments(a, b segment) relationship {
	switch {
	case a.kind == segLiteral && b.kind == segLiteral:
		if a.text == b.text {
			return equivalent
		}
		return disjoint
	case a.kind == segParam && b.kind == segParam:
		return equivalent
	case a.kind == segLiteral: // literal vs {name}
		if a.text == "" {
			return disjoint // {name} never matches the empty (trailing-slash) segment
		}
		return moreSpecific
	default: // {name} vs literal
		if b.text == "" {
			return disjoint
		}
		return moreGeneral
	}
}

// combine merges two relationships that must both hold, for example a
// method relationship and a path relationship. More specific in one way and
// more general in another means neither contains the other: overlaps.
func combine(a, b relationship) relationship {
	switch {
	case a == disjoint || b == disjoint:
		return disjoint
	case a == equivalent:
		return b
	case b == equivalent:
		return a
	case a == b:
		return a
	default:
		return overlaps
	}
}

// conflictExample builds a request that both patterns match. Only meaningful
// when relate(p1, p2) is equivalent or overlaps, which guarantees the paths
// line up segment by segment until one of them reaches a {name...}.
func conflictExample(p1, p2 *pattern) string {
	method := p1.method
	switch {
	case p1.method == p2.method:
	case p1.method == "":
		method = p2.method
	case p2.method == "":
		method = p1.method
	default:
		method = "HEAD" // the only other overlap: HEAD with GET
	}
	if method == "" {
		method = "GET"
	}

	var segs []string
	s1, s2 := p1.segments, p2.segments
	for i := 0; i < len(s1) && i < len(s2); i++ {
		a, b := s1[i], s2[i]
		if a.kind == segRest || b.kind == segRest {
			// Whichever isn't the rest decides what comes next; a rest takes
			// anything. If both are rests, one more segment will do.
			other := s2[i:]
			if b.kind == segRest {
				other = s1[i:]
			}
			if other[0].kind == segRest {
				segs = append(segs, "x")
			} else {
				for _, sg := range other {
					segs = append(segs, exampleSegment(sg))
				}
			}
			break
		}
		if a.kind == segLiteral {
			segs = append(segs, a.text)
		} else {
			segs = append(segs, exampleSegment(b))
		}
	}
	return method + " /" + strings.Join(segs, "/")
}

// exampleSegment is a request segment that sg matches.
func exampleSegment(sg segment) string {
	if sg.kind == segLiteral {
		return sg.text
	}
	return "x" // any non-empty text satisfies {name} and {name...}
}
