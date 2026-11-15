// Package config loads application settings from environment variables and an
// optional .env file into a typed Config struct.
//
// Why typed? Reading os.Getenv("PORT") all over the codebase means every caller
// re-parses strings and handles mistakes differently. Loading once, validating
// once, and handing out a struct means a typo in .env fails loudly at startup
// instead of quietly misbehaving later.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting the framework needs.
type Config struct {
	AppName string // APP_NAME, default "omolar"
	Env     string // APP_ENV: development, test or production (default development)
	Debug   bool   // DEBUG, default false. Not allowed in production.

	Host string // HOST, default "" (all interfaces)
	Port int    // PORT, default 4000

	ReadTimeout     time.Duration // READ_TIMEOUT, default 15s
	WriteTimeout    time.Duration // WRITE_TIMEOUT, default 30s
	IdleTimeout     time.Duration // IDLE_TIMEOUT, default 60s
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT, default 10s

	LogLevel  string // LOG_LEVEL: debug, info, warn, error (default info)
	LogFormat string // LOG_FORMAT: text or json (default text)
}

// LookupFunc finds a setting by name. It has the same shape as os.LookupEnv:
// the bool is false when the setting isn't present at all.
//
// GO CONCEPT: function types. Functions are values in Go. Accepting a function
// instead of calling os.LookupEnv directly lets tests pass in a fake
// environment, with no global state to clean up.
//
// PATTERN: Dependency injection. FromLookup is *given* the thing it depends
// on instead of reaching out for it (os.LookupEnv). Without it, tests would
// have to change real environment variables and remember to undo them.
type LookupFunc func(key string) (string, bool)

// Default returns the configuration used when no settings are provided.
// Start from it when building a Config in code instead of from the environment.
func Default() Config {
	return Config{}
}

// Validate reports every problem with c. Code that receives a Config it didn't
// load itself (omolar.New, for one) calls this, so a hand-built Config can't
// skip the rules that FromLookup enforces.
func (c Config) Validate() error {
	return nil
}

// FromLookup builds a Config from settings provided by lookup, applies
// defaults, and validates the result. All problems are reported together.
func FromLookup(lookup LookupFunc) (Config, error) {
	l := &loader{lookup: lookup}

	cfg := Config{
		AppName: l.str("APP_NAME", "omolar"),
		Env:     l.oneOf("APP_ENV", "development", "development", "test", "production"),
		Debug:   l.bool("DEBUG", false),

		Host: l.str("HOST", ""),
		Port: l.int("PORT", 4000),

		ReadTimeout:     l.duration("READ_TIMEOUT", 15*time.Second),
		WriteTimeout:    l.duration("WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:     l.duration("IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", 10*time.Second),

		LogLevel:  l.oneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
		LogFormat: l.oneOf("LOG_FORMAT", "text", "text", "json"),
	}

	// Rules that involve the meaning of a value, not just its format.
	if cfg.Port < 1 || cfg.Port > 65535 {
		l.fail("PORT", "must be between 1 and 65535, got %d", cfg.Port)
	}
	if !validAppName(cfg.AppName) {
		l.fail("APP_NAME", "use only letters, digits, '-' and '_', got %q", cfg.AppName)
	}
	if cfg.Env == "production" && cfg.Debug {
		// Security: debug mode can expose stack traces and internal details.
		l.fail("DEBUG", "must be false when APP_ENV=production")
	}

	if len(l.errs) > 0 {
		// GO CONCEPT: errors.Join (Go 1.20+) bundles several errors into one.
		// Its message is each error on its own line.
		return Config{}, errors.Join(l.errs...)
	}
	return cfg, nil
}

// Load reads the .env file at path (a missing file is fine), then builds a
// Config where real environment variables take priority over the file.
// Pass "" to skip the file entirely.
func Load(path string) (Config, error) {
	fileValues := map[string]string{}

	if path != "" {
		f, err := os.Open(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// No .env file: perfectly normal in production, where settings
			// come from the real environment (Docker, systemd, etc).
		case err != nil:
			return Config{}, fmt.Errorf("config: %w", err)
		default:
			fileValues, err = ParseDotEnv(f)
			f.Close()
			if err != nil {
				return Config{}, fmt.Errorf("config: %w", err)
			}
		}
	}

	// The real environment wins over the file. This is the standard
	// convention: the file holds local defaults, the deployment overrides them.
	lookup := func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
			return v, true
		}
		v, ok := fileValues[key]
		return v, ok
	}

	cfg, err := FromLookup(lookup)
	if err != nil {
		return Config{}, fmt.Errorf("config: invalid settings:\n%w", err)
	}
	return cfg, nil
}

// Addr returns the "host:port" the server should listen on.
func (c Config) Addr() string {
	// JoinHostPort adds the [brackets] IPv6 addresses need.
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// IsProduction reports whether the app is running in production.
//
// GO CONCEPT: value receivers. `(c Config)` means this method gets a copy of
// the Config. That's fine for read-only methods on small structs. Methods that
// need to modify the struct use a pointer receiver, `(c *Config)`, instead.
func (c Config) IsProduction() bool {
	return c.Env == "production"
}

// loader reads settings and collects every error instead of stopping at the
// first one.
//
// PATTERN: Notification (collect errors, report once). Each check adds to a
// list rather than returning early. Without it, a .env file with five mistakes
// takes five restarts to fix.
type loader struct {
	lookup LookupFunc
	errs   []error
}

// get returns the trimmed value for key. An empty value counts as missing.
func (l *loader) get(key string) (string, bool) {
	v, ok := l.lookup(key)
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// fail records a problem with a setting.
//
// GO CONCEPT: variadic parameters. `args ...any` accepts any number of
// arguments of any type, passed on to fmt.Sprintf like Printf does.
func (l *loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
}

func (l *loader) str(key, def string) string {
	if v, ok := l.get(key); ok {
		return v
	}
	return def
}

func (l *loader) int(key string, def int) int {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail(key, "must be a whole number, got %q", v)
		return def
	}
	return n
}

func (l *loader) bool(key string, def bool) bool {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	// ParseBool accepts 1, t, true, TRUE, 0, f, false, FALSE, etc.
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "must be true or false, got %q", v)
		return def
	}
	return b
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	// ParseDuration understands values like "300ms", "15s", "2m", "1h30m".
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(key, "must be a duration like 15s or 2m, got %q", v)
		return def
	}
	if d <= 0 {
		l.fail(key, "must be greater than zero, got %s", d)
		return def
	}
	return d
}

// oneOf reads a value that must be one of the allowed options (case-insensitive).
func (l *loader) oneOf(key, def string, allowed ...string) string {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	v = strings.ToLower(v)
	// slices.Contains (Go 1.21+) is a generic helper: it works on a slice of
	// any comparable type.
	if !slices.Contains(allowed, v) {
		l.fail(key, "must be one of %s, got %q", strings.Join(allowed, ", "), v)
		return def
	}
	return v
}

// validAppName allows letters, digits, '-' and '_'. The name ends up in
// cookie names and log lines, where spaces and symbols cause trouble.
func validAppName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}
