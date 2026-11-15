package routetree

import (
	"errors"
	"fmt"
	"strings"
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
//
// GO CONCEPT: iota. Inside a const block, iota counts 0, 1, 2... so each
// constant gets the next number without writing it. It's Go's enum.
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
//
// It runs once per route at startup, so it favours clear checks and precise
// error offsets over speed.
func parsePattern(s string) (*pattern, error) {
	fail := func(offset int, reason string) (*pattern, error) {
		return nil, &PatternError{Pattern: s, Offset: offset, Reason: reason}
	}

	if s == "" {
		return fail(0, "pattern is empty")
	}

	p := &pattern{raw: s}

	// An optional "METHOD " prefix. Without one, the pattern starts with "/",
	// so a space later in the path is just text ("/a b").
	pathStart := 0
	if s[0] != '/' {
		sp := strings.IndexByte(s, ' ')
		if sp < 0 {
			return fail(0, `must start with "/" or with a method and a space ("GET /x")`)
		}
		if sp == 0 {
			return fail(0, "missing method before the space")
		}
		if off, reason := checkMethod(s[:sp]); reason != "" {
			return fail(off, reason)
		}
		p.method = s[:sp]
		pathStart = sp + 1
	}

	segs, err := splitPath(s[pathStart:])
	if err != nil {
		// splitPath's offsets count from the start of the path; shift them so
		// they count from the start of the whole pattern.
		var pe *pathError
		if errors.As(err, &pe) {
			return fail(pathStart+pe.Offset, pe.Reason)
		}
		return nil, err
	}

	p.segments = make([]segment, 0, len(segs))
	off := pathStart + 1 // where the current segment starts in s
	for i, seg := range segs {
		segOff := off
		off += len(seg) + 1
		last := i == len(segs)-1

		sg, errOff, reason := parseSegment(seg)
		if reason != "" {
			return fail(segOff+errOff, reason)
		}

		// {name...} and {$} only make sense at the very end.
		isEnd := sg.kind == segRest || seg == "{$}"
		if isEnd && !last {
			return fail(segOff, "{name...} and {$} must be the last segment")
		}

		if sg.kind != segLiteral {
			for _, prev := range p.segments {
				if prev.kind != segLiteral && prev.text == sg.text {
					return fail(segOff, fmt.Sprintf("duplicate wildcard name %q", sg.text))
				}
			}
		}
		p.segments = append(p.segments, sg)
	}
	return p, nil
}

// parseSegment classifies one segment. On failure it returns the offset of
// the problem within seg and a reason; reason is "" on success.
func parseSegment(seg string) (sg segment, errOff int, reason string) {
	if !strings.HasPrefix(seg, "{") {
		// A literal. Braces may only appear as a whole {wildcard} segment.
		if i := strings.IndexAny(seg, "{}"); i >= 0 {
			return segment{}, i, "a {wildcard} must be a whole segment"
		}
		if i := strings.IndexByte(seg, '%'); i >= 0 {
			// Ambiguous: is "%41" the three characters or the letter "A"?
			// Patterns are written decoded ("/café", not "/caf%C3%A9").
			return segment{}, i, `"%" is not allowed; write the decoded text`
		}
		if seg == "." || seg == ".." {
			return segment{}, 0, "dot segment can never match (request paths are cleaned)"
		}
		return segment{kind: segLiteral, text: seg}, 0, ""
	}

	if !strings.HasSuffix(seg, "}") || len(seg) < 2 {
		return segment{}, 0, `missing closing "}"`
	}
	inner := seg[1 : len(seg)-1]
	if i := strings.IndexAny(inner, "{}"); i >= 0 {
		return segment{}, 1 + i, "a {wildcard} must be a whole segment"
	}

	if inner == "$" {
		// {$} is "exactly a trailing slash": the same as a literal "".
		return segment{kind: segLiteral, text: ""}, 0, ""
	}

	kind := segParam
	name := inner
	if n, ok := strings.CutSuffix(inner, "..."); ok {
		kind, name = segRest, n
	}
	if name == "" {
		return segment{}, 0, "missing wildcard name"
	}
	if !isIdentifier(name) {
		return segment{}, 0, fmt.Sprintf("invalid wildcard name %q (use letters, digits and _, not starting with a digit)", name)
	}
	return segment{kind: kind, text: name}, 0, ""
}

// checkMethod validates an HTTP method in a pattern: upper-case letters,
// digits, '-' and '_', starting with a letter. Lower case is rejected because
// methods are case-sensitive, so "get /x" could never match a real request.
func checkMethod(m string) (offset int, reason string) {
	for i := 0; i < len(m); i++ {
		c := m[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '_'):
		case c >= 'a' && c <= 'z':
			return i, "method must be upper case (methods are case-sensitive)"
		default:
			return i, fmt.Sprintf("invalid character %q in method", c)
		}
	}
	return 0, ""
}

// isIdentifier reports whether s is an ASCII Go-style identifier.
func isIdentifier(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		digit := c >= '0' && c <= '9'
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return s != ""
}

// String rebuilds a canonical form of the pattern. A {$} ending is written as
// a trailing slash, which means the same thing.
func (p *pattern) String() string {
	var b strings.Builder
	if p.method != "" {
		b.WriteString(p.method)
		b.WriteByte(' ')
	}
	for _, sg := range p.segments {
		b.WriteByte('/')
		switch sg.kind {
		case segLiteral:
			b.WriteString(sg.text)
		case segParam:
			b.WriteString("{" + sg.text + "}")
		case segRest:
			b.WriteString("{" + sg.text + "...}")
		}
	}
	return b.String()
}
