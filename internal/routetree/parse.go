package routetree

import (
	"fmt"
)

// PatternError reports a malformed route pattern, with the byte offset of the
// problem so the message can point at it.
type PatternError struct {
	Pattern string // the pattern as registered
	Offset  int    // byte index in Pattern where the problem is
	Reason  string // short and human-readable
}

func (e *PatternError) Error() string {
	return fmt.Sprintf("invalid route pattern %q at offset %d: %s", e.Pattern, e.Offset, e.Reason)
}

// segKind says what a pattern segment matches.
type segKind uint8

const (
	segLiteral segKind = iota // exact text; "" is the trailing-slash segment
	segParam                  // {name}: one non-empty segment
	segRest                   // {name...}: the rest of the path (last only)
)

// segment is one parsed piece of a pattern's path.
type segment struct {
	kind segKind
	text string // the literal text, or the wildcard's name
}

// pattern is a parsed route pattern such as "GET /users/{id}".
type pattern struct {
	raw      string    // as registered
	method   string    // "" means any method
	segments []segment // never empty: "/" is one literal "" segment
}

// parsePattern parses and validates a route pattern. Grammar and rules:
// docs/design/02-router.md §5.2.
func parsePattern(s string) (*pattern, error) {
	return nil, errNotImplemented
}

// String rebuilds a canonical form of the pattern. A {$} ending is written as
// a trailing slash, which means the same thing.
func (p *pattern) String() string {
	return ""
}
