// Command testreport runs the test suite and writes test-report.json.
//
// Usage:
//
//	go run ./cmd/testreport                 # run ./... and write test-report.json
//	go run ./cmd/testreport -out r.json     # choose the output file
//	go test -json ./... | go run ./cmd/testreport -stdin   # parse piped output
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "testreport: not implemented yet")
	os.Exit(1)
}
