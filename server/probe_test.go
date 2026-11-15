package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// GO CONCEPT: httptest.NewServer starts a real HTTP server on a random local
// port, so Probe makes genuine network requests in these tests.

func TestProbe_HealthyEndpoint(t *testing.T) {
	ts := httptest.NewServer(Health("1.0.0"))
	defer ts.Close()

	if err := Probe(context.Background(), ts.URL+"/health"); err != nil {
		t.Errorf("Probe on a healthy endpoint returned %v, want nil", err)
	}
}

func TestProbe_Unhealthy(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string // fragment expected in the error
	}{
		{
			name: "server error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			want: "503",
		},
		{
			name: "not JSON",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("<html>hi</html>"))
			},
			want: "status",
		},
		{
			name: "status is not ok",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"status":"degraded"}`))
			},
			want: "degraded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()

			err := Probe(context.Background(), ts.URL)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestProbe_ConnectionDropped(t *testing.T) {
	// A listener the test keeps open, which accepts each connection and
	// hangs up straight away: an app that's crashing or not speaking HTTP.
	// (Closing a port and hoping nothing else grabs it would be flaky.)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed at the end of the test
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done }) // stop and join the goroutine

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Probe(ctx, "http://"+ln.Addr().String()+"/health"); err == nil {
		t.Error("expected an error when the connection is dropped")
	}
}

func TestProbe_DoesNotFollowRedirects(t *testing.T) {
	// A health endpoint that redirects is misconfigured. Following the
	// redirect could report "healthy" based on a different app entirely.
	healthy := httptest.NewServer(Health("1"))
	defer healthy.Close()
	redirecting := httptest.NewServer(http.RedirectHandler(healthy.URL, http.StatusFound))
	defer redirecting.Close()

	err := Probe(context.Background(), redirecting.URL)
	if err == nil {
		t.Fatal("expected an error for a redirect")
	}
	if !strings.Contains(err.Error(), "302") {
		t.Errorf("error should mention the 302 status, got: %v", err)
	}
}

func TestProbe_RespectsContextTimeout(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the test ends
	}))
	// Cleanup runs last-registered-first: release the handler, then close.
	defer ts.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	begin := time.Now()
	err := Probe(ctx, ts.URL)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	// Docker kills healthchecks that take too long; Probe must give up on time.
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("Probe took %s, should stop at the context deadline (~100ms)", elapsed)
	}
}
