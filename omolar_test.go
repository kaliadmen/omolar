// GO CONCEPT: black-box tests. This file says `package omolar_test`, not
// `package omolar`. That makes it a separate package that can only use
// exported names, exactly like a real user of the framework would.
package omolar_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaliadmen/omolar"
	"github.com/kaliadmen/omolar/config"
)

// testConfig returns the default config, as if no settings were provided.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.FromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	return cfg
}

func newApp(t *testing.T, logs io.Writer) *omolar.App {
	t.Helper()
	app, err := omolar.New(testConfig(t), omolar.WithLogOutput(logs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if app == nil {
		t.Fatal("New returned nil app")
	}
	return app
}

func TestNew_HasHealthEndpoint(t *testing.T) {
	app := newApp(t, io.Discard)

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

func TestHandleFunc_RegistersRoutes(t *testing.T) {
	app := newApp(t, io.Discard)
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
	app := newApp(t, io.Discard)
	app.Handle("GET /teapot", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/teapot", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
}

func TestNew_LoggerUsesConfig(t *testing.T) {
	var logs bytes.Buffer
	cfg := testConfig(t)
	cfg.AppName = "pdpanel"
	cfg.LogFormat = "json"

	app, err := omolar.New(cfg, omolar.WithLogOutput(&logs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.Logger.Info("ping")

	if !strings.Contains(logs.String(), `"app":"pdpanel"`) {
		t.Errorf("log line should be JSON tagged with the app name: %s", logs.String())
	}
}

func TestNew_RejectsBadLogSettings(t *testing.T) {
	cfg := testConfig(t)
	cfg.LogFormat = "xml" // bypassing config validation on purpose
	if _, err := omolar.New(cfg, omolar.WithLogOutput(io.Discard)); err == nil {
		t.Error("expected an error for an invalid log format")
	}
}

func TestServe_EndToEnd(t *testing.T) {
	app := newApp(t, io.Discard)
	app.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "home")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, ln) }()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "home" {
		t.Errorf("body = %q, want home", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop after cancel")
	}
}
