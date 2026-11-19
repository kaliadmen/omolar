package routetree

// Conflict detection: design §6.2 and test plan §11.3.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestInsert_Conflicts(t *testing.T) {
	cases := []struct {
		existing, added string
		conflict        bool
	}{
		// Design §6.2
		{"GET /a/b", "GET /a/b", true},         // duplicate
		{"GET /u/{id}", "GET /u/{name}", true}, // same shape, different name
		{"GET /a/{x}", "GET /{y}/b", true},     // both match /a/b
		{"GET /a/{x}", "POST /{y}/b", false},   // the methods never overlap
		{"/x", "GET /{y}", true},               // GET /x matches both
		{"GET /files/{p...}", "GET /files/{name}", false},
		{"GET /a/", "GET /a/{$}", true}, // identical meaning
		// Methods
		{"GET /x", "/x", false},       // GET is more specific than "any"
		{"HEAD /x", "GET /x", false},  // HEAD is more specific than GET
		{"GET /x", "HEAD /{y}", true}, // HEAD /x matches both
		{"HEAD /x", "GET /{y}", false},
		{"/x", "/x", true},
		{"OPTIONS /x", "/{y}", false},
		{"GET /f/{p...}", "PUT /f/{q...}", false},
		// Wildcards
		{"GET /{p...}", "GET /{q...}", true},
		{"GET /static/{p...}", "GET /{q...}", false},
		{"GET /a/{x}/c", "GET /a/{y...}", false},
		{"GET /a/b/z", "GET /a/{x}/c", false},
		{"GET /users/{id}", "GET /users/", false}, // {id} never matches the empty segment
		{"GET /a", "GET /a/", false},
		{"GET /{p...}", "GET /", false},
		{"/a/{x...}", "GET /{y}/b", true},
		{"GET /{x}/{y}", "GET /{x}/b", false},
		{"GET /{x}/{y}", "GET /{a}/{b}", true},
	}
	for _, tc := range cases {
		name := fmt.Sprintf("%s + %s", tc.existing, tc.added)
		t.Run(name, func(t *testing.T) {
			tr := newTree(t, tc.existing)
			err := tr.Insert(tc.added, tc.added)
			if !tc.conflict {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ce *ConflictError
			if !errors.As(err, &ce) {
				t.Fatalf("Insert returned %v, want *ConflictError", err)
			}
			if ce.New != tc.added || ce.Existing != tc.existing {
				t.Errorf("New/Existing = %q/%q, want %q/%q", ce.New, ce.Existing, tc.added, tc.existing)
			}
			checkExample(t, ce, tc.existing, tc.added)
			for _, want := range []string{tc.added, tc.existing, ce.Example} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Error() %q should mention %q", err.Error(), want)
				}
			}
		})
	}
}

// checkExample confirms the example request really matches both patterns,
// each tested alone so neither can win over the other.
func checkExample(t *testing.T, ce *ConflictError, p1, p2 string) {
	t.Helper()
	method, path, ok := strings.Cut(ce.Example, " ")
	if !ok || method == "" || !strings.HasPrefix(path, "/") {
		t.Fatalf("Example %q should look like \"METHOD /path\"", ce.Example)
	}
	for _, p := range []string{p1, p2} {
		if got := lookup(t, newTree(t, p), method, path); got == "no match" {
			t.Errorf("Example %q doesn't match %q on its own", ce.Example, p)
		}
	}
}

func TestInsert_RejectedRouteLeavesTreeUnchanged(t *testing.T) {
	tr := newTree(t, "GET /a/{x}", "GET /users/{id}/posts")
	before := dump(&tr.root, "")
	routesBefore := len(tr.routes)

	// Both would create new nodes ("b" under {y}, "comments" under {uid})
	// if insertion started before the conflict check.
	for _, p := range []string{"GET /{y}/b", "GET /users/{uid}/posts"} {
		if err := tr.Insert(p, p); err == nil {
			t.Fatalf("Insert(%q) should conflict", p)
		}
	}
	if after := dump(&tr.root, ""); after != before {
		t.Errorf("tree changed after rejected inserts:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if len(tr.routes) != routesBefore {
		t.Errorf("routes list grew from %d to %d", routesBefore, len(tr.routes))
	}
}

func TestInsert_PatternErrorBeforeConflict(t *testing.T) {
	tr := newTree(t, "GET /x")
	err := tr.Insert("GET /x/{", "v")
	var pe *PatternError
	if !errors.As(err, &pe) {
		t.Errorf("a malformed pattern should be a *PatternError, got %v", err)
	}
}

// TestRelate_AgreesWithBruteForce is the strongest check of the conflict
// rules. It generates every small pattern, works out by actual matching which
// requests each one accepts, and compares sets: two patterns conflict exactly
// when some request matches both and neither's set strictly contains the
// other's. relate must agree for every pair, and every conflict's example
// must match both.
func TestRelate_AgreesWithBruteForce(t *testing.T) {
	patterns := smallPatterns()
	requests := smallRequests()

	// For each pattern, the set of requests it matches, as a bitset.
	words := (len(requests) + 63) / 64
	sets := make([][]uint64, len(patterns))
	parsed := make([]*pattern, len(patterns))
	for i, p := range patterns {
		parsed[i], _ = parsePattern(p)
		tr := newTree(t, p)
		sets[i] = make([]uint64, words)
		for j, rq := range requests {
			segs, _ := splitPath(rq.path)
			if _, ok := tr.Match(rq.method, segs, nil); ok {
				sets[i][j/64] |= 1 << (j % 64)
			}
		}
	}

	failures := 0
	for i := range patterns {
		for j := i + 1; j < len(patterns); j++ {
			truth := setRelation(sets[i], sets[j])
			got := relate(parsed[i], parsed[j])
			if got != truth {
				failures++
				if failures <= 10 {
					t.Errorf("relate(%q, %q) = %v, but matching says %v", patterns[i], patterns[j], got, truth)
				}
				continue
			}
			if truth == equivalent || truth == overlaps {
				ce := &ConflictError{Example: conflictExample(parsed[i], parsed[j])}
				checkExample(t, ce, patterns[i], patterns[j])
			}
		}
	}
	if failures > 10 {
		t.Errorf("... and %d more disagreements", failures-10)
	}
	t.Logf("checked %d patterns, %d pairs, against %d requests", len(patterns), len(patterns)*(len(patterns)-1)/2, len(requests))
}

// smallPatterns: paths of 1-3 segments drawn from a, b, {x}, plus an optional
// ending of "" (trailing slash) or {r...}, each with method any/GET/HEAD/POST.
func smallPatterns() []string {
	middles := []string{"a", "b", "{w}"}
	var paths []string
	var build func(prefix []string, depth int)
	build = func(prefix []string, depth int) {
		// endings: a normal last segment, a trailing slash, or a rest
		for _, last := range []string{"a", "b", "{x}", "", "{r...}"} {
			segs := append(append([]string{}, prefix...), last)
			paths = append(paths, "/"+strings.Join(uniqueNames(segs), "/"))
		}
		if depth < 2 {
			for _, m := range middles {
				build(append(append([]string{}, prefix...), m), depth+1)
			}
		}
	}
	build(nil, 0)

	var out []string
	for _, method := range []string{"", "GET ", "HEAD ", "POST "} {
		for _, p := range paths {
			out = append(out, method+p)
		}
	}
	return out
}

// uniqueNames renames {w} wildcards so names don't repeat within a pattern.
func uniqueNames(segs []string) []string {
	n := 0
	for i, s := range segs {
		if s == "{w}" {
			n++
			segs[i] = fmt.Sprintf("{w%d}", n)
		}
	}
	return segs
}

// smallRequests: every path of 0-4 segments over a, b, c, with and without a
// trailing slash, for four methods.
func smallRequests() []struct{ method, path string } {
	var paths []string
	var build func(prefix string, depth int)
	build = func(prefix string, depth int) {
		paths = append(paths, prefix+"/")
		if depth == 4 {
			return
		}
		for _, s := range []string{"a", "b", "c"} {
			p := prefix + "/" + s
			paths = append(paths, p)
			build(p, depth+1)
		}
	}
	build("", 0)

	var out []struct{ method, path string }
	for _, m := range []string{"GET", "HEAD", "POST", "PUT"} {
		for _, p := range paths {
			out = append(out, struct{ method, path string }{m, p})
		}
	}
	return out
}

// setRelation compares two request sets.
func setRelation(a, b []uint64) relationship {
	inter, aOnly, bOnly := false, false, false
	for i := range a {
		inter = inter || a[i]&b[i] != 0
		aOnly = aOnly || a[i]&^b[i] != 0
		bOnly = bOnly || b[i]&^a[i] != 0
	}
	switch {
	case !inter:
		return disjoint
	case !aOnly && !bOnly:
		return equivalent
	case !aOnly:
		return moreSpecific // every request a matches, b matches too
	case !bOnly:
		return moreGeneral
	default:
		return overlaps
	}
}

func (r relationship) String() string {
	return [...]string{"equivalent", "moreSpecific", "moreGeneral", "disjoint", "overlaps"}[r]
}

// dump renders the tree's structure, for "unchanged" comparisons.
func dump[V any](n *node[V], indent string) string {
	var b strings.Builder
	describe := func(name string, tb *table[V]) {
		if tb == nil {
			return
		}
		fmt.Fprintf(&b, "%s%s:", indent, name)
		for _, r := range tb.byMethod {
			fmt.Fprintf(&b, " %q", r.pat.raw)
		}
		if tb.any != nil {
			fmt.Fprintf(&b, " %q", tb.any.pat.raw)
		}
		b.WriteString("\n")
	}
	describe("leaf", n.leaf)
	describe("rest", n.rest)
	for i, l := range n.labels {
		fmt.Fprintf(&b, "%s%q\n", indent, l)
		b.WriteString(dump(n.statics[i], indent+"  "))
	}
	if n.param != nil {
		fmt.Fprintf(&b, "%s{param}\n", indent)
		b.WriteString(dump(n.param, indent+"  "))
	}
	return b.String()
}
