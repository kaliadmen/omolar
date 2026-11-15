// Command testreport runs the test suite and writes test-report.json.
//
// Usage:
//
//	go run ./cmd/testreport                        # test ./... and write test-report.json
//	go run ./cmd/testreport -out r.json ./config   # choose output file and packages
//	go test -json ./... | go run ./cmd/testreport -stdin
//
// It exits with status 1 when any test fails, so it can be used in scripts/CI.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/kaliadmen/omolar/internal/testreport"
)

func main() {
	// GO CONCEPT: the flag package parses command-line options. Each call
	// returns a *pointer*; the value is filled in when flag.Parse() runs.
	out := flag.String("out", "test-report.json", "where to write the JSON report")
	fromStdin := flag.Bool("stdin", false, "read `go test -json` output from stdin instead of running tests")
	race := flag.Bool("race", false, "run tests with the race detector")
	flag.Parse()

	// Any arguments left after the flags are package patterns.
	pkgs := flag.Args()
	if len(pkgs) == 0 {
		pkgs = []string{"./..."}
	}

	var (
		rep *testreport.Report
		err error
	)
	if *fromStdin {
		rep, err = testreport.Parse(os.Stdin)
	} else {
		rep, err = runTests(pkgs, *race)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "testreport:", err)
		os.Exit(2)
	}

	addMetadata(rep)

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testreport:", err)
		os.Exit(2)
	}
	if err := rep.WriteJSON(f); err != nil {
		f.Close()
		fmt.Fprintln(os.Stderr, "testreport:", err)
		os.Exit(2)
	}
	f.Close()

	fmt.Print(rep.Text())
	fmt.Printf("\nWrote %s\n", *out)

	if !rep.OK {
		os.Exit(1)
	}
}

// runTests runs `go test -json -cover` and parses its output as it streams.
func runTests(pkgs []string, race bool) (*testreport.Report, error) {
	args := []string{"test", "-json", "-cover"}
	if race {
		args = append(args, "-race")
	}
	args = append(args, pkgs...)

	cmd := exec.Command("go", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting go test: %w", err)
	}

	rep, parseErr := testreport.Parse(stdout)

	// `go test` exits non-zero when tests fail. That's expected (the report
	// records the failures), so we ignore the exit error here.
	_ = cmd.Wait()

	if parseErr != nil {
		return nil, parseErr
	}
	// Anything go test printed to stderr goes into the report too.
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			rep.Unparsed = append(rep.Unparsed, line)
		}
	}
	if rep.Summary.Packages == 0 && len(rep.Unparsed) > 0 {
		rep.OK = false
	}
	return rep, nil
}

// addMetadata records what machine and commit the tests ran on.
func addMetadata(rep *testreport.Report) {
	rep.GeneratedAt = time.Now().UTC().Truncate(time.Second)
	rep.GoVersion = runtime.Version()
	rep.OS = runtime.GOOS
	rep.Arch = runtime.GOARCH
	rep.GitCommit = git("rev-parse", "--short", "HEAD")
	rep.GitBranch = git("rev-parse", "--abbrev-ref", "HEAD")
	// "dirty" means there are uncommitted changes, so the commit hash alone
	// doesn't describe the code that was tested.
	rep.GitDirty = git("status", "--porcelain") != ""
}

// git runs a git command and returns its trimmed output, or "" on any error
// (for example when git isn't installed).
func git(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
