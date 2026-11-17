// Package router is Omolar's HTTP router: a segment trie (internal/routetree)
// plus the web-specific parts. Design: docs/design/02-router.md.
package router

// Limits on request paths (design §6.7). They bound the work one request can
// cost, including how deep the matching recursion can go.
const (
	defaultMaxPathLength = 8192 // bytes of the escaped path
	defaultMaxSegments   = 128
)

type limits struct {
	maxPathLength int
	maxSegments   int
}

// pathStatus is the outcome of normalizePath.
type pathStatus uint8

const (
	pathBad          pathStatus = iota // 400 Bad Request
	pathTooLong                        // 414 URI Too Long
	pathNotCanonical                   // 307 to canonical (GET/HEAD) or 400 (others)
	pathOK                             // segs is ready for matching
)

// normalizedPath is what phase 1 hands to the matcher (design §5.4).
type normalizedPath struct {
	status    pathStatus
	segs      []string // decoded segments, valid only when status == pathOK
	canonical string   // the cleaned escaped path, set when status == pathNotCanonical
	reason    string   // why a path was refused, for logs and tests
}

// normalizePath validates and splits a request's escaped path (r.URL.EscapedPath()).
func normalizePath(escaped string, lim limits, buf []string) normalizedPath {
	return normalizedPath{reason: "not implemented"}
}

// cleanPath returns the canonical form of an escaped path.
func cleanPath(p string) string {
	return ""
}

// redirectTarget builds a Location header value for a redirect to canonical.
func redirectTarget(canonical, rawQuery string) string {
	return ""
}
