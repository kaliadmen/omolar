// Package router is Omolar's HTTP router: a segment trie (internal/routetree)
// plus the web-specific parts. Design: docs/design/02-router.md.
package router

import (
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

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
//
// The zero value is pathBad on purpose. A normalizedPath{} that was never
// filled in, or a code path that forgets to set a status, rejects the request
// instead of letting it through. Make the default the safe choice.
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
	decoded   bool     // some segment contained a %-escape, so segs aren't substrings of the path
	reason    string   // why a path was refused, for logs and tests
}

func bad(reason string) normalizedPath { return normalizedPath{status: pathBad, reason: reason} }

// normalizePath validates and splits a request's escaped path (r.URL.EscapedPath()).
//
// The order of checks matters:
//
//  1. Limits first. They're O(1) and O(n) byte scans, so a huge hostile path
//     is refused before any decoding work.
//  2. One pass over the segments: decode each (only if it has a %), and
//     refuse anything unsafe. Unsafe beats non-canonical: there's no point
//     redirecting to a "cleaned" path that would be refused next.
//  3. Only then, if the path isn't canonical, report its canonical form.
//
// Decoded segments go into buf, so with a stack buffer a plain path costs no
// allocations; each segment with a % costs what url.PathUnescape needs.
func normalizePath(escaped string, lim limits, buf []string) normalizedPath {
	if escaped == "" || escaped[0] != '/' {
		return bad(`path must start with "/"`)
	}
	if len(escaped) > lim.maxPathLength {
		return normalizedPath{status: pathTooLong, reason: "path is too long"}
	}
	// Every "/" starts one segment, so this counts segments without splitting.
	if strings.Count(escaped, "/") > lim.maxSegments {
		return normalizedPath{status: pathTooLong, reason: "path has too many segments"}
	}

	segs := buf[:0]
	canonical := true
	decoded := false
	start := 1 // the current segment begins just after a "/"
	for {
		end := len(escaped)
		last := true
		if i := strings.IndexByte(escaped[start:], '/'); i >= 0 {
			end, last = start+i, false
		}
		raw := escaped[start:end]

		// Empty, "." and ".." segments are what cleaning removes. An empty
		// LAST segment is the trailing slash, which is meaningful and kept.
		if (raw == "" && !last) || raw == "." || raw == ".." {
			canonical = false
		}

		seg, reason := decodeSegment(raw)
		if reason != "" {
			return bad(reason)
		}
		segs = append(segs, seg)
		if len(seg) != len(raw) {
			decoded = true // decoding always shortens a segment ("%41" -> "A")
		}

		if last {
			break
		}
		start = end + 1
	}

	if !canonical {
		return normalizedPath{status: pathNotCanonical, canonical: cleanPath(escaped)}
	}
	return normalizedPath{status: pathOK, segs: segs, decoded: decoded}
}

// decodeSegment percent-decodes one escaped segment and checks the result.
// reason is "" when the segment is safe.
func decodeSegment(raw string) (seg, reason string) {
	seg = raw
	encoded := strings.IndexByte(raw, '%') >= 0
	if encoded {
		var err error
		// PathUnescape, not QueryUnescape: in a path, "+" is a plus sign.
		if seg, err = url.PathUnescape(raw); err != nil {
			return "", "malformed percent-escape"
		}
		// These two are only dangerous when they were hidden by encoding:
		// a raw "/" can't be inside a segment, and raw "." / ".." are
		// handled by cleaning. Encoded, they'd slip past cleaning (§3.3).
		if strings.IndexByte(seg, '/') >= 0 {
			return "", "encoded slash (%2F) in a path segment"
		}
		if seg == "." || seg == ".." {
			return "", "encoded dot segment (%2E)"
		}
	}
	for i := 0; i < len(seg); i++ {
		switch c := seg[i]; {
		case c < 0x20 || c == 0x7f:
			// NUL truncates strings in C code; CR/LF can forge log lines or
			// headers; the rest have no business in a URL.
			return "", "control character in a path segment"
		case c == '\\':
			// Omolar policy: browsers and Windows treat "\" as "/", so it has
			// no legitimate use in a route and is a common disguise. Raw or
			// encoded (%5C), it's refused. Defence in depth only: this does
			// not make file access safe (design §6.8).
			return "", "backslash in a path segment"
		}
	}
	// One valid byte sequence per character: no overlong forms like %C0%AF
	// (a disguised "/") and no lone surrogates.
	if !utf8.ValidString(seg) {
		return "", "invalid UTF-8 in a path segment"
	}
	return seg, ""
}

// cleanPath returns the canonical form of an escaped path: path.Clean, but
// keeping a trailing slash, since "/a/" and "/a" are different routes.
// It works on the escaped form, so %2F etc. are left exactly as they were.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	np := path.Clean(p)
	if strings.HasSuffix(p, "/") && np != "/" {
		np += "/"
	}
	return np
}

// redirectTarget builds a Location header value for a redirect to canonical.
//
// Security: the target is built only from the path and query the client sent,
// never from Host, and it must never be read as another site:
//   - "//evil.com" is a protocol-relative URL: browsers go to evil.com.
//   - Browsers also treat "\" like "/", so "/\evil.com" does the same.
//
// cleanPath already guarantees a single leading "/". The checks here make the
// function safe on its own, whatever it's given.
func redirectTarget(canonical, rawQuery string) string {
	target := "/" + strings.TrimLeft(canonical, "/")
	if strings.IndexByte(target, '\\') >= 0 {
		target = strings.ReplaceAll(target, `\`, "%5C")
	}
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	return target
}

// TRY: in normalize_test.go, add {"/a/%2e%2e/b", ...} to the canonicalization
// table and run it. It fails, because that path is refused (400) before
// cleaning. Why would redirecting it be worse than refusing it?
