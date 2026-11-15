# Milestone 1: A basic server

**What you get:** a framework that loads settings, logs in a structured way, serves
HTTP with safe timeouts, and shuts down gracefully. Plus a test report tool.

## Reading order

Read each test file *before* its code file. The tests say what the code must do.

1. `config/dotenv_test.go` → `config/dotenv.go`
   A hand-written parser. Watch how one line is broken into key, value, quotes and comments.
2. `config/config_test.go` → `config/config.go`
   Defaults, validation, and why every error is collected before returning.
3. `logging/logging_test.go` → `logging/logging.go`
   Short. Your first look at interfaces (`slog.Handler`).
4. `server/health_test.go` → `server/health.go`
   The simplest possible HTTP handler, tested without a network.
5. `server/server_test.go` → `server/server.go`
   The hardest file this milestone: goroutines, channels, `select`, contexts.
   Take your time here.
6. `omolar_test.go` → `omolar.go`
   How the pieces plug together. The functional options pattern.
7. `examples/hello/main.go`: a real app using it all.
8. Bonus: `internal/testreport/`, the tool behind `make report`.

Search the code for `GO CONCEPT:` to jump between explanations, and `TRY:` for experiments.

## Go concepts introduced

| Concept | Where to see it |
|---|---|
| Packages, exported vs unexported names | everywhere; capital letter = exported |
| `internal/` packages | `internal/testreport` |
| Structs and struct tags | `testreport.Event` |
| Defined types (`type Status string`) | `testreport.Status` |
| Multiple return values, `(T, error)` | `config.ParseDotEnv` |
| Custom error types, `errors.As` | `config.ParseError`, `dotenv_test.go` |
| `errors.Join`, `errors.Is`, `%w` wrapping | `config.FromLookup`, `config.Load` |
| `defer` | `testreport_test.go`, `server.Serve` |
| Function types and closures | `config.LookupFunc`, `fakeEnv` |
| Variadic functions (`...any`) | `loader.fail` |
| Value vs pointer receivers | `Config.IsProduction` vs `loader` methods |
| Interfaces | `slog.Handler`, `http.Handler` |
| `http.HandlerFunc` adapter | `server.Health` |
| Goroutines, channels, `select` | `server.Serve`, `server_test.go` |
| `context.Context` and cancellation | `server.Serve`, `examples/hello` |
| Functional options | `omolar.Option` |
| Table-driven tests, subtests, `t.Helper` | every `_test.go` file |
| White-box vs black-box tests | `server_test.go` (package server) vs `omolar_test.go` (package omolar_test) |
| `httptest` | `health_test.go`, `omolar_test.go` |

## Patterns used

Search the code for `PATTERN:` to find each one.

| Pattern | Where | Problem it solves |
|---|---|---|
| Facade | `omolar.App` | One simple entry point over config, logging, server and routing |
| Functional options | `omolar.Option`, `WithLogOutput` | Optional settings without constructor explosion |
| Dependency injection | `config.LookupFunc` | Testable without touching the real environment |
| Notification | `config.loader` | Report every bad setting at once |
| Strategy | `slog.Handler` in `logging.New` | Text or JSON output, chosen at runtime |
| Adapter | `http.HandlerFunc` in `server.Health` | A plain function used as an `http.Handler` |
| Constructor with safe defaults | `server.New` | Impossible to build a server with no timeouts |

## Docker

Go isn't installed on the VM; everything runs in containers.

| File | What it does |
|---|---|
| `docker-compose.yml` → `dev` | Go toolchain container. Your code is mounted in; caches live in a volume |
| `docker-compose.yml` → `app` | Runs the production image, hardened (read-only, no capabilities) |
| `Dockerfile` | Two-stage build: compile with full Go, ship only the binary on distroless |
| `server/probe.go` + `App.Healthcheck` | `/app healthcheck`: lets Docker check health without curl |

Things worth understanding:
- **Why two stages?** The Go image is ~800MB of compilers and tools. The app only needs its binary.
- **Why distroless?** No shell means an attacker who breaks in can't run `sh`, `curl` or `apt`.
- **Why `ENTRYPOINT ["/app"]` and not `ENTRYPOINT /app`?** The list form runs the app as PID 1,
  so `docker stop`'s SIGTERM reaches it and triggers the graceful shutdown you built.
- **Why `user: ${HOST_UID}`?** Files the container writes (test-report.json) belong to you, not root.
- **Why `name: omolar` instead of `container_name:`?** Fixed names like `postgres` collide
  across projects; a project name prefixes everything automatically.

## Security decisions in this milestone

- **No zero timeouts.** In `net/http`, zero means "wait forever". A slow client could tie up the server.
- **`ReadHeaderTimeout`** defends against Slowloris (trickling headers to hold connections open).
- **`DEBUG=true` is refused in production.** Debug output leaks internals.
- **`/health` reveals only status and version.** Health endpoints are usually public.
- **`.env` is git-ignored** and `.env.example` holds no secrets.
- **User input from the URL is served as `text/plain`** in the example, so it can't become HTML/JS.
- **The image runs as non-root on distroless**, read-only, with every Linux capability dropped.
- **`Probe` caps how much it reads** from the network (4KB), a habit that prevents memory exhaustion.

## Try it

```sh
make docker-test       # all tests in the dev container, race detector on
make docker-report     # writes test-report.json; send it to Claude
make docker-dev-run    # run from source: http://<vm-ip>:4001, Ctrl+C to stop
make up                # run the production image: http://<vm-ip>:4000
make docker-cover      # coverage.html: which lines do the tests never run?
docker ps              # after `make up`: watch STATUS go from (health: starting) to (healthy)
```

In GoLand: GoLand can run tests through Docker, but the simplest start is the
terminal. Run `make docker-shell`, then inside it:
`go test ./server -run TestServe_GracefulShutdown -v`. Add a
`t.Logf(...)` or `log.Println(...)` line inside the `case <-ctx.Done():` branch of
`server.go` and run it again to watch a shutdown step by step.

## Explain it back

You own this code when you can answer these without looking:

1. Why does `config.Load` let real environment variables win over the `.env` file? When would that matter in a deployment?
2. In `server.Serve`, why is a *new* context created for `Shutdown` instead of reusing `ctx`?
3. What would happen if `server.New` left `ReadTimeout` at zero, and why is that a security problem?
4. `fakeEnv` returns a function. What does that function "remember", and what's that called?
5. Why does `server.Serve` treat `http.ErrServerClosed` as success?
6. The production image has no curl. How does Docker know the app is healthy?
