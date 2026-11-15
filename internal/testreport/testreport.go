// Package testreport turns the event stream printed by `go test -json` into a
// single, easy-to-share JSON report (test-report.json).
//
// GO CONCEPT: "internal" packages. Because this package lives under a folder
// named internal/, the Go compiler only lets code inside this module import it.
// It's a tool for Omolar's own development, not part of the public framework API.
//
// How it works, in one paragraph: `go test -json` prints one JSON object per
// line ("an event"). Events for many packages and tests are interleaved. We
// read them one at a time, route each event to the package and test it belongs
// to, and at the end we "finish" everything: sort, count, and decide what
// output is worth keeping.
package testreport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event is one line of `go test -json` output. The field names match the JSON
// keys Go prints, so encoding/json can fill this struct automatically.
//
// GO CONCEPT: struct tags. The text in backticks (`json:"Action"`) tells the
// encoding/json package which JSON key maps to which field.
type Event struct {
	Time       time.Time `json:"Time"`
	Action     string    `json:"Action"`     // start, run, output, pass, fail, skip, build-output, build-fail...
	Package    string    `json:"Package"`    // e.g. github.com/kaliadmen/omolar/config
	ImportPath string    `json:"ImportPath"` // only set on build-output / build-fail events
	Test       string    `json:"Test"`       // empty for package-level events
	Elapsed    float64   `json:"Elapsed"`    // seconds, set on pass/fail/skip
	Output     string    `json:"Output"`     // one line of printed output
}

// Status is the outcome of a test or package.
//
// GO CONCEPT: defined types. Status is a new type whose underlying type is
// string. That lets the compiler stop you from passing any random string
// where a Status is expected, while still printing and encoding as text.
type Status string

// The possible outcomes.
const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// TestResult is the outcome of a single test or subtest.
type TestResult struct {
	Name           string  `json:"name"`
	Status         Status  `json:"status"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	// Output is only kept for failed tests. Passing tests would just add noise.
	Output string `json:"output,omitempty"`
}

// PackageResult is the outcome of one Go package and all of its tests.
type PackageResult struct {
	Name           string  `json:"name"`
	Status         Status  `json:"status"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	// CoveragePercent is a pointer so that "no coverage data" (nil, omitted from
	// the JSON) is different from "0% coverage".
	CoveragePercent *float64 `json:"coverage_percent,omitempty"`
	// Output holds package-level output when the package failed for a reason
	// that isn't a single test, such as a compile error.
	Output string       `json:"output,omitempty"`
	Tests  []TestResult `json:"tests"`
}

// Summary holds the headline numbers.
type Summary struct {
	Packages        int     `json:"packages"`
	PackagesFailed  int     `json:"packages_failed"`
	Tests           int     `json:"tests"`
	Passed          int     `json:"passed"`
	Failed          int     `json:"failed"`
	Skipped         int     `json:"skipped"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// Report is the whole test-report.json file.
type Report struct {
	OK          bool      `json:"ok"`
	GeneratedAt time.Time `json:"generated_at"`
	GoVersion   string    `json:"go_version"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	GitCommit   string    `json:"git_commit,omitempty"`
	GitBranch   string    `json:"git_branch,omitempty"`
	GitDirty    bool      `json:"git_dirty"`

	Summary  Summary         `json:"summary"`
	Packages []PackageResult `json:"packages"`

	// Unparsed collects any input lines that weren't valid JSON events, so
	// nothing printed by the test run is silently lost.
	Unparsed []string `json:"unparsed,omitempty"`
}

// coverageRe finds "coverage: 50.0% of statements" in package output.
//
// GO CONCEPT: package-level variables are initialized once when the program
// starts. Compiling a regexp is relatively slow, so we do it once here rather
// than every time we need it. MustCompile panics on a bad pattern, which is
// fine for a constant pattern: a typo would crash immediately at startup.
var coverageRe = regexp.MustCompile(`coverage: (\d+(?:\.\d+)?)% of statements`)

// unfinishedNote is appended to tests that started but never reported a result.
const unfinishedNote = "test did not finish (the test binary may have panicked, been killed, or timed out)\n"

// testState is the in-progress record for one test while we read events.
type testState struct {
	result TestResult
	output strings.Builder
}

// pkgState is the in-progress record for one package while we read events.
//
// Data structure note: we need two things for tests: look up a test by name
// quickly (every output event names its test) AND remember the order tests
// started in. A map gives O(1) lookup but no order; a slice gives order but
// O(n) lookup. So we keep both: the slice owns the order, the map points into it.
type pkgState struct {
	name    string
	status  Status
	elapsed float64
	output  strings.Builder       // package-level output (no Test field)
	tests   []*testState          // in the order they started
	byName  map[string]*testState // fast lookup by test name
}

// test returns the state for a test, creating it the first time it's seen.
func (p *pkgState) test(name string) *testState {
	if ts, ok := p.byName[name]; ok {
		return ts
	}
	ts := &testState{result: TestResult{Name: name}}
	p.tests = append(p.tests, ts)
	p.byName[name] = ts
	return ts
}

// parser holds everything we accumulate while reading the stream.
type parser struct {
	packages    map[string]*pkgState
	buildOutput map[string]*strings.Builder // compiler errors, keyed by package name
	unparsed    []string
	first, last time.Time // earliest and latest event timestamps
}

func newParser() *parser {
	return &parser{
		packages:    make(map[string]*pkgState),
		buildOutput: make(map[string]*strings.Builder),
	}
}

// pkg returns the state for a package, creating it the first time it's seen.
func (ps *parser) pkg(name string) *pkgState {
	if p, ok := ps.packages[name]; ok {
		return p
	}
	p := &pkgState{name: name, byName: make(map[string]*testState)}
	ps.packages[name] = p
	return p
}

// Parse reads `go test -json` output and builds a Report. It fills in
// OK, Summary, Packages and Unparsed; the caller adds machine metadata
// (Go version, git commit...).
func Parse(r io.Reader) (*Report, error) {
	ps := newParser()

	// GO CONCEPT: bufio.Scanner reads input one line at a time without loading
	// the whole thing into memory. Its default max line length is 64KB; a long
	// panic trace can exceed that, so we allow lines up to 10MB.
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var ev Event
		// Anything that isn't a JSON object (like "go: downloading ...") is
		// kept rather than dropped, so the report never hides information.
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil {
			ps.unparsed = append(ps.unparsed, line)
			continue
		}
		ps.handle(ev)
	}
	// sc.Err() reports read errors (not end-of-file, which is normal).
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading test output: %w", err)
	}

	return ps.finish(), nil
}

// handle routes one event to the package/test it belongs to.
func (ps *parser) handle(ev Event) {
	if !ev.Time.IsZero() {
		if ps.first.IsZero() || ev.Time.Before(ps.first) {
			ps.first = ev.Time
		}
		if ev.Time.After(ps.last) {
			ps.last = ev.Time
		}
	}

	switch ev.Action {
	case "build-output":
		// Compiler errors are keyed by ImportPath, e.g. "fx/broken [fx/broken.test]".
		// Strip the bracketed part to get the package name.
		name, _, _ := strings.Cut(ev.ImportPath, " [")
		b, ok := ps.buildOutput[name]
		if !ok {
			b = &strings.Builder{}
			ps.buildOutput[name] = b
		}
		b.WriteString(ev.Output)
		return
	case "build-fail":
		// The package's own "fail" event follows; nothing extra to record.
		return
	}

	if ev.Package == "" {
		return
	}
	p := ps.pkg(ev.Package)

	// Package-level event (no test name).
	if ev.Test == "" {
		switch ev.Action {
		case "output":
			p.output.WriteString(ev.Output)
		case "pass", "fail", "skip":
			p.status = Status(ev.Action)
			p.elapsed = ev.Elapsed
		}
		return
	}

	// Test-level event.
	t := p.test(ev.Test)
	switch ev.Action {
	case "output":
		t.output.WriteString(ev.Output)
	case "pass", "fail", "skip":
		t.result.Status = Status(ev.Action)
		t.result.ElapsedSeconds = ev.Elapsed
	}
}

// finish turns the accumulated state into the final Report.
func (ps *parser) finish() *Report {
	rep := &Report{Packages: []PackageResult{}}

	// Sort package names so the report is stable from run to run.
	names := make([]string, 0, len(ps.packages))
	for name := range ps.packages {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := ps.packages[name]
		pr := PackageResult{
			Name:            name,
			Status:          p.status,
			ElapsedSeconds:  p.elapsed,
			CoveragePercent: parseCoverage(p.output.String()),
			Tests:           make([]TestResult, 0, len(p.tests)),
		}
		// A package that never reported an outcome didn't succeed.
		if pr.Status == "" {
			pr.Status = StatusFail
		}

		failedTests := 0
		for _, ts := range p.tests {
			tr := ts.result
			out := cleanOutput(ts.output.String())
			if tr.Status == "" {
				tr.Status = StatusFail
				out += unfinishedNote
			}
			if tr.Status == StatusFail {
				tr.Output = out
				failedTests++
			}
			pr.Tests = append(pr.Tests, tr)

			rep.Summary.Tests++
			switch tr.Status {
			case StatusPass:
				rep.Summary.Passed++
			case StatusFail:
				rep.Summary.Failed++
			case StatusSkip:
				rep.Summary.Skipped++
			}
		}

		// If the package failed but no test explains why (compile error,
		// TestMain crash...), keep the package's own output.
		if pr.Status == StatusFail && failedTests == 0 {
			var b strings.Builder
			if bo, ok := ps.buildOutput[name]; ok {
				b.WriteString(bo.String())
			}
			b.WriteString(p.output.String())
			pr.Output = b.String()
		}

		rep.Summary.Packages++
		if pr.Status == StatusFail {
			rep.Summary.PackagesFailed++
		}
		rep.Packages = append(rep.Packages, pr)
	}

	if !ps.first.IsZero() {
		secs := ps.last.Sub(ps.first).Seconds()
		rep.Summary.DurationSeconds = math.Round(secs*1000) / 1000 // millisecond precision
	}
	rep.Unparsed = ps.unparsed
	rep.OK = rep.Summary.PackagesFailed == 0 && rep.Summary.Failed == 0
	return rep
}

// parseCoverage returns the first coverage percentage found, or nil.
func parseCoverage(output string) *float64 {
	m := coverageRe.FindStringSubmatch(output)
	if m == nil {
		return nil
	}
	// m[0] is the whole match, m[1] is the first (...) group: the number.
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	return &v
}

// cleanOutput drops the "=== RUN / PAUSE / CONT" bookkeeping lines Go prints,
// keeping only what the test actually said.
func cleanOutput(s string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "=== RUN") ||
			strings.HasPrefix(trimmed, "=== PAUSE") ||
			strings.HasPrefix(trimmed, "=== CONT") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// WriteJSON writes the report as indented JSON.
func (rep *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// By default encoding/json escapes <, > and & for safe embedding in HTML.
	// This file is read by humans and tools, so keep the characters as-is.
	enc.SetEscapeHTML(false)
	return enc.Encode(rep)
}

// Text returns a short human-readable summary for the terminal.
func (rep *Report) Text() string {
	var b strings.Builder

	verdict := "PASS"
	if !rep.OK {
		verdict = "FAIL"
	}
	s := rep.Summary
	fmt.Fprintf(&b, "Omolar test report: %s\n", verdict)
	fmt.Fprintf(&b, "  %d packages (%d failed) | %d tests: %d passed, %d failed, %d skipped | %.2fs\n\n",
		s.Packages, s.PackagesFailed, s.Tests, s.Passed, s.Failed, s.Skipped, s.DurationSeconds)

	for _, p := range rep.Packages {
		cov := ""
		if p.CoveragePercent != nil {
			cov = fmt.Sprintf("%5.1f%%", *p.CoveragePercent)
		}
		fmt.Fprintf(&b, "  %-4s  %-50s %s\n", strings.ToUpper(string(p.Status)), p.Name, cov)
	}

	if rep.OK {
		return b.String()
	}

	b.WriteString("\nFailures:\n")
	for _, p := range rep.Packages {
		if p.Output != "" {
			fmt.Fprintf(&b, "\n  %s (package)\n%s", p.Name, indent(p.Output, "      "))
		}
		for _, t := range p.Tests {
			// Only show the deepest failing test. If TestDiv/zero failed,
			// TestDiv also "failed", but showing both just repeats the news.
			if t.Status != StatusFail || hasFailedChild(p, t.Name) {
				continue
			}
			fmt.Fprintf(&b, "\n  %s  %s\n%s", p.Name, t.Name, indent(t.Output, "      "))
		}
	}
	return b.String()
}

// hasFailedChild reports whether any subtest of name (name/...) failed.
func hasFailedChild(p PackageResult, name string) bool {
	prefix := name + "/"
	for _, t := range p.Tests {
		if t.Status == StatusFail && strings.HasPrefix(t.Name, prefix) {
			return true
		}
	}
	return false
}

// indent prefixes every non-empty line of s.
func indent(s, prefix string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(prefix)
		b.WriteString(strings.TrimLeft(line, " \t"))
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
