# Makefile — shortcuts for common tasks. Run `make help` to list them.
# Each target is just a shell command; open this file to see exactly what runs.

.PHONY: help test report cover vet fmt run-hello

## help: list available commands
help:
	@grep -E '^## ' Makefile | sed 's/## /  /'

## test: run every test with the race detector (catches unsafe concurrent access)
test:
	go test -race ./...

## report: run every test and write test-report.json (share this file with Claude)
report:
	go run ./cmd/testreport -out test-report.json

## cover: build an HTML coverage report and print where it is
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html in a browser"

## vet: run go vet, Go's built-in static checker
vet:
	go vet ./...

## fmt: format all code the standard Go way
fmt:
	gofmt -w .

## run-hello: run the example app on http://localhost:4000
run-hello:
	go run ./examples/hello
