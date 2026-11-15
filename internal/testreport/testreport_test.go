package testreport

// READ THIS FILE FIRST. These tests are the specification for the report tool:
// each one describes a situation `go test -json` can produce and what the
// report should say about it.
//
// The fixtures in testdata/ are REAL output captured from `go test -json`.
// Open testdata/mixed.jsonl to see what the raw stream looks like.
//
// TRY: run just one test with
//   go test ./internal/testreport -run TestParse_FailingTest -v

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// parseFixture is a test helper: it opens a file from testdata/ and parses it.
//
// GO CONCEPT: t.Helper() marks this function as a helper, so when a check fails
// Go reports the line number in the *calling* test, not in here.
func parseFixture(t *testing.T, name string) *Report {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	// GO CONCEPT: defer runs this call when the surrounding function returns,
	// no matter how it returns. It's Go's way of guaranteeing cleanup.
	defer f.Close()

	rep, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if rep == nil {
		t.Fatal("Parse returned a nil report")
	}
	return rep
}

// findPackage and findTest return nil when the item isn't there, so tests can
// give a clear "missing" message instead of crashing on an index error.
func findPackage(rep *Report, name string) *PackageResult {
	for i := range rep.Packages {
		if rep.Packages[i].Name == name {
			return &rep.Packages[i]
		}
	}
	return nil
}

func findTest(pkg *PackageResult, name string) *TestResult {
	for i := range pkg.Tests {
		if pkg.Tests[i].Name == name {
			return &pkg.Tests[i]
		}
	}
	return nil
}

func TestParse_PackageStatuses(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")

	// GO CONCEPT: table-driven tests. Instead of writing four near-identical
	// tests, we list the cases as data and loop over them. Adding a case is
	// one line.
	cases := []struct {
		pkg  string
		want Status
	}{
		{"fx/good", StatusPass},
		{"fx/bad", StatusFail},
		{"fx/broken", StatusFail}, // compile error
		{"fx/empty", StatusSkip},  // no test files
	}

	for _, tc := range cases {
		// t.Run creates a named subtest, so failures read like
		// "TestParse_PackageStatuses/fx/bad".
		t.Run(tc.pkg, func(t *testing.T) {
			pkg := findPackage(rep, tc.pkg)
			if pkg == nil {
				t.Fatalf("package %s missing from report", tc.pkg)
			}
			if pkg.Status != tc.want {
				t.Errorf("status = %q, want %q", pkg.Status, tc.want)
			}
		})
	}
}

func TestParse_PackagesAreSortedByName(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")

	var names []string
	for _, p := range rep.Packages {
		names = append(names, p.Name)
	}
	want := []string{"fx/bad", "fx/broken", "fx/empty", "fx/good"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("package order = %v, want %v", names, want)
	}
}

func TestParse_Coverage(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")

	good := findPackage(rep, "fx/good")
	if good == nil {
		t.Fatal("fx/good missing")
	}
	if good.CoveragePercent == nil {
		t.Fatal("fx/good coverage is nil, want 50.0")
	}
	if *good.CoveragePercent != 50.0 {
		t.Errorf("fx/good coverage = %v, want 50.0", *good.CoveragePercent)
	}

	// A package with no test files has no coverage data at all: nil, not 0.
	empty := findPackage(rep, "fx/empty")
	if empty == nil {
		t.Fatal("fx/empty missing")
	}
	if empty.CoveragePercent != nil {
		t.Errorf("fx/empty coverage = %v, want nil", *empty.CoveragePercent)
	}
}

func TestParse_TestsAndSubtests(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")
	good := findPackage(rep, "fx/good")
	if good == nil {
		t.Fatal("fx/good missing")
	}

	want := map[string]Status{
		"TestAdd":       StatusPass,
		"TestSkip":      StatusSkip,
		"TestTable":     StatusPass,
		"TestTable/one": StatusPass, // subtests are reported with their full name
		"TestTable/two": StatusPass,
	}
	if len(good.Tests) != len(want) {
		t.Errorf("got %d tests, want %d", len(good.Tests), len(want))
	}
	for name, status := range want {
		tr := findTest(good, name)
		if tr == nil {
			t.Errorf("test %s missing", name)
			continue
		}
		if tr.Status != status {
			t.Errorf("%s status = %q, want %q", name, tr.Status, status)
		}
	}
}

func TestParse_TestsKeepStartOrder(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")
	good := findPackage(rep, "fx/good")
	if good == nil {
		t.Fatal("fx/good missing")
	}
	var names []string
	for _, tr := range good.Tests {
		names = append(names, tr.Name)
	}
	want := "TestAdd,TestSkip,TestTable,TestTable/one,TestTable/two"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
}

func TestParse_FailingTestKeepsOutput(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")
	bad := findPackage(rep, "fx/bad")
	if bad == nil {
		t.Fatal("fx/bad missing")
	}

	sub := findTest(bad, "TestDiv/zero")
	if sub == nil {
		t.Fatal("TestDiv/zero missing")
	}
	if sub.Status != StatusFail {
		t.Errorf("status = %q, want fail", sub.Status)
	}
	// The failure message is the whole point of the report.
	if !strings.Contains(sub.Output, "want 0, got 5") {
		t.Errorf("output %q does not contain the failure message", sub.Output)
	}
}

func TestParse_PassingTestsDropOutput(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")
	good := findPackage(rep, "fx/good")
	if good == nil {
		t.Fatal("fx/good missing")
	}
	tr := findTest(good, "TestAdd")
	if tr == nil {
		t.Fatal("TestAdd missing")
	}
	if tr.Output != "" {
		t.Errorf("passing test kept output %q, want empty", tr.Output)
	}
}

func TestParse_BuildFailureKeepsCompilerError(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")
	broken := findPackage(rep, "fx/broken")
	if broken == nil {
		t.Fatal("fx/broken missing")
	}
	// The compiler error arrives in "build-output" events keyed by ImportPath
	// ("fx/broken [fx/broken.test]"), not by Package. The parser has to connect
	// the two.
	if !strings.Contains(broken.Output, "cannot use") {
		t.Errorf("package output %q does not contain the compiler error", broken.Output)
	}
}

func TestParse_PanicIsAFailureWithStackTrace(t *testing.T) {
	rep := parseFixture(t, "panic.jsonl")
	pkg := findPackage(rep, "fx/panicky")
	if pkg == nil {
		t.Fatal("fx/panicky missing")
	}
	tr := findTest(pkg, "TestBoom")
	if tr == nil {
		t.Fatal("TestBoom missing")
	}
	if tr.Status != StatusFail {
		t.Errorf("status = %q, want fail", tr.Status)
	}
	if !strings.Contains(tr.Output, "assignment to entry in nil map") {
		t.Errorf("output does not contain the panic message:\n%s", tr.Output)
	}
}

func TestParse_UnfinishedTestCountsAsFailure(t *testing.T) {
	// A test that starts but never reports pass/fail/skip, e.g. because the
	// test binary was killed by a timeout. Hand-written events this time.
	input := `{"Action":"start","Package":"p"}
{"Action":"run","Package":"p","Test":"TestHang"}
{"Action":"output","Package":"p","Test":"TestHang","Output":"=== RUN   TestHang\n"}
{"Action":"fail","Package":"p","Elapsed":30}
`
	rep, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if rep == nil {
		t.Fatal("nil report")
	}
	pkg := findPackage(rep, "p")
	if pkg == nil {
		t.Fatal("package p missing")
	}
	tr := findTest(pkg, "TestHang")
	if tr == nil {
		t.Fatal("TestHang missing")
	}
	if tr.Status != StatusFail {
		t.Errorf("status = %q, want fail", tr.Status)
	}
	if !strings.Contains(tr.Output, "did not finish") {
		t.Errorf("output %q should explain the test did not finish", tr.Output)
	}
}

func TestParse_NonJSONLinesAreKept(t *testing.T) {
	input := `go: downloading example.com/thing v1.0.0
{"Action":"start","Package":"p"}
{"Action":"pass","Package":"p","Elapsed":0.1}
`
	rep, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if rep == nil {
		t.Fatal("nil report")
	}
	if len(rep.Unparsed) != 1 || !strings.Contains(rep.Unparsed[0], "downloading") {
		t.Errorf("Unparsed = %q, want the one non-JSON line", rep.Unparsed)
	}
	if findPackage(rep, "p") == nil {
		t.Error("the valid events after the bad line were not parsed")
	}
}

func TestParse_Summary(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")

	// fx/good: 4 pass + 1 skip; fx/bad: 2 fail (TestDiv and TestDiv/zero).
	want := Summary{
		Packages:       4,
		PackagesFailed: 2,
		Tests:          7,
		Passed:         4,
		Failed:         2,
		Skipped:        1,
	}
	got := rep.Summary
	got.DurationSeconds = 0 // checked separately below; depends on timestamps

	// GO CONCEPT: structs with only comparable fields can be compared with ==.
	if got != want {
		t.Errorf("summary =\n  %+v\nwant\n  %+v", got, want)
	}
	if rep.Summary.DurationSeconds <= 0 {
		t.Errorf("duration = %v, want > 0", rep.Summary.DurationSeconds)
	}
	if rep.OK {
		t.Error("OK = true, want false (there are failures)")
	}
}

func TestParse_AllPassingIsOK(t *testing.T) {
	input := `{"Action":"start","Package":"p"}
{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0}
{"Action":"pass","Package":"p","Elapsed":0.1}
`
	rep, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if rep == nil || !rep.OK {
		t.Errorf("OK should be true when everything passes, got report %+v", rep)
	}
}

func TestWriteJSON_UsesSnakeCaseKeys(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON error: %v", err)
	}

	// Decode into a generic map to check the actual key names on disk.
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"ok", "summary", "packages", "go_version"} {
		if _, found := raw[key]; !found {
			t.Errorf("JSON is missing key %q", key)
		}
	}
	if !strings.Contains(buf.String(), "\n  ") {
		t.Error("JSON should be indented so it's readable")
	}
}

func TestText_MentionsFailures(t *testing.T) {
	rep := parseFixture(t, "mixed.jsonl")
	text := rep.Text()

	for _, want := range []string{"FAIL", "fx/bad", "TestDiv/zero", "want 0, got 5"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary text is missing %q:\n%s", want, text)
		}
	}
}
