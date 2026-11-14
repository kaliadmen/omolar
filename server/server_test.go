package server

// These tests start real servers on real (random) ports. A few involve
// timing; they use generous limits so they don't flake on a slow machine.
//
// TRY: run with -v to watch the start/stop log lines:
//   go test ./server -run TestServe_GracefulShutdown -v

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// client has a timeout so a broken server makes tests fail instead of hang.
// (http.Get uses a client with NO timeout by default, worth remembering.)
var client = &http.Client{Timeout: 5 * time.Second}

// waitFor blocks until ch is closed, or fails the test after limit.
func waitFor(t *testing.T, ch <-chan struct{}, limit time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(limit):
		t.Fatalf("timed out after %s waiting for %s", limit, what)
	}
}

// listen opens a listener on a random free port. Port 0 asks the OS to pick
// one, so tests never collide with each other or with apps you're running.
func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// startServer runs Serve in the background and returns the base URL, a
// cancel function that triggers shutdown, and a channel that receives
// Serve's return value.
//
// GO CONCEPT: goroutines and channels. `go f()` runs f concurrently. A
// channel is a typed pipe goroutines use to hand values to each other;
// here the background goroutine sends Serve's error back to the test.
func startServer(t *testing.T, h http.Handler, opts Options) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ln := listen(t)
	s := New(h, opts)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1) // buffered so the goroutine never blocks
	go func() {
		done <- s.Serve(ctx, ln)
	}()
	t.Cleanup(cancel) // make sure the server stops even if the test fails early

	return "http://" + ln.Addr().String(), cancel, done
}

// waitErr waits for Serve to return, failing the test if it takes too long.
func waitErr(t *testing.T, done <-chan error, limit time.Duration) error {
	t.Helper()
	// GO CONCEPT: select waits on several channel operations and runs
	// whichever is ready first. time.After gives us a timeout.
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("Serve did not return within %s", limit)
		return nil
	}
}

func TestServe_HandlesRequests(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello from omolar")
	})
	url, cancel, done := startServer(t, h, Options{})

	resp, err := client.Get(url + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "hello from omolar" {
		t.Errorf("body = %q", body)
	}

	cancel()
	if err := waitErr(t, done, 2*time.Second); err != nil {
		t.Errorf("Serve returned %v after a clean shutdown, want nil", err)
	}
}

func TestServe_GracefulShutdownLetsInFlightRequestsFinish(t *testing.T) {
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)                     // tell the test we're mid-request
		time.Sleep(300 * time.Millisecond) // simulate slow work
		io.WriteString(w, "finished")
	})
	url, cancel, done := startServer(t, h, Options{ShutdownTimeout: 5 * time.Second})

	// Fire the request in the background so we can shut down while it runs.
	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := client.Get(url + "/slow")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		resCh <- result{body: string(b), err: err}
	}()

	waitFor(t, started, 3*time.Second, "the handler to start") // now mid-request
	cancel()                                                   // ask the server to shut down mid-request

	res := <-resCh
	if res.err != nil {
		t.Fatalf("in-flight request failed during shutdown: %v", res.err)
	}
	if res.body != "finished" {
		t.Errorf("body = %q, want the handler to complete", res.body)
	}
	if err := waitErr(t, done, 5*time.Second); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

func TestServe_ShutdownTimeoutGivesUp(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})

	// This handler won't finish until the test ends.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	})
	url, cancel, done := startServer(t, h, Options{ShutdownTimeout: 100 * time.Millisecond})

	go client.Get(url + "/stuck") //nolint:errcheck // we only care that it's in flight
	waitFor(t, started, 3*time.Second, "the handler to start")

	begin := time.Now()
	cancel()
	err := waitErr(t, done, 3*time.Second)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Serve returned %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s; it should give up after ~100ms", elapsed)
	}
}

func TestServe_RefusesConnectionsAfterShutdown(t *testing.T) {
	url, cancel, done := startServer(t, http.NotFoundHandler(), Options{})
	cancel()
	waitErr(t, done, 2*time.Second)

	quick := &http.Client{Timeout: time.Second}
	if _, err := quick.Get(url); err == nil {
		t.Error("expected the request to fail after shutdown")
	}
}

func TestNew_AppliesTimeoutsAndDefaults(t *testing.T) {
	// This test lives in package server (not server_test), so it can look at
	// the unexported srv field. That's called a "white-box" test.
	s := New(http.NotFoundHandler(), Options{
		Addr:         ":9999",
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 2 * time.Second,
		IdleTimeout:  3 * time.Second,
	})
	if s.srv.Addr != ":9999" || s.srv.ReadTimeout != time.Second ||
		s.srv.WriteTimeout != 2*time.Second || s.srv.IdleTimeout != 3*time.Second {
		t.Errorf("timeouts not applied: %+v", s.srv)
	}
	// Security: without ReadHeaderTimeout a client can open a connection and
	// send headers one byte at a time forever ("Slowloris" attack).
	if s.srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set to defend against slowloris")
	}
	if s.srv.Handler == nil {
		t.Error("handler not set")
	}

	// Zero options get safe defaults rather than "no timeout at all".
	d := New(http.NotFoundHandler(), Options{})
	if d.srv.ReadTimeout <= 0 || d.srv.WriteTimeout <= 0 || d.srv.IdleTimeout <= 0 || d.opts.ShutdownTimeout <= 0 {
		t.Errorf("zero options should get defaults, got %+v / shutdown %s", d.srv, d.opts.ShutdownTimeout)
	}
}

// syncBuffer is a bytes.Buffer safe to write from several goroutines.
// The server logs from its own goroutine while the test reads the buffer,
// and the race detector (go test -race) would flag a plain Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServe_LogsStartAndStop(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	_, cancel, done := startServer(t, http.NotFoundHandler(), Options{Logger: logger})
	cancel()
	waitErr(t, done, 2*time.Second)

	out := logs.String()
	for _, want := range []string{"server started", "addr=127.0.0.1:", "server stopped"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
}

func TestListenAndServe_BadAddressFailsFast(t *testing.T) {
	s := New(http.NotFoundHandler(), Options{Addr: "not-an-address:abc"})
	err := s.ListenAndServe(context.Background())
	if err == nil {
		t.Fatal("expected an error for an invalid address")
	}
}

func TestListenAndServe_ServesUntilCancelled(t *testing.T) {
	// Grab a free port, release it, then ask ListenAndServe to use it.
	ln := listen(t)
	addr := ln.Addr().String()
	ln.Close()

	s := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}), Options{Addr: addr})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()

	// The server needs a moment to start listening; retry briefly.
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		resp, err = client.Get("http://" + addr)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	resp.Body.Close()

	cancel()
	if err := waitErr(t, done, 2*time.Second); err != nil {
		t.Errorf("ListenAndServe returned %v, want nil", err)
	}
}
