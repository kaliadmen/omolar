package router

// Tests for Task 2 (docs/tasks/02-allow-header.md). Make these pass by
// implementing allowHeader in allow.go. Don't change these tests; if you
// think one is wrong, raise it in review.
//
//   go test ./router -run AllowHeader -v

import (
	"slices"
	"testing"
)

func TestAllowHeader(t *testing.T) {
	cases := []struct {
		name    string
		methods []string
		want    string
	}{
		// Design §11.4, rows A1-A7.
		{"A1 GET adds HEAD", []string{"GET"}, "GET, HEAD"},
		{"A2 explicit HEAD not duplicated", []string{"GET", "HEAD"}, "GET, HEAD"},
		{"A2 explicit HEAD first", []string{"HEAD", "GET"}, "GET, HEAD"},
		{"A3 HEAD alone doesn't imply GET", []string{"HEAD"}, "HEAD"},
		{"A4 sorted", []string{"POST", "PUT", "DELETE"}, "DELETE, POST, PUT"},
		{"A5 custom methods", []string{"PURGE", "GET"}, "GET, HEAD, PURGE"},
		{"A6 OPTIONS only when registered", []string{"OPTIONS", "GET"}, "GET, HEAD, OPTIONS"},
		{"A7 union across leaves", []string{"POST", "GET"}, "GET, HEAD, POST"},
		// Duplicates: Allowed collects from every matching route, so the same
		// method can appear more than once.
		{"duplicates", []string{"GET", "POST", "GET", "POST", "GET"}, "GET, HEAD, POST"},
		{"duplicate HEAD", []string{"HEAD", "HEAD"}, "HEAD"},
		{"duplicate GET and HEAD", []string{"GET", "HEAD", "GET", "HEAD"}, "GET, HEAD"},
		// No GET, so no HEAD is added.
		{"no GET no HEAD", []string{"POST"}, "POST"},
		{"custom only", []string{"M-SEARCH", "PURGE"}, "M-SEARCH, PURGE"},
		// Byte order: upper-case letters sort by their ASCII codes, and "-"
		// (0x2D) sorts before any letter.
		{"byte order", []string{"PUT", "PATCH", "POST", "M-SEARCH", "MKCOL"}, "M-SEARCH, MKCOL, PATCH, POST, PUT"},
		// Empty input: nothing is allowed, so no header value at all.
		{"empty", []string{}, ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowHeader(tc.methods); got != tc.want {
				t.Errorf("allowHeader(%q) = %q, want %q", tc.methods, got, tc.want)
			}
		})
	}
}

func TestAllowHeader_DoesNotModifyInput(t *testing.T) {
	// The caller owns this slice. Sorting or de-duplicating it in place would
	// change their data behind their back.
	in := []string{"PUT", "GET", "POST", "GET"}
	before := slices.Clone(in)
	allowHeader(in)
	if !slices.Equal(in, before) {
		t.Errorf("allowHeader changed its input: %q -> %q", before, in)
	}
}

func TestAllowHeader_SameForAnyOrder(t *testing.T) {
	want := "DELETE, GET, HEAD, POST"
	for _, in := range [][]string{
		{"GET", "POST", "DELETE"},
		{"DELETE", "POST", "GET"},
		{"POST", "GET", "DELETE", "GET"},
		{"HEAD", "DELETE", "POST", "GET"},
	} {
		if got := allowHeader(in); got != want {
			t.Errorf("allowHeader(%q) = %q, want %q", in, got, want)
		}
	}
}
