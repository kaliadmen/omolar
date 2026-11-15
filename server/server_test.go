package server

// These tests start real servers on real ports. Two rules keep them reliable:
//
//  1. Every goroutine a test starts is waited for before the test ends
//     (startServer's cleanup waits for Serve; client calls use a WaitGroup).
//  2. No test closes a port and then assumes it's still free. Ports come from
//     listeners the test keeps open, or from the OS via Addr ":0".
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

// listen opens a listener on a port the OS picks. The test owns it from then
// on, so no other process can take the port in between.
func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// newClient returns an HTTP client with a timeout (so a broken server fails
// the test instead of hanging it) and its own connection pool, which is
// closed when the test ends. http.Get would share one global pool across
// every test in the package.
func newClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Timeout: 5 * time.Second, Transport: tr}
}

// mustNew is New for tests: it fails the test on error.
func mustNew(t *testing.T, h http.Handler, opts Options) *Server {
	t.Helper()
	s, err := New(h, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s == nil {
		t.Fatal("New returned a nil Server")
	}
	return s
}

// testServer is a Server running in the background for one test.
type testServer struct {
	srv    *Server
	ln     net.Listener
	url    string
	client *http.Client
	cancel context.CancelFunc

	done chan struct{} // closed when Serve has returned
	err  error         // Serve's result; read it only after done is closed
}

// startServer runs Serve in a goroutine and arranges for the test to stop it
// and WAIT for it, even if the test fails early.
//
// GO CONCEPT: goroutines and channels. `go f()` runs f concurrently. A
// channel is a typed pipe goroutines use to hand values to each other.
// Closing a channel is a broadcast: every receiver wakes up.
func startServer(t *testing.T, h http.Handler, opts Options) *testServer {
	t.Helper()
	ln := listen(t)
	ts := &testServer{
		srv:    mustNew(t, h, opts),
		ln:     ln,
		url:    "http://" + ln.Addr().String(),
		client: newClient(t),
		done:   make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	ts.cancel = cancel
	go func() {
		ts.err = ts.srv.Serve(ctx, ln)
		close(ts.done) // closing happens-after the write above, so reads after <-done are safe
	}()

	// Cleanups run last-in-first-out after the test. This one stops the
	// server and blocks until the goroutine above has finished.
	t.Cleanup(func() {
		cancel()
		select {
		case <-ts.done:
		case <-time.After(10 * time.Second):
			t.Error("cleanup: Serve did not return within 10s")
		}
	})
	return ts
}

// wait blocks until Serve returns (or limit passes) and returns its error.
func (ts *testServer) wait(t *testing.T, limit time.Duration) error {
	t.Helper()
	// GO CONCEPT: select waits on several channel operations and runs
	// whichever is ready first. time.After gives us a timeout.
	select {
	case <-ts.done:
		return ts.err
	case <-time.After(limit):
		t.Fatalf("Serve did not return within %s", limit)
		return nil
	}
}

// waitFor blocks until ch is closed, or fails the test after limit.
func waitFor(t *testing.T, ch <-chan struct{}, limit time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(limit):
		t.Fatalf("timed out after %s waiting for %s", limit, what)
	}
}

// isClosed reports whether ch is closed, without waiting.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// assertListenerClosed checks Serve released the listener. A closed listener
// returns net.ErrClosed from Accept immediately; an open one would block, so
// a short deadline turns "still open" into a timeout error instead of a hang.
func assertListenerClosed(t *testing.T, ln net.Listener) {
	t.Helper()
	if tl, ok := ln.(*net.TCPListener); ok {
		tl.SetDeadline(time.Now().Add(500 * time.Millisecond))
	}
	conn, err := ln.Accept()
	if err == nil {
		conn.Close()
		t.Fatal("listener is still accepting connections")
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("listener should be closed, Accept returned: %v", err)
	}
}

func text(s string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, s) })
}

// ---- New --------------------------------------------------------------------

func TestNew_AppliesTimeoutsAndDefaults(t *testing.T) {
	// This test lives in package server (not server_test), so it can look at
	// the unexported srv field. That's called a "white-box" test.
	s := mustNew(t, http.NotFoundHandler(), Options{
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

	// Zero means "use the safe default", never "no timeout at all".
	d := mustNew(t, http.NotFoundHandler(), Options{})
	if d.srv.ReadTimeout != DefaultReadTimeout || d.srv.WriteTimeout != DefaultWriteTimeout ||
		d.srv.IdleTimeout != DefaultIdleTimeout || d.opts.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("zero options should get defaults, got %+v / shutdown %s", d.srv, d.opts.ShutdownTimeout)
	}
}

func TestNew_RejectsNegativeTimeouts(t *testing.T) {
	// Negative is never a sensible choice, so it's an error rather than being
	// silently replaced with a default (which would hide a bug in the caller).
	_, err := New(http.NotFoundHandler(), Options{
		ReadTimeout:     -1,
		WriteTimeout:    -1,
		IdleTimeout:     -1,
		ShutdownTimeout: -1,
	})
	if err == nil {
		t.Fatal("expected an error for negative timeouts")
	}
	for _, field := range []string{"ReadTimeout", "WriteTimeout", "IdleTimeout", "ShutdownTimeout"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error should mention %s:\n%v", field, err)
		}
	}
}

func TestNew_RejectsNilHandler(t *testing.T) {
	// Security: http.Server with a nil Handler silently uses the global
	// http.DefaultServeMux, which any imported package can add routes to.
	if _, err := New(nil, Options{}); err == nil {
		t.Error("expected an error for a nil handler")
	}
}

// ---- Serve: the normal lifecycle -------------------------------------------

func TestServe_HandlesRequests(t *testing.T) {
	ts := startServer(t, text("hello from omolar"), Options{})

	resp, err := ts.client.Get(ts.url + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from omolar" {
		t.Errorf("body = %q", body)
	}

	ts.cancel()
	if err := ts.wait(t, 2*time.Second); err != nil {
		t.Errorf("Serve returned %v after a clean shutdown, want nil", err)
	}
}

func TestServe_ReadyAndAddr(t *testing.T) {
	ln := listen(t)
	s := mustNew(t, text("ok"), Options{})

	if s.Addr() != nil {
		t.Errorf("Addr() before Serve = %v, want nil", s.Addr())
	}
	if isClosed(s.Ready()) {
		t.Fatal("Ready() closed before Serve was called")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }() // always join the goroutine

	waitFor(t, s.Ready(), 2*time.Second, "Ready()")
	addr := s.Addr()
	if addr == nil {
		t.Fatal("Addr() is nil after Ready()")
	}
	if addr.String() != ln.Addr().String() {
		t.Errorf("Addr() = %s, want %s", addr, ln.Addr())
	}
}

func TestServe_ClosesListenerAfterShutdown(t *testing.T) {
	ts := startServer(t, http.NotFoundHandler(), Options{})
	ts.cancel()
	if err := ts.wait(t, 2*time.Second); err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}
	// Checking the listener itself, rather than dialing the old port and
	// expecting a failure (another process could have taken the port).
	assertListenerClosed(t, ts.ln)
}

func TestServe_LogsStartAndStop(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	ts := startServer(t, http.NotFoundHandler(), Options{Logger: logger})
	waitFor(t, ts.srv.Ready(), 2*time.Second, "Ready()")
	ts.cancel()
	ts.wait(t, 2*time.Second)

	out := logs.String()
	for _, want := range []string{"server started", "addr=127.0.0.1:", "server stopped"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
}

// ---- Review finding: context cancelled before the server starts -----------

func TestServe_ContextAlreadyCancelled(t *testing.T) {
	var logs syncBuffer
	ln := listen(t)
	s := mustNew(t, text("ok"), Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Serve is even called

	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	select {
	case err := <-done:
		// Cancelling means "stop", and there was nothing to stop: that's a
		// clean outcome, the same as cancelling a running server.
		if err != nil {
			t.Errorf("Serve returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return promptly for a cancelled context")
	}

	assertListenerClosed(t, ln) // the listener it was given is released
	if strings.Contains(logs.String(), "server started") {
		t.Errorf("should not claim it started:\n%s", logs.String())
	}
	if isClosed(s.Ready()) {
		t.Error("Ready() closed, but the server never accepted connections")
	}
}

func TestListenAndServe_ContextAlreadyCancelled(t *testing.T) {
	s := mustNew(t, text("ok"), Options{Addr: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.ListenAndServe(ctx); err != nil {
		t.Errorf("ListenAndServe returned %v, want nil", err)
	}
	if s.Addr() != nil {
		t.Errorf("it should not have opened a port, but Addr() = %v", s.Addr())
	}
}

// ---- Review finding: shutdown when requests outlive the deadline -----------

func TestServe_GracefulShutdownLetsInFlightRequestsFinish(t *testing.T) {
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)                     // tell the test we're mid-request
		time.Sleep(300 * time.Millisecond) // simulate slow work
		io.WriteString(w, "finished")
	})
	ts := startServer(t, h, Options{ShutdownTimeout: 5 * time.Second})

	// Fire the request in the background so we can shut down while it runs.
	// GO CONCEPT: sync.WaitGroup counts running goroutines; wg.Go (Go 1.25+)
	// starts one and counts it, and wg.Wait blocks until all have finished.
	var wg sync.WaitGroup
	var body string
	var reqErr error
	wg.Go(func() {
		resp, err := ts.client.Get(ts.url + "/slow")
		if err != nil {
			reqErr = err
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		body, reqErr = string(b), err
	})

	waitFor(t, started, 3*time.Second, "the handler to start")
	ts.cancel() // ask the server to shut down mid-request
	wg.Wait()

	if reqErr != nil {
		t.Fatalf("in-flight request failed during shutdown: %v", reqErr)
	}
	if body != "finished" {
		t.Errorf("body = %q, want the handler to complete", body)
	}
	if err := ts.wait(t, 5*time.Second); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

func TestServe_ShutdownDeadlineExceeded(t *testing.T) {
	started := make(chan struct{})
	handlerExited := make(chan struct{})

	// A handler that only stops when its request context is cancelled, like
	// a well-behaved long-poll or a slow database query using ctx.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerExited)
		close(started)
		<-r.Context().Done()
	})
	ts := startServer(t, h, Options{ShutdownTimeout: 100 * time.Millisecond})

	var wg sync.WaitGroup
	var reqErr error
	wg.Go(func() {
		resp, err := ts.client.Get(ts.url + "/stuck")
		if err == nil {
			resp.Body.Close()
		}
		reqErr = err
	})
	waitFor(t, started, 3*time.Second, "the handler to start")

	begin := time.Now()
	ts.cancel()
	err := ts.wait(t, 3*time.Second)

	// 1. Serve reports that it had to give up.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Serve returned %v, want context.DeadlineExceeded", err)
	}
	// 2. It gave up on time.
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s; it should give up after ~100ms", elapsed)
	}
	// 3. The stuck handler was told to stop, via its request context.
	waitFor(t, handlerExited, 2*time.Second, "the stuck handler to see its context cancelled")
	// 4. The connection was really closed: the client got an error, not a hang.
	wg.Wait()
	if reqErr == nil {
		t.Error("the client should see its request fail when the server force-closes")
	}
	// 5. The listener was released.
	assertListenerClosed(t, ts.ln)
}

// ---- Review finding: termination racing with cancellation -----------------

func TestServe_ListenerFailureIsAnError(t *testing.T) {
	ts := startServer(t, http.NotFoundHandler(), Options{})
	waitFor(t, ts.srv.Ready(), 2*time.Second, "Ready()")

	ts.ln.Close() // the listener dies; nobody cancelled the context

	err := ts.wait(t, 2*time.Second)
	if err == nil {
		t.Fatal("Serve returned nil, but the listener failed: that's an error")
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("error should wrap the listener's error, got %v", err)
	}
}

func TestServe_ListenerFailureStillDrainsRequests(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, "finished")
		close(finished)
	})
	ts := startServer(t, h, Options{ShutdownTimeout: 5 * time.Second})

	var wg sync.WaitGroup
	var body string
	wg.Go(func() {
		resp, err := ts.client.Get(ts.url + "/slow")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
	})
	waitFor(t, started, 3*time.Second, "the handler to start")

	ts.ln.Close()
	ts.wait(t, 5*time.Second)

	// Serve must not return while a request is still running: returning
	// early would abandon that connection and the goroutine serving it.
	if !isClosed(finished) {
		t.Error("Serve returned while a request was still in flight")
	}
	wg.Wait()
	if body != "finished" {
		t.Errorf("in-flight request was cut off (body %q); a listener failure should still drain", body)
	}
}

func TestServe_CancelThenListenerCloseIsClean(t *testing.T) {
	// Cancellation that happens first wins: once the caller has asked the
	// server to stop, a listener error that follows is just part of stopping.
	for i := range 20 {
		ts := startServer(t, http.NotFoundHandler(), Options{})
		waitFor(t, ts.srv.Ready(), 2*time.Second, "Ready()")
		ts.cancel()
		ts.ln.Close()
		if err := ts.wait(t, 2*time.Second); err != nil {
			t.Fatalf("run %d: Serve returned %v, want nil", i, err)
		}
	}
}

func TestServe_ConcurrentCancelAndListenerClose(t *testing.T) {
	// Cancel and close at the same instant, many times. Run with -race. Serve
	// must always return, and only ever with one of the two documented results.
	for i := range 50 {
		ts := startServer(t, http.NotFoundHandler(), Options{})
		waitFor(t, ts.srv.Ready(), 2*time.Second, "Ready()")

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() { <-start; ts.cancel() })
		wg.Go(func() { <-start; ts.ln.Close() })
		close(start) // release both goroutines at once
		wg.Wait()

		err := ts.wait(t, 2*time.Second)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("run %d: Serve returned %v, want nil or a closed-listener error", i, err)
		}
	}
}

// ---- A Server runs once ----------------------------------------------------

func TestServe_SecondCallIsRejected(t *testing.T) {
	s := mustNew(t, text("ok"), Options{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Serve(ctx, listen(t)) // first run (ends immediately)

	ln2 := listen(t)
	err := s.Serve(context.Background(), ln2)
	if !errors.Is(err, ErrServerUsed) {
		t.Errorf("second Serve returned %v, want ErrServerUsed", err)
	}
	assertListenerClosed(t, ln2) // even a rejected listener is released
}

func TestServe_ConcurrentCallsOnlyOneRuns(t *testing.T) {
	s := mustNew(t, text("ok"), Options{})
	ctx, cancel := context.WithCancel(context.Background())

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		ln := listen(t)
		wg.Go(func() { results <- s.Serve(ctx, ln) })
	}

	// One call must be rejected straight away; the other is now serving.
	select {
	case err := <-results:
		if !errors.Is(err, ErrServerUsed) {
			t.Errorf("first result = %v, want ErrServerUsed", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("neither call was rejected")
	}
	cancel()
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("the running Serve returned %v, want nil", err)
		}
	}
}

func TestServe_NilListener(t *testing.T) {
	s := mustNew(t, text("ok"), Options{})

	// GO CONCEPT: recover. A panic unwinds the stack running deferred calls;
	// recover() inside a deferred function stops it and returns the panic
	// value. Here it turns "Serve crashed" into a normal test failure instead
	// of killing every other test in the package.
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("Serve panicked on a nil listener (%v); it should return an error", p)
		}
	}()
	if err := s.Serve(context.Background(), nil); err == nil {
		t.Error("expected an error for a nil listener")
	}
}

// ---- ListenAndServe --------------------------------------------------------

func TestListenAndServe_BadAddressFailsFast(t *testing.T) {
	s := mustNew(t, http.NotFoundHandler(), Options{Addr: "not-an-address:abc"})
	if err := s.ListenAndServe(context.Background()); err == nil {
		t.Fatal("expected an error for an invalid address")
	}
}

func TestListenAndServe_OSPicksThePort(t *testing.T) {
	// Port 0 asks the OS for a free port at the moment of listening, so
	// there's no window where another process could grab it.
	s := mustNew(t, text("ok"), Options{Addr: "127.0.0.1:0"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()

	stopped := false
	t.Cleanup(func() { // join the goroutine even if the test fails early
		if !stopped {
			cancel()
			<-done
		}
	})

	waitFor(t, s.Ready(), 2*time.Second, "Ready()")
	addr := s.Addr()
	if addr == nil {
		t.Fatal("Addr() is nil after Ready()")
	}

	resp, err := newClient(t).Get("http://" + addr.String())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	cancel()
	err = <-done
	stopped = true
	if err != nil {
		t.Errorf("ListenAndServe returned %v, want nil", err)
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
