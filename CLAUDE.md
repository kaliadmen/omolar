# Working on Omolar

Omolar is a security-minded Go web framework. The owner (Quinze) is relearning Go by
**reading and experimenting with** the code, so the code doubles as teaching material.

## Workflow rules

- **TDD, always.** Each feature lands as separate commits:
  1. `test(<pkg>): ...` — tests plus stub signatures that compile but fail (red).
     Stubs return zero values / `errNotImplemented`; never `panic`, so the test
     output stays readable.
  2. `feat(<pkg>): ...` — the minimum code to make the tests pass (green).
  3. `refactor(<pkg>): ...` — optional cleanup with tests still green.
- One branch + pull request per milestone. Never commit straight to `main` after the
  initial scaffold.
- Prefer the standard library. Every new dependency needs a reason in the PR.
- Data structures and algorithms are built by hand where they teach something
  (router trie, LRU cache, rate limiter, heap, ring buffer) — each in its own package
  with its own tests first.

## Comment style

- Explain **why**, not just what. The first time a Go concept appears in the codebase
  (closures, interfaces, `defer`, goroutines, channels, contexts, generics...), add a
  short `// GO CONCEPT:` comment explaining it.
- Add `// TRY:` comments with small experiments the reader can do (change a value,
  delete a line, run a specific test) and what they should observe.
- Add a `// PATTERN:` comment wherever a design pattern is used: name it, say what
  problem it solves here, and what the code would look like without it. Use patterns
  only when they solve a real problem, the Go way: small interfaces defined where
  they're used, composition over inheritance, no global singletons.
- Every exported identifier gets a godoc comment.

## Each milestone ends with

- `docs/learning/NN-name.md`: concepts used, patterns used, suggested reading order
  through the files, and 3–5 "explain it back" questions.
- `make report` passing, which writes `test-report.json`.

## Environment

The owner develops with GoLand over SSH into an Ubuntu VM and runs **everything in
Docker**; Go is not installed on the VM. Every workflow must work through
`docker compose` / the `make docker-*` targets. New services (databases, Redis...)
go in `docker-compose.yml`, never "install X on the server".

## Commands

On the VM (Docker):

- `make docker-test` — all tests with the race detector, in the dev container
- `make docker-report` — writes `test-report.json`
- `make docker-shell` — shell in the dev container
- `make up` / `make down` — run / stop the production image

Inside the dev container or CI (plain Go): `make test`, `make report`, `make cover`, `make vet`.
