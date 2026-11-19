package router

// Phase 1, path normalization: design §5.4, §6.6, §6.7 and test plan §11.7–11.8.
// These tests call normalizePath directly (level L1/L2). The same cases go
// through ServeHTTP and raw TCP once dispatch exists (step 7 onwards).

import (
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

var defaultLimits = limits{maxPathLength: defaultMaxPathLength, maxSegments: defaultMaxSegments}

func normalize(p string) normalizedPath {
	var buf [32]string
	return normalizePath(p, defaultLimits, buf[:0])
}

func TestNormalize_OK(t *testing.T) {
	cases := []struct {
		in   string
		segs []string
	}{
		{"/", []string{""}},
		{"/a", []string{"a"}},
		{"/a/", []string{"a", ""}},
		{"/users/42/posts", []string{"users", "42", "posts"}},
		{"/caf%C3%A9", []string{"café"}},
		{"/café", []string{"café"}}, // already-decoded UTF-8
		{"/a%20b", []string{"a b"}},
		{"/%41/b/", []string{"A", "b", ""}},
		{"/%F0%9F%98%80", []string{"😀"}},
		{"/a%2Cb", []string{"a,b"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalize(tc.in)
			if got.status != pathOK {
				t.Fatalf("status = %v (%s), want ok", got.status, got.reason)
			}
			if !slices.Equal(got.segs, tc.segs) {
				t.Errorf("segs = %q, want %q", got.segs, tc.segs)
			}
		})
	}
}

// Every refusal from design §6.7, each a 400.
func TestNormalize_RejectsUnsafeSegments(t *testing.T) {
	cases := []struct {
		in, reason string
	}{
		// Encoded slashes: the §3.3 traversal bypass and its variants.
		{"/static/a%2F..%2F..%2Fetc%2Fpasswd", "encoded slash"},
		{"/users/a%2Fb", "encoded slash"},
		{"/users/a%2fb", "encoded slash"},
		{"/%2F", "encoded slash"},
		// Encoded dot segments.
		{"/users/%2e%2e", "dot segment"},
		{"/users/%2E%2E", "dot segment"},
		{"/users/.%2e", "dot segment"},
		{"/users/%2e.", "dot segment"},
		{"/users/%2e", "dot segment"},
		{"/a/%2e%2e/b", "dot segment"},
		// Control characters, including NUL and CR/LF (header/log injection).
		{"/a%00b", "control character"},
		{"/a%0d%0aSet-Cookie:x", "control character"},
		{"/a%09b", "control character"},
		{"/a%1Bb", "control character"},
		{"/a%7Fb", "control character"},
		{"/a\x01b", "control character"}, // raw, in case net/http ever lets one through
		// Invalid UTF-8 (Q7), including the overlong encoding of "/".
		{"/%FF", "UTF-8"},
		{"/%C0%AF", "UTF-8"},
		{"/%ED%A0%80", "UTF-8"}, // a UTF-16 surrogate: not valid UTF-8
		{"/a\xffb", "UTF-8"},
		// Backslashes, raw or encoded: an intentionally strict Omolar policy
		// (review decision). Browsers and Windows treat "\" as "/", so it has
		// no legitimate use in a route. This is defence in depth only; file
		// access still has to be safe on its own (§6.8).
		{"/files/a%5Cb", "backslash"},
		{"/files/a%5cb", "backslash"},
		{"/%5C", "backslash"},
		{`/files/a\b`, "backslash"},
		{`/\evil.com`, "backslash"},
		{`/a/..\..\etc`, "backslash"},
		// Malformed percent-encoding.
		{"/%zz", "escape"},
		{"/%", "escape"},
		{"/a%4", "escape"},
		// Not a path at all.
		{"", "start with"},
		{"*", "start with"}, // OPTIONS * HTTP/1.1
		{"a/b", "start with"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalize(tc.in)
			if got.status != pathBad {
				t.Fatalf("status = %v, want bad (400)", got.status)
			}
			if !strings.Contains(got.reason, tc.reason) {
				t.Errorf("reason %q should mention %q", got.reason, tc.reason)
			}
		})
	}
}

func TestNormalize_Limits(t *testing.T) {
	t.Run("path length", func(t *testing.T) {
		atLimit := "/" + strings.Repeat("a", defaultMaxPathLength-1)
		if got := normalize(atLimit); got.status != pathOK {
			t.Errorf("a path of exactly %d bytes: status %v (%s), want ok", len(atLimit), got.status, got.reason)
		}
		if got := normalize(atLimit + "a"); got.status != pathTooLong {
			t.Errorf("a path of %d bytes: status %v, want too long (414)", len(atLimit)+1, got.status)
		}
	})
	t.Run("segment count", func(t *testing.T) {
		atLimit := strings.Repeat("/a", defaultMaxSegments)
		if got := normalize(atLimit); got.status != pathOK {
			t.Errorf("%d segments: status %v (%s), want ok", defaultMaxSegments, got.status, got.reason)
		}
		if got := normalize(atLimit + "/a"); got.status != pathTooLong {
			t.Errorf("%d segments: status %v, want too long (414)", defaultMaxSegments+1, got.status)
		}
		// A trailing slash counts as a segment too: it's one more level to match.
		if got := normalize(atLimit + "/"); got.status != pathTooLong {
			t.Errorf("%d segments + trailing slash: status %v, want too long", defaultMaxSegments, got.status)
		}
	})
	t.Run("limits are checked before any other work", func(t *testing.T) {
		// Over-long AND full of bad escapes: the cheap length check wins, so
		// a huge hostile path is refused without decoding any of it.
		huge := "/" + strings.Repeat("%zz", defaultMaxPathLength)
		if got := normalize(huge); got.status != pathTooLong {
			t.Errorf("status %v, want too long", got.status)
		}
	})
	t.Run("custom limits", func(t *testing.T) {
		var buf [32]string
		lim := limits{maxPathLength: 10, maxSegments: 2}
		if got := normalizePath("/a/b", lim, buf[:0]); got.status != pathOK {
			t.Errorf("/a/b under custom limits: %v (%s)", got.status, got.reason)
		}
		if got := normalizePath("/a/b/c", lim, buf[:0]); got.status != pathTooLong {
			t.Errorf("3 segments with maxSegments 2: %v, want too long", got.status)
		}
		if got := normalizePath("/abcdefghij", lim, buf[:0]); got.status != pathTooLong {
			t.Errorf("11 bytes with maxPathLength 10: %v, want too long", got.status)
		}
	})
}

func TestNormalize_Canonicalization(t *testing.T) {
	cases := []struct{ in, canonical string }{
		{"//a", "/a"},
		{"/a//b", "/a/b"},
		{"/a/./b", "/a/b"},
		{"/a/../b", "/b"},
		{"/a/b/..", "/a"},
		{"/a/b/../", "/a/"},
		{"/a/.", "/a"},
		{"/a/./", "/a/"},
		{"/..", "/"},
		{"/../../etc/passwd", "/etc/passwd"},
		{"//evil.com", "/evil.com"}, // never a protocol-relative "//host"
		{"///evil.com/x", "/evil.com/x"},
		{"/%41/../b", "/b"},     // cleaning works on the escaped form...
		{"/x/%41//", "/x/%41/"}, // ...and keeps escapes as they were
		{"/a//", "/a/"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalize(tc.in)
			if got.status != pathNotCanonical {
				t.Fatalf("status = %v (%s), want not canonical", got.status, got.reason)
			}
			if got.canonical != tc.canonical {
				t.Errorf("canonical = %q, want %q", got.canonical, tc.canonical)
			}
			// The canonical form must itself be accepted, or a GET would be
			// redirected forever.
			if again := normalize(got.canonical); again.status != pathOK {
				t.Errorf("canonical %q is not accepted itself: %v (%s)", got.canonical, again.status, again.reason)
			}
		})
	}
}

func TestNormalize_ValidationBeforeCanonicalization(t *testing.T) {
	// A path that is both non-canonical and unsafe is refused (400), not
	// redirected to a "cleaned" version that would only be refused next.
	for _, in := range []string{"/a//b%2F", "/a/../%2e%2e", "/./%00", "//%FF", `/a/..\x`, "//%5Cevil.com"} {
		if got := normalize(in); got.status != pathBad {
			t.Errorf("normalize(%q) = %v, want bad", in, got.status)
		}
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]string{
		"/":          "/",
		"/a":         "/a",
		"/a/":        "/a/",
		"//":         "/",
		"/a/b/../c/": "/a/c/",
		"/./":        "/",
		"/a/..":      "/",
	}
	for in, want := range cases {
		if got := cleanPath(in); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedirectTarget(t *testing.T) {
	cases := []struct{ canonical, query, want string }{
		{"/a", "", "/a"},
		{"/a/", "x=1&y=2", "/a/?x=1&y=2"},
		{"/a%2Fb", "q=%0d%0a", "/a%2Fb?q=%0d%0a"}, // escapes are passed through untouched
		// Some browsers treat "\" as "/", so "/\evil.com" acts like "//evil.com".
		{`/\evil.com`, "", "/%5Cevil.com"},
		{`/a\b\c`, "", "/a%5Cb%5Cc"},
		// Defence in depth: normalizePath never produces these, but a
		// Location must never start with "//" whatever it's given.
		{"//evil.com", "", "/evil.com"},
		{"///evil.com", "", "/evil.com"},
		{"", "", "/"},
	}
	for _, tc := range cases {
		got := redirectTarget(tc.canonical, tc.query)
		if got != tc.want {
			t.Errorf("redirectTarget(%q, %q) = %q, want %q", tc.canonical, tc.query, got, tc.want)
		}
		if strings.HasPrefix(got, "//") || strings.HasPrefix(got, `/\`) {
			t.Errorf("redirectTarget(%q, %q) = %q is an open redirect", tc.canonical, tc.query, got)
		}
	}
}

func TestNormalize_PlainPathAllocatesNothing(t *testing.T) {
	var buf [32]string
	allocs := testing.AllocsPerRun(200, func() {
		normalizePath("/api/v1/users/42/posts/", defaultLimits, buf[:0])
	})
	if allocs != 0 {
		t.Errorf("normalizePath on a plain path made %.1f allocations, want 0", allocs)
	}
}

func TestNormalize_EscapedPathAllocatesOnlyForDecoding(t *testing.T) {
	// Budget (design §8): "+ whatever url.PathUnescape needs", nothing more.
	// Measure that reference in the same test rather than hard-coding it.
	const p = "/caf%C3%A9/a%20b/plain"
	var buf [32]string
	got := testing.AllocsPerRun(200, func() {
		normalizePath(p, defaultLimits, buf[:0])
	})
	want := testing.AllocsPerRun(200, func() {
		url.PathUnescape("caf%C3%A9") //nolint:errcheck
		url.PathUnescape("a%20b")     //nolint:errcheck
	})
	if got != want {
		t.Errorf("normalizePath made %.1f allocations; decoding alone needs %.1f", got, want)
	}
}

// Never panics, and its verdicts are consistent with each other and with
// cleanPath:
//
//   - ok:            every segment is safe, and the path is already canonical
//
//   - not canonical: it passed validation, canonical == cleanPath(input), and
//     the canonical form is ok
//
//   - bad / too long: fine, as long as it didn't panic
//
//     go test ./router -run '^$' -fuzz FuzzNormalize -fuzztime 30s
func FuzzNormalize(f *testing.F) {
	for _, s := range []string{
		"/", "/a/b/", "//evil.com", "/a/../b", "/a/./b/", "/%2e%2e", "/a%2Fb", "/%00",
		"/%FF", "/caf%C3%A9", "/%zz", "*", "", "/a//b//", "/" + strings.Repeat("a/", 200),
		`/\evil.com`, "/static/a%2F..%2F..%2Fetc%2Fpasswd",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		var buf [32]string
		got := normalizePath(in, defaultLimits, buf[:0])
		switch got.status {
		case pathOK:
			if cleanPath(in) != in {
				t.Fatalf("%q accepted as canonical, but cleanPath gives %q", in, cleanPath(in))
			}
			if len(got.segs) == 0 || len(got.segs) > defaultMaxSegments {
				t.Fatalf("%q: %d segments", in, len(got.segs))
			}
			for i, s := range got.segs {
				if strings.ContainsAny(s, `/\`) || s == "." || s == ".." || !utf8.ValidString(s) {
					t.Fatalf("%q: unsafe segment %d %q accepted", in, i, s)
				}
				if s == "" && i != len(got.segs)-1 {
					t.Fatalf("%q: empty interior segment %d accepted", in, i)
				}
				for j := 0; j < len(s); j++ {
					if s[j] < 0x20 || s[j] == 0x7f {
						t.Fatalf("%q: control character in segment %d accepted", in, i)
					}
				}
			}
		case pathNotCanonical:
			if want := cleanPath(in); got.canonical != want {
				t.Fatalf("%q: canonical %q, but cleanPath gives %q", in, got.canonical, want)
			}
			if again := normalizePath(got.canonical, defaultLimits, buf[:0]); again.status != pathOK {
				t.Fatalf("%q: canonical %q is refused (%s): a GET would redirect forever", in, got.canonical, again.reason)
			}
			if path.Clean(got.canonical) == "" {
				t.Fatal("unreachable")
			}
		}
	})
}

func (s pathStatus) String() string {
	return [...]string{"bad", "too long", "not canonical", "ok"}[s]
}
