// GO CONCEPT: black-box tests. This file says `package omolar_test`, not
// `package omolar`. That makes it a separate package that can only use
// exported names, exactly like a real user of the framework would.
package omolar_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaliadmen/omolar"
	"github.com/kaliadmen/omolar/config"
)

func newApp(t *testing.T, cfg config.Config, logs io.Writer) *omolar.App {
	t.Helper()
	app, err := omolar.New(cfg, omolar.WithLogOutput(logs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if app == nil {
		t.Fatal("New returned nil app")
	}
	return app
}

// listen opens a listener on a port the OS picks, held by the test.
func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func portOf(ln net.Listener) int {
	return ln.Addr().(*net.TCPAddr).Port
}

// serveApp runs app.Serve on ln in the background. The returned stop
// function cancels it and waits for it, returning Serve's error. It's also
// registered as a cleanup, so the goroutine is joined even if the test fails.
func serveApp(t *testing.T, app *omolar.App, ln net.Listener) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, ln) }()

	var once sync.Once
	var result error
	stop = func() error {
		// sync.Once makes stop safe to call twice: once by the test, and
		// again by the cleanup below.
		once.Do(func() {
			cancel()
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				result = fmt.Errorf("Serve did not stop within 10s")
			}
		})
		return result
	}
	t.Cleanup(func() {
		if err := stop(); err != nil && strings.Contains(err.Error(), "did not stop") {
			t.Error(err)
		}
	})
	return stop
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Timeout: 5 * time.Second, Transport: tr}
}

// fetch makes a GET request and returns the status and body. It returns an
// error instead of failing the test, so it's safe to call from goroutines.
// (t.Fatal may only be called from the test's own goroutine.)
func fetch(c *http.Client, url string) (int, string, error) {
	resp, err := c.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// get is fetch for the test goroutine: any error fails the test.
func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	code, body, err := fetch(c, url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return code, body
}

// ---- New ---------------------------------------------------------------------

func TestNew_HasHealthEndpoint(t *testing.T) {
	app := newApp(t, config.Default(), io.Discard)

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body is not JSON: %q", rec.Body.String())
	}
	if body["version"] != omolar.Version {
		t.Errorf("version = %q, want %q", body["version"], omolar.Version)
	}
}

func TestNew_RejectsInvalidConfig(t *testing.T) {
	// Review finding 1: a Config built in code must face the same rules as one
	// loaded from .env. New is the gate every app goes through.
	cases := []struct {
		name   string
		change func(c *config.Config)
		want   string
	}{
		{"zero Config", func(c *config.Config) { *c = config.Config{} }, "PORT"},
		{"zero port", func(c *config.Config) { c.Port = 0 }, "PORT"},
		{"negative read timeout", func(c *config.Config) { c.ReadTimeout = -time.Second }, "READ_TIMEOUT"},
		{"zero shutdown timeout", func(c *config.Config) { c.ShutdownTimeout = 0 }, "SHUTDOWN_TIMEOUT"},
		{"debug in production", func(c *config.Config) { c.Env = "production"; c.Debug = true }, "DEBUG"},
		{"bad log format", func(c *config.Config) { c.LogFormat = "xml" }, "LOG_FORMAT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.change(&cfg)
			app, err := omolar.New(cfg, omolar.WithLogOutput(io.Discard))
			if err == nil {
				t.Fatal("expected an error")
			}
			if app != nil {
				t.Error("New should not return an app alongside an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %s:\n%v", tc.want, err)
			}
		})
	}
}

func TestNew_LoggerUsesConfig(t *testing.T) {
	var logs bytes.Buffer
	cfg := config.Default()
	cfg.AppName = "pdpanel"
	cfg.LogFormat = "json"

	app := newApp(t, cfg, &logs)
	app.Logger().Info("ping")

	if !strings.Contains(logs.String(), `"app":"pdpanel"`) {
		t.Errorf("log line should be JSON tagged with the app name: %s", logs.String())
	}
}

// ---- State can't be changed from outside after New -------------------------

func TestConfig_ReturnsTheValidatedConfig(t *testing.T) {
	cfg := config.Default()
	cfg.AppName = "panel"
	cfg.Port = 8123
	app := newApp(t, cfg, io.Discard)

	if got := app.Config(); got != cfg {
		t.Errorf("Config() =\n  %+v\nwant\n  %+v", got, cfg)
	}
}

func TestConfig_IsACopy(t *testing.T) {
	app := newApp(t, config.Default(), io.Discard)

	// Changing the returned value must not reach inside the app, or code
	// could skip validation by editing the config after New checked it.
	c := app.Config()
	c.Port = 0
	c.ShutdownTimeout = -time.Second

	if app.Config().Port != config.Default().Port {
		t.Error("changing the copy changed the app's config")
	}
}

func TestNew_CallerChangesAfterNewDontLeakIn(t *testing.T) {
	cfg := config.Default()
	app := newApp(t, cfg, io.Discard)
	cfg.Port = 0 // the caller keeps editing their variable

	if app.Config().Port != config.Default().Port {
		t.Error("the app should hold its own copy of the config")
	}
}

// ---- Routing -------------------------------------------------------------------

func TestHandleFunc_RegistersRoutes(t *testing.T) {
	app := newApp(t, config.Default(), io.Discard)
	app.HandleFunc("GET /hello/{name}", func(w http.ResponseWriter, r *http.Request) {
		// r.PathValue reads a {wildcard} from the pattern (Go 1.22+).
		io.WriteString(w, "hi "+r.PathValue("name"))
	})

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/quinze", nil))
	if rec.Body.String() != "hi quinze" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hi quinze")
	}
}

func TestHandle_RegistersHandlers(t *testing.T) {
	app := newApp(t, config.Default(), io.Discard)
	app.Handle("GET /teapot", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/teapot", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
}

// ---- Serving -------------------------------------------------------------------

func TestServe_EndToEnd(t *testing.T) {
	app := newApp(t, config.Default(), io.Discard)
	app.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "home")
	})

	ln := listen(t)
	stop := serveApp(t, app, ln)

	if code, body := get(t, newClient(t), "http://"+ln.Addr().String()+"/"); code != 200 || body != "home" {
		t.Errorf("GET / = %d %q, want 200 home", code, body)
	}
	if err := stop(); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

func TestServe_CanRunAgainAfterStopping(t *testing.T) {
	// Each Run/Serve builds a fresh server, so an App can be stopped and
	// started again (a single server.Server only runs once).
	app := newApp(t, config.Default(), io.Discard)
	client := newClient(t)

	for i := range 2 {
		ln := listen(t)
		stop := serveApp(t, app, ln)
		if code, _ := get(t, client, "http://"+ln.Addr().String()+"/health"); code != 200 {
			t.Errorf("run %d: /health = %d", i, code)
		}
		if err := stop(); err != nil {
			t.Errorf("run %d: Serve returned %v", i, err)
		}
	}
}

// ---- Review finding: independent instances share no mutable state ----------

func TestApps_AreIndependent(t *testing.T) {
	var logsOne, logsTwo syncBuffer

	cfgOne := config.Default()
	cfgOne.AppName = "one"
	cfgTwo := config.Default()
	cfgTwo.AppName = "two"

	one := newApp(t, cfgOne, &logsOne)
	two := newApp(t, cfgTwo, &logsTwo)

	// A route on one app must not appear on the other.
	one.HandleFunc("GET /only-one", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "one")
	})

	lnOne, lnTwo := listen(t), listen(t)
	stopOne := serveApp(t, one, lnOne)
	stopTwo := serveApp(t, two, lnTwo)
	urlOne := "http://" + lnOne.Addr().String()
	urlTwo := "http://" + lnTwo.Addr().String()
	client := newClient(t)

	// Hit both at the same time from several goroutines (run with -race).
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			code, body, err := fetch(client, urlOne+"/only-one")
			if err != nil || code != 200 || body != "one" {
				t.Errorf("app one /only-one = %d %q %v", code, body, err)
			}
			code, _, err = fetch(client, urlTwo+"/only-one")
			if err != nil || code != http.StatusNotFound {
				t.Errorf("app two /only-one = %d %v, want 404: routes leaked between apps", code, err)
			}
		})
	}
	wg.Wait()

	// Each app logs only to its own writer, tagged with its own name.
	one.Logger().Info("hello")
	two.Logger().Info("hello")
	if strings.Contains(logsOne.String(), "app=two") || !strings.Contains(logsOne.String(), "app=one") {
		t.Errorf("app one's logs are mixed up:\n%s", logsOne.String())
	}
	if strings.Contains(logsTwo.String(), "app=one") || !strings.Contains(logsTwo.String(), "app=two") {
		t.Errorf("app two's logs are mixed up:\n%s", logsTwo.String())
	}

	// Stopping one leaves the other running.
	if err := stopOne(); err != nil {
		t.Errorf("stopping app one: %v", err)
	}
	if code, _ := get(t, client, urlTwo+"/health"); code != 200 {
		t.Errorf("app two stopped serving when app one stopped (got %d)", code)
	}
	if err := stopTwo(); err != nil {
		t.Errorf("stopping app two: %v", err)
	}

	// Config values stayed separate throughout.
	if one.Config().AppName != "one" || two.Config().AppName != "two" {
		t.Errorf("configs mixed up: %q, %q", one.Config().AppName, two.Config().AppName)
	}
}

// ---- Healthcheck ---------------------------------------------------------------

func TestHealthcheck_RunningApp(t *testing.T) {
	// The listener is opened FIRST and kept open, and the app is configured
	// with its port. Nothing else can take the port in between.
	ln := listen(t)
	cfg := config.Default()
	cfg.Host = "127.0.0.1"
	cfg.Port = portOf(ln)
	app := newApp(t, cfg, io.Discard)
	serveApp(t, app, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Healthcheck(ctx); err != nil {
		t.Errorf("Healthcheck on a running app returned %v, want nil", err)
	}
}

func TestHealthcheck_EmptyHostProbesLoopback(t *testing.T) {
	// HOST is empty by default (listen on all interfaces, the Docker case).
	// The probe must then go to 127.0.0.1, which reaches the app.
	ln := listen(t)
	cfg := config.Default()
	cfg.Port = portOf(ln)
	app := newApp(t, cfg, io.Discard)
	serveApp(t, app, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Healthcheck(ctx); err != nil {
		t.Errorf("Healthcheck with empty HOST: %v", err)
	}
}

func TestHealthcheck_AppNotServing(t *testing.T) {
	// Something holds the port but isn't the app: it accepts connections and
	// hangs up. The healthcheck must say "unhealthy". We hold this listener
	// ourselves rather than assuming a closed port stays unused.
	ln := listen(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done })

	cfg := config.Default()
	cfg.Port = portOf(ln)
	app := newApp(t, cfg, io.Discard)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := app.Healthcheck(ctx); err == nil {
		t.Error("expected an error when the app isn't serving")
	}
}

// syncBuffer is a bytes.Buffer safe to write from several goroutines.
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
