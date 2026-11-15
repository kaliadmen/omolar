package routetree

// Pattern parsing: docs/design/02-router.md §5.2 and test plan §11.1.

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func lit(s string) segment   { return segment{kind: segLiteral, text: s} }
func param(s string) segment { return segment{kind: segParam, text: s} }
func rest(s string) segment  { return segment{kind: segRest, text: s} }

func TestParsePattern(t *testing.T) {
	cases := []struct {
		in     string
		method string
		segs   []segment
	}{
		{"/", "", []segment{lit("")}},
		{"/users", "", []segment{lit("users")}},
		{"/users/", "", []segment{lit("users"), lit("")}},
		{"GET /users/{id}", "GET", []segment{lit("users"), param("id")}},
		{"POST /users/{id}/posts/", "POST", []segment{lit("users"), param("id"), lit("posts"), lit("")}},
		{"/static/{path...}", "", []segment{lit("static"), rest("path")}},
		{"GET /{p...}", "GET", []segment{rest("p")}},
		// {$} means "exactly a trailing slash": the same as ending in "/".
		{"/{$}", "", []segment{lit("")}},
		{"GET /a/{$}", "GET", []segment{lit("a"), lit("")}},
		{"PURGE /cache", "PURGE", []segment{lit("cache")}},
		{"M-SEARCH /x", "M-SEARCH", []segment{lit("x")}},
		{"/café", "", []segment{lit("café")}},
		{"/a b", "", []segment{lit("a b")}}, // no method: a space is just text
		{"GET /a b", "GET", []segment{lit("a b")}},
		{"/{_id2}", "", []segment{param("_id2")}},
		{"/a/{x}/b/{y}", "", []segment{lit("a"), param("x"), lit("b"), param("y")}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			p, err := parsePattern(tc.in)
			if err != nil {
				t.Fatalf("parsePattern(%q) error: %v", tc.in, err)
			}
			if p == nil {
				t.Fatal("nil pattern")
			}
			if p.raw != tc.in {
				t.Errorf("raw = %q, want %q", p.raw, tc.in)
			}
			if p.method != tc.method {
				t.Errorf("method = %q, want %q", p.method, tc.method)
			}
			if !slices.Equal(p.segments, tc.segs) {
				t.Errorf("segments = %+v, want %+v", p.segments, tc.segs)
			}
		})
	}
}

func TestParsePattern_Errors(t *testing.T) {
	cases := []struct {
		in     string
		offset int
		reason string // fragment; "" = only check the offset (reason comes from splitPath)
	}{
		{"", 0, "empty"},
		{"GET", 0, "must start with"},
		{" /x", 0, "missing method"},
		{"get /x", 0, "upper case"},
		{"GeT /x", 1, "upper case"},
		{"G@T /x", 1, "invalid character"},
		{"GET  /x", 4, ""}, // two spaces: the path " /x" doesn't start with "/"
		{"GET x", 4, ""},
		{"GET ", 4, ""},
		{"/a//b", 3, ""},
		{"GET /a//b", 7, ""},
		{"/a/{id", 3, "missing closing"},
		{"/a/id}", 5, "whole segment"},
		{"/file{id}", 5, "whole segment"},
		{"/{x}{y}", 3, "whole segment"},
		{"/{}", 1, "missing wildcard name"},
		{"/{...}", 1, "missing wildcard name"},
		{"/{1x}", 1, "invalid wildcard name"},
		{"/{a-b}", 1, "invalid wildcard name"},
		{"/{id}/{id}", 6, "duplicate wildcard name"},
		{"/{id}/{id...}", 6, "duplicate wildcard name"},
		{"/{p...}/x", 1, "last segment"},
		{"GET /{x...}/", 5, "last segment"},
		{"/{$}/x", 1, "last segment"},
		{"/a/%41", 3, "%"},
		{"/a/./b", 3, "dot segment"},
		{"/..", 1, "dot segment"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			p, err := parsePattern(tc.in)
			if err == nil {
				t.Fatalf("parsePattern(%q) = %+v, want an error", tc.in, p)
			}
			if p != nil {
				t.Errorf("returned a pattern alongside an error")
			}
			var pe *PatternError
			if !errors.As(err, &pe) {
				t.Fatalf("error is %T (%v), want *PatternError", err, err)
			}
			if pe.Pattern != tc.in {
				t.Errorf("Pattern = %q, want %q", pe.Pattern, tc.in)
			}
			if pe.Offset != tc.offset {
				t.Errorf("Offset = %d, want %d (%s)", pe.Offset, tc.offset, pe.Reason)
			}
			if pe.Reason == "" {
				t.Error("Reason is empty")
			}
			if tc.reason != "" && !strings.Contains(pe.Reason, tc.reason) {
				t.Errorf("Reason %q should contain %q", pe.Reason, tc.reason)
			}
			if !strings.Contains(err.Error(), tc.in) {
				t.Errorf("Error() %q should quote the pattern", err.Error())
			}
		})
	}
}

func TestPatternString_IsCanonical(t *testing.T) {
	cases := map[string]string{
		"/":                      "/",
		"/{$}":                   "/",
		"GET /a/{$}":             "GET /a/",
		"GET /users/{id}/posts/": "GET /users/{id}/posts/",
		"/static/{path...}":      "/static/{path...}",
		"PURGE /cache":           "PURGE /cache",
		"DELETE /a/{x}/b/{y...}": "DELETE /a/{x}/b/{y...}",
	}
	for in, want := range cases {
		p, err := parsePattern(in)
		if err != nil {
			t.Fatalf("parsePattern(%q): %v", in, err)
		}
		if got := p.String(); got != want {
			t.Errorf("parsePattern(%q).String() = %q, want %q", in, got, want)
		}
	}
}

// Never panics; anything that parses survives a round trip through String().
//
//	go test ./internal/routetree -run '^$' -fuzz FuzzParsePattern -fuzztime 30s
func FuzzParsePattern(f *testing.F) {
	for _, s := range []string{
		"/", "GET /users/{id}", "/static/{p...}", "/{$}", "POST /a/{x}/b/", "get /x",
		"/{x}{y}", "/{id}/{id}", "/a/%41", "/..", "GET  /x", "", "{", "/{", "/{...}", "/{$",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		p, err := parsePattern(in)
		if err != nil {
			var pe *PatternError
			if !errors.As(err, &pe) {
				t.Fatalf("parsePattern(%q) error %T is not *PatternError", in, err)
			}
			if pe.Offset < 0 || pe.Offset > len(in) {
				t.Fatalf("parsePattern(%q) Offset %d outside the pattern", in, pe.Offset)
			}
			return
		}
		again, err := parsePattern(p.String())
		if err != nil {
			t.Fatalf("%q parsed, but its String() %q doesn't: %v", in, p.String(), err)
		}
		if again.method != p.method || !slices.Equal(again.segments, p.segments) {
			t.Fatalf("round trip changed %q: %+v -> %+v", in, p, again)
		}
	})
}
