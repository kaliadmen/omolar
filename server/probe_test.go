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

func TestProbe_NothingListening(t *testing.T) {
	// Grab a free port and close it again, so nothing is listening there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	if err := Probe(context.Background(), "http://"+addr+"/health"); err == nil {
		t.Error("expected an error when nothing is listening")
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
