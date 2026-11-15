# Omolar

[![CI](https://github.com/kaliadmen/omolar/actions/workflows/ci.yml/badge.svg)](https://github.com/kaliadmen/omolar/actions/workflows/ci.yml)

A security-minded web framework for Go, built test-first on the standard library.

> **Status:** early development (v0.1.0-dev). APIs will change.

## Why Omolar

- **Secure by default.** Safe server timeouts, Slowloris protection, debug mode refused in
  production. Security headers, CSRF protection, rate limiting and audit logging are on the roadmap.
- **Standard library first.** Few dependencies means a small attack surface and less to keep patched.
- **Built test-first.** Every feature starts as a failing test. Read the tests to learn the API.
- **Hand-built core data structures.** Router trie, LRU cache, token-bucket rate limiter, heap
  scheduler, each with its own tests.

## Quick start

Requires Docker (no local Go install needed). Built on Go 1.27.

```sh
git clone https://github.com/kaliadmen/omolar
cd omolar
make up
```

Then open <http://localhost:4000>, <http://localhost:4000/hello/you> and
<http://localhost:4000/health>. Press Ctrl+C to watch a graceful shutdown.

Have Go 1.27+ installed instead? `make run-hello` works too.

A minimal app:

```go
cfg, err := config.Load(".env")   // .env is optional; env vars override it
if err != nil { log.Fatal(err) }  // or build one in code: cfg := config.Default()

app, err := omolar.New(cfg)
if err != nil { log.Fatal(err) }

app.HandleFunc("GET /hello/{name}", func(w http.ResponseWriter, r *http.Request) {
    fmt.Fprintf(w, "Hello, %s!", r.PathValue("name"))
})

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()
app.Run(ctx) // returns when Ctrl+C is pressed, after in-flight requests finish
```

## Configuration

Settings come from environment variables, optionally loaded from a `.env` file. See
[`.env.example`](.env.example) for every option. Invalid values stop the app at startup
with a list of every problem, not just the first.

You can also build a config in code, starting from `config.Default()`. `omolar.New`
validates it with the same rules, so a hand-built config can't skip them (zero or
negative timeouts, port 0, `DEBUG` in production...). After `New`, the app's settings
are fixed: `app.Config()` returns a copy.

## Packages

| Package | Purpose |
|---|---|
| `omolar` | `App`: ties config, logging, routing and the server together |
| `config` | `.env` parser and validated, typed configuration |
| `logging` | Structured text/JSON logging on `log/slog` |
| `server` | HTTP server with safe timeouts, graceful shutdown, `/health` |
| `internal/testreport` | Turns `go test -json` into `test-report.json` |

## Development

All development runs in Docker. `docker-compose.yml` has a `dev` service with the Go
toolchain, and your checkout is mounted into it.

```sh
make docker-test      # all tests with the race detector
make docker-report    # run tests and write test-report.json
make docker-cover     # HTML coverage report
make docker-shell     # shell with go, git and make
make docker-dev-run   # run examples/hello with live reload on :4001
make up / make down   # run / stop the production image on :4000
```

The production image is a two-stage build onto `distroless/static:nonroot` (~10–15 MB).
It runs as non-root on a read-only filesystem with all capabilities dropped, and checks
its own health with `/app healthcheck` because there's no curl.

With Go installed locally, the same targets without `docker-` work: `make test`,
`make report`, `make cover`, `make vet`.

Each feature lands as a `test(...)` commit (failing tests) followed by a `feat(...)`
commit (code that makes them pass). Learning notes for each milestone are in
[`docs/learning/`](docs/learning/).

## Roadmap

- [x] **M1** Config, logging, server, graceful shutdown, test reports, Docker
- [ ] **M2** Radix-tree router with path parameters
- [ ] **M3** Middleware: panic recovery, request IDs, request logging, security headers
- [ ] **M4** Templates and static files (plus a dev-only page that serves `coverage.html`)
- [ ] **M5** Database layer and migrations
- [ ] **M6** Sessions, CSRF, authentication
- [ ] **M7** Cache (LRU + Redis) and rate limiting
- [ ] **M8** Mail queue and background jobs
- [ ] **M9** CLI and code generation

## License

MIT
