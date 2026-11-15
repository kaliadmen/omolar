// Package routetree is the data structure behind Omolar's router: a trie of
// path segments that stores any value V. It knows nothing about HTTP.
//
// See docs/design/02-router.md.
package routetree

import (
	"errors"
	"fmt"
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
// TODO(quinze): Task 1. Replace this stub with your implementation.
func splitPath(path string) ([]string, error) {
	return nil, errNotImplemented
}
