// Command hello is the smallest useful Omolar app.
//
// Run it from the repo root:
//
//	make run-hello        (or: go run ./examples/hello)
//
// Then open http://localhost:4000, http://localhost:4000/hello/quinze and
// http://localhost:4000/health. Press Ctrl+C and watch the shutdown logs.
//
// TRY: copy .env.example to .env, set LOG_FORMAT=json, and run it again.
// TRY: set PORT=99999 in .env and run it. Read the error message.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaliadmen/omolar"
	"github.com/kaliadmen/omolar/config"
)

func main() {
	// GO CONCEPT: os.Exit skips deferred calls. So main() does almost
	// nothing itself: it calls run(), which can use defer freely, and only
	// main decides the exit code.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}

	app, err := omolar.New(cfg)
	if err != nil {
		return err
	}

	// `hello healthcheck` asks an already-running copy of this app whether
	// it's healthy, then exits 0 (yes) or 1 (no). Docker's HEALTHCHECK runs
	// this inside the container, because the distroless image has no curl.
	//
	// TRY: start the app in one terminal, then run
	//   go run ./examples/hello healthcheck; echo $?
	// Stop the app and run it again. Compare the exit codes.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return app.Healthcheck(ctx)
	}

	// "{$}" means "exactly /", so this doesn't also catch /anything-else.
	app.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Hello from Omolar", omolar.Version)
	})

	app.HandleFunc("GET /hello/{name}", func(w http.ResponseWriter, r *http.Request) {
		// Security: name comes straight from the URL, so it's attacker-
		// controlled. Serving it as text/plain means the browser won't run
		// it as HTML, so /hello/<script>... can't inject JavaScript (XSS).
		// When we add HTML templates, html/template escapes values for us.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Hello, %s!\n", r.PathValue("name"))
	})

	// signal.NotifyContext returns a context that is cancelled when the
	// process receives Ctrl+C (SIGINT) or SIGTERM (what Docker and systemd
	// send). Cancelling it is what triggers the graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app.Logger.Info("starting", "url", fmt.Sprintf("http://localhost:%d", cfg.Port))
	return app.Run(ctx)
}
