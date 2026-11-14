package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEnv turns a map into a LookupFunc, so tests never touch the real
// environment.
//
// GO CONCEPT: closures. The function we return "closes over" the variable m:
// it keeps a reference to it and can use it later, after fakeEnv has returned.
func fakeEnv(m map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func TestFromLookup_Defaults(t *testing.T) {
	cfg, err := FromLookup(fakeEnv(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := Config{
		AppName:         "omolar",
		Env:             "development",
		Debug:           false,
		Host:            "",
		Port:            4000,
		ReadTimeout:     15 * time.Second,
		WriteTimeout:    30 * time.Second,
		IdleTimeout:     60 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
		LogFormat:       "text",
	}
	if cfg != want {
		t.Errorf("defaults =\n  %+v\nwant\n  %+v", cfg, want)
	}
}

func TestFromLookup_Overrides(t *testing.T) {
	cfg, err := FromLookup(fakeEnv(map[string]string{
		"APP_NAME":         "pdpanel",
		"APP_ENV":          "test",
		"DEBUG":            "true",
		"HOST":             "127.0.0.1",
		"PORT":             "8080",
		"READ_TIMEOUT":     "5s",
		"WRITE_TIMEOUT":    "1m",
		"IDLE_TIMEOUT":     "90s",
		"SHUTDOWN_TIMEOUT": "2s",
		"LOG_LEVEL":        "debug",
		"LOG_FORMAT":       "json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := Config{
		AppName:         "pdpanel",
		Env:             "test",
		Debug:           true,
		Host:            "127.0.0.1",
		Port:            8080,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    time.Minute,
		IdleTimeout:     90 * time.Second,
		ShutdownTimeout: 2 * time.Second,
		LogLevel:        "debug",
		LogFormat:       "json",
	}
	if cfg != want {
		t.Errorf("config =\n  %+v\nwant\n  %+v", cfg, want)
	}
}

func TestFromLookup_EmptyValueMeansDefault(t *testing.T) {
	// .env.example files often contain lines like "PORT=" as placeholders.
	// An empty value should behave like a missing one.
	cfg, err := FromLookup(fakeEnv(map[string]string{"PORT": "", "LOG_LEVEL": "  "}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 4000 || cfg.LogLevel != "info" {
		t.Errorf("empty values should fall back to defaults, got %+v", cfg)
	}
}

func TestFromLookup_ValuesAreCaseInsensitiveWhereSensible(t *testing.T) {
	cfg, err := FromLookup(fakeEnv(map[string]string{
		"APP_ENV":    "Production",
		"LOG_LEVEL":  "WARN",
		"LOG_FORMAT": "JSON",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "production" || cfg.LogLevel != "warn" || cfg.LogFormat != "json" {
		t.Errorf("values were not normalised to lower case: %+v", cfg)
	}
}

func TestFromLookup_ReportsAllProblemsAtOnce(t *testing.T) {
	// Five mistakes. A good config loader tells you about all of them in one
	// go, so you don't fix one, restart, and discover the next.
	_, err := FromLookup(fakeEnv(map[string]string{
		"PORT":         "99999",
		"DEBUG":        "maybe",
		"READ_TIMEOUT": "fast",
		"LOG_LEVEL":    "loud",
		"APP_ENV":      "staging",
	}))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	msg := err.Error()
	for _, key := range []string{"PORT", "DEBUG", "READ_TIMEOUT", "LOG_LEVEL", "APP_ENV"} {
		if !strings.Contains(msg, key) {
			t.Errorf("error should mention %s:\n%s", key, msg)
		}
	}
}

func TestFromLookup_Validation(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string // fragment expected in the error
	}{
		{"port zero", map[string]string{"PORT": "0"}, "PORT"},
		{"port not a number", map[string]string{"PORT": "http"}, "PORT"},
		{"negative timeout", map[string]string{"WRITE_TIMEOUT": "-5s"}, "WRITE_TIMEOUT"},
		{"zero timeout", map[string]string{"SHUTDOWN_TIMEOUT": "0s"}, "SHUTDOWN_TIMEOUT"},
		{"bad log format", map[string]string{"LOG_FORMAT": "xml"}, "LOG_FORMAT"},
		{"app name with spaces", map[string]string{"APP_NAME": "my app"}, "APP_NAME"},
		// Security rule: debug output can leak internals (stack traces, config),
		// so it's refused in production.
		{"debug in production", map[string]string{"APP_ENV": "production", "DEBUG": "true"}, "DEBUG"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FromLookup(fakeEnv(tc.env))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestLoad_EnvironmentBeatsFile(t *testing.T) {
	// t.TempDir gives each test its own folder, deleted automatically after.
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	contents := "APP_NAME=from-file\nPORT=5000\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	// t.Setenv sets a real environment variable for this test only and
	// restores the old value afterwards.
	t.Setenv("PORT", "6000")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppName != "from-file" {
		t.Errorf("AppName = %q, want value from file", cfg.AppName)
	}
	if cfg.Port != 6000 {
		t.Errorf("Port = %d, want 6000 (environment should win over file)", cfg.Port)
	}
}

func TestLoad_MissingFileIsFine(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.env"))
	if err != nil {
		t.Fatalf("a missing .env file should not be an error, got: %v", err)
	}
	if cfg.Port == 0 {
		t.Error("expected defaults to be applied")
	}
}

func TestLoad_BadFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("NOT VALID\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for a malformed .env file")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error should point at the bad line: %v", err)
	}
}

func TestAddr(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"", 4000, ":4000"},
		{"127.0.0.1", 8080, "127.0.0.1:8080"},
		{"::1", 9000, "[::1]:9000"}, // IPv6 addresses need brackets
	}
	for _, tc := range cases {
		cfg := Config{Host: tc.host, Port: tc.port}
		if got := cfg.Addr(); got != tc.want {
			t.Errorf("Addr() with host %q port %d = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

func TestIsProduction(t *testing.T) {
	if !(Config{Env: "production"}).IsProduction() {
		t.Error("production should be production")
	}
	if (Config{Env: "development"}).IsProduction() {
		t.Error("development is not production")
	}
}
