// Package testreport turns the event stream printed by `go test -json` into a
// single, easy-to-share JSON report (test-report.json).
//
// GO CONCEPT: "internal" packages. Because this package lives under a folder
// named internal/, the Go compiler only lets code inside this module import it.
// It's a tool for Omolar's own development, not part of the public framework API.
package testreport

import (
	"errors"
	"io"
	"time"
)

// errNotImplemented is returned by stubs during the "red" step of TDD.
var errNotImplemented = errors.New("not implemented")

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

// Parse reads `go test -json` output and builds a Report. It fills in
// OK, Summary, Packages and Unparsed; the caller adds machine metadata
// (Go version, git commit...).
func Parse(r io.Reader) (*Report, error) {
	return nil, errNotImplemented
}

// WriteJSON writes the report as indented JSON.
func (rep *Report) WriteJSON(w io.Writer) error {
	return errNotImplemented
}

// Text returns a short human-readable summary for the terminal.
func (rep *Report) Text() string {
	return ""
}
