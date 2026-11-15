# Makefile: shortcuts for common tasks. Run `make help` to list them.
# Each target is just a shell command; open this file to see exactly what runs.
#
# Two sets of targets:
#   docker-*, up, down   run inside Docker (the normal way on your VM)
#   test, report, ...    run with a local Go install (used inside the dev
#                        container and by CI)

.PHONY: help test report cover vet fmt run-hello \
        docker-test docker-report docker-cover docker-vet docker-fmt \
        docker-shell docker-dev-run docker-build up down

DC  := docker compose
DEV := $(DC) run --rm dev

# Passed to docker-compose.yml so the dev container runs as your user.
export HOST_UID := $(shell id -u)
export HOST_GID := $(shell id -g)

## help: list available commands
help:
	@grep -E '^## ' Makefile | sed 's/## /  /'

# ---- Docker (use these on the VM) -------------------------------------------

## docker-test: run every test inside the dev container
docker-test:
	$(DEV) make test

## docker-report: run tests in Docker and write test-report.json here (share it with Claude)
docker-report:
	$(DEV) make report

## docker-cover: build coverage.html in Docker
docker-cover:
	$(DEV) make cover

## docker-vet: run go vet in Docker
docker-vet:
	$(DEV) make vet

## docker-fmt: format all code in Docker
docker-fmt:
	$(DEV) make fmt

## docker-shell: open a bash shell inside the dev container (go, git, make available)
docker-shell:
	$(DEV) bash

## docker-dev-run: run examples/hello from source at http://<vm>:4001 (Ctrl+C to stop)
docker-dev-run:
	$(DC) run --rm --service-ports dev go run ./examples/hello

## docker-build: build the production image (omolar-hello:dev)
docker-build:
	docker build -t omolar-hello:dev .

## up: build and run the production image at http://<vm>:4000 (Ctrl+C to stop)
up:
	$(DC) up --build app

## down: stop and remove the containers
down:
	$(DC) down

# ---- Local Go (inside the dev container, or if Go is installed) ------------

## test: run every test with the race detector (catches unsafe concurrent access)
test:
	go test -race ./...

## report: run every test and write test-report.json
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
