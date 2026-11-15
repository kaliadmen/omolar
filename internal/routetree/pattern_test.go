package routetree

// Tests for Task 1 (docs/tasks/01-split-path.md). Make these pass by
// implementing splitPath in pattern.go. Don't change these tests; if you think
// one is wrong, raise it in review.
//
//   go test ./internal/routetree -run SplitPath -v

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

// R2, R3, R4, R6: valid paths and their segments.
func TestSplitPath(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"/", []string{""}},
		{"/users", []string{"users"}},
		{"/users/", []string{"users", ""}},
		{"/users/{id}", []string{"users", "{id}"}},
		{"/users/{id}/posts/", []string{"users", "{id}", "posts", ""}},
		{"/a/b/c", []string{"a", "b", "c"}},
		{"/café/{x...}", []string{"café", "{x...}"}},
		{"/a b/%41", []string{"a b", "%41"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := splitPath(tc.in)
			if err != nil {
				t.Fatalf("splitPath(%q) returned error: %v", tc.in, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("splitPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// R1, R5: invalid paths, with the exact offset of the problem.
func TestSplitPath_Errors(t *testing.T) {
	cases := []struct {
		in         string
		wantOffset int
	}{
		{"", 0},
		{"users", 0},
		{"//", 1},
		{"/a//b", 3},
		{"/a//", 3},
		{"/a/b///c", 5}, // the FIRST "//" pair
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := splitPath(tc.in)
			if err == nil {
				t.Fatalf("splitPath(%q) = %q, want an error", tc.in, got)
			}
			if got != nil {
				t.Errorf("splitPath(%q) returned segments %q alongside an error; want nil", tc.in, got)
			}
			var pe *pathError
			if !errors.As(err, &pe) {
				t.Fatalf("splitPath(%q) error is %T (%v), want *pathError", tc.in, err, err)
			}
			if pe.Offset != tc.wantOffset {
				t.Errorf("splitPath(%q) Offset = %d, want %d", tc.in, pe.Offset, tc.wantOffset)
			}
			if pe.Reason == "" {
				t.Errorf("splitPath(%q) Reason is empty", tc.in)
			}
		})
	}
}

// R7: segments are substrings of the input, not copies.
func TestSplitPath_SegmentsShareInputMemory(t *testing.T) {
	in := "/users/{id}/posts/"
	got, err := splitPath(in)
	if err != nil {
		t.Fatalf("splitPath(%q) returned error: %v", in, err)
	}
	if len(got) == 0 {
		t.Fatalf("splitPath(%q) returned no segments", in)
	}

	// GO CONCEPT: unsafe.StringData returns a pointer to a string's bytes,
	// like a char* in C. Comparing addresses tells us whether a segment lives
	// inside the input's memory. (unsafe is fine in a test that inspects
	// memory; production code here never needs it.)
	start := uintptr(unsafe.Pointer(unsafe.StringData(in)))
	end := start + uintptr(len(in))
	for i, seg := range got {
		if seg == "" {
			continue // an empty string has no bytes to point at
		}
		p := uintptr(unsafe.Pointer(unsafe.StringData(seg)))
		if p < start || p+uintptr(len(seg)) > end {
			t.Errorf("segment %d (%q) was copied; it should be a slice of the input", i, seg)
		}
	}
}

// R8: one allocation for valid input (the result slice, sized up front).
func TestSplitPath_OneAllocation(t *testing.T) {
	for _, in := range []string{"/", "/users", "/users/{id}/posts/", "/a/b/c/d/e/f/g/h/i/j"} {
		if _, err := splitPath(in); err != nil {
			t.Fatalf("splitPath(%q) returned error: %v", in, err)
		}
		allocs := testing.AllocsPerRun(100, func() {
			splitPath(in) //nolint:errcheck // only counting allocations
		})
		if allocs != 1 {
			t.Errorf("splitPath(%q) made %.0f allocations, want exactly 1", in, allocs)
		}
	}
}

// R9 and differential testing: never panics, and agrees with a reference.
//
// The reference: a path is valid when it's non-empty, starts with "/", and has
// no "//" anywhere. For valid paths, strings.Split(path[1:], "/") gives the
// right segments. For "//" the error Offset is the second slash of the first
// pair. (The implementation itself must not use strings.Split, R10.)
//
//	go test ./internal/routetree -run '^$' -fuzz FuzzSplitPath -fuzztime 30s
func FuzzSplitPath(f *testing.F) {
	for _, seed := range []string{
		"/", "/users", "/users/", "/users/{id}", "/users/{id}/posts/", "/a/b/c",
		"/café/{x...}", "/a b/%41", "", "users", "//", "/a//b", "/a//", "/a/b///c",
		"///", "/a/", "a/b", "/\x00", "/\xff/", "/%2F/",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got, err := splitPath(in) // a panic here fails the test (R9)

		startsWithSlash := in != "" && in[0] == '/'
		valid := startsWithSlash && !strings.Contains(in, "//")

		if valid {
			if err != nil {
				t.Fatalf("splitPath(%q) returned error %v, want success", in, err)
			}
			want := strings.Split(in[1:], "/")
			if !slices.Equal(got, want) {
				t.Fatalf("splitPath(%q) = %q, want %q", in, got, want)
			}
			return
		}

		if err == nil {
			t.Fatalf("splitPath(%q) = %q, want an error", in, got)
		}
		if got != nil {
			t.Fatalf("splitPath(%q) returned segments alongside an error", in)
		}
		var pe *pathError
		if !errors.As(err, &pe) {
			t.Fatalf("splitPath(%q) error is %T, want *pathError", in, err)
		}
		wantOffset := 0
		if startsWithSlash {
			wantOffset = strings.Index(in, "//") + 1
		}
		if pe.Offset != wantOffset {
			t.Fatalf("splitPath(%q) Offset = %d, want %d", in, pe.Offset, wantOffset)
		}
	})
}
