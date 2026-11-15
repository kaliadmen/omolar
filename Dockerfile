# syntax=docker/dockerfile:1
#
# Production image for an Omolar app (examples/hello by default).
#
# Two stages:
#   1. "build" uses the full Go image (~800MB) to compile a static binary.
#   2. The final image is distroless: just the binary plus CA certificates and
#      timezone data (~10-15MB). No shell, no package manager, no curl. If an
#      attacker gets code execution, there are almost no tools to use.
#
# Build another app:  docker build --build-arg APP=./path/to/app -t myapp .

ARG GO_VERSION=1.24

# ---- Stage 1: build ---------------------------------------------------------
FROM golang:${GO_VERSION} AS build
WORKDIR /src

# Copy only the module files first and download dependencies. Docker caches
# each step, so this layer is reused until go.mod/go.sum change, which makes
# rebuilds after code edits much faster.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG APP=./examples/hello
# CGO_ENABLED=0  -> a fully static binary (no C libraries needed at runtime)
# -trimpath      -> strips your local file paths from the binary
# -ldflags -s -w -> drops debug symbols, making the binary smaller
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ${APP}

# ---- Stage 2: run -----------------------------------------------------------
# The :nonroot tag runs as user 65532, never root.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/app /app

USER nonroot:nonroot
EXPOSE 4000

# Docker runs `/app healthcheck` periodically; the container shows as
# "healthy" or "unhealthy" in `docker ps`.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --start-interval=2s --retries=3 \
    CMD ["/app", "healthcheck"]

# Exec form (a JSON list) makes the app PID 1, so it receives SIGTERM from
# `docker stop` directly and shuts down gracefully.
ENTRYPOINT ["/app"]
