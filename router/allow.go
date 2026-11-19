package router

import (
	"slices"
	"strings"
)

// allowHeader builds the value of a 405 response's Allow header from the
// methods of the routes that match a request's path (routetree's Allowed).
// Rules: docs/tasks/02-allow-header.md.
func allowHeader(methods []string) string {
	// Create a copy so the original isn't modified.
	result := slices.Clone(methods)

	// If GET exists, HEAD should also be allowed.
	if slices.Contains(result, "GET") {
		result = append(result, "HEAD")
	}

	// Sort the methods alphabetically so duplicates are adjacent.
	slices.Sort(result)

	// Remove adjacent duplicates (assign the returned shortened slice).
	result = slices.Compact(result)

	// Join them into a single header value.
	return strings.Join(result, ", ")
}
