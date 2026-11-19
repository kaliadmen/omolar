// Package routetree is the data structure behind Omolar's router: a trie of
// path segments that stores any value V. It knows nothing about HTTP.
//
// See docs/design/02-router.md.
package routetree

import (
	"errors"
	"fmt"
	"strings"
)

// errNotImplemented is returned by stubs during the "red" step of TDD.
var errNotImplemented = errors.New("not implemented")

// pathError reports a problem in a pattern's path.
type pathError struct {
	Offset int    // byte index in the input where the problem is
	Reason string // short and human-readable
}

func (e *pathError) Error() string {
	return fmt.Sprintf("invalid path at offset %d: %s", e.Offset, e.Reason)
}

// splitPath splits the path part of a route pattern into its segments.
//
//	"/"       -> [""]
//	"/a/b"    -> ["a", "b"]
//	"/a/b/"   -> ["a", "b", ""]   (a trailing slash is a final empty segment)
//
// The input must start with "/" and must not contain an empty segment except
// at the end. Requirements R1-R10: docs/tasks/01-split-path.md.
//
// splitPath splits the path part of a route pattern into its segments.
func splitPath(path string) ([]string, error) {
	if len(path) == 0 || path[0] != '/' {
		return nil, &pathError{
			Offset: 0,
			Reason: "path must start with '/'",
		}
	}

	count := strings.Count(path, "/")
	segments := make([]string, 0, count)

	start := 1

	for {
		// Find the next slash relative to the current 'start' position
		idx := strings.IndexByte(path[start:], '/')

		// If no slash remains, append the rest of the string and finish the loop
		if idx == -1 {
			segments = append(segments, path[start:])
			break
		}

		// Calculate absolute index of the found slash
		end := start + idx

		// Detect an empty intermediate segment.
		// If start == end, it means we found two slashes back-to-back ("//").
		// 'end' correctly represents the byte index of the second slash.
		if start == end {
			return nil, &pathError{
				Offset: end,
				Reason: "path contains empty intermediate segment",
			}
		}

		// Append the substring to segments
		segments = append(segments, path[start:end])

		// Advance start past the slash we just found
		start = end + 1
	}

	return segments, nil
}
