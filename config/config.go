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

// The allowed values for the settings that are a fixed list of choices.
var (
	validEnvs       = []string{"development", "test", "production"}
	validLogLevels  = []string{"debug", "info", "warn", "error"}
	validLogFormats = []string{"text", "json"}
)

// Default returns the configuration used when no settings are provided.
// Start from it when building a Config in code instead of from the environment:
//
//	cfg := config.Default()
//	cfg.Port = 8080
func Default() Config {
	return Config{
		AppName: "omolar",
		Env:     "development",
		Debug:   false,

		Host: "",
		Port: 4000,

		ReadTimeout:     15 * time.Second,
		WriteTimeout:    30 * time.Second,
		IdleTimeout:     60 * time.Second,
		ShutdownTimeout: 10 * time.Second,

		LogLevel:  "info",
		LogFormat: "text",
	}
}

// Validate reports every problem with c. Code that receives a Config it didn't
// load itself (omolar.New, for one) calls this, so a hand-built Config can't
// skip the rules that FromLookup enforces.
//
// Errors name the environment variable, since that's usually what you'd fix.
func (c Config) Validate() error {
	var errs []error
	bad := func(key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
	}

	if !validAppName(c.AppName) {
		bad("APP_NAME", "use only letters, digits, '-' and '_', got %q", c.AppName)
	}
	if !slices.Contains(validEnvs, c.Env) {
		bad("APP_ENV", "must be one of %s, got %q", strings.Join(validEnvs, ", "), c.Env)
	}
	if c.Env == "production" && c.Debug {
		// Security: debug mode can expose stack traces and internal details.
		bad("DEBUG", "must be false when APP_ENV=production")
	}

	if !validHost(c.Host) {
		bad("HOST", "must be empty, an IP address or a hostname (no port), got %q", c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		bad("PORT", "must be between 1 and 65535, got %d", c.Port)
	}

	// A zero timeout in net/http means "wait forever", which lets slow or
	// malicious clients hold connections open. So zero is an error here, not
	// a quiet fallback.
	timeouts := []struct {
		key string
		d   time.Duration
	}{
		{"READ_TIMEOUT", c.ReadTimeout},
		{"WRITE_TIMEOUT", c.WriteTimeout},
		{"IDLE_TIMEOUT", c.IdleTimeout},
		{"SHUTDOWN_TIMEOUT", c.ShutdownTimeout},
	}
	for _, t := range timeouts {
		if t.d <= 0 {
			bad(t.key, "must be greater than zero, got %s", t.d)
		}
	}

	if !slices.Contains(validLogLevels, c.LogLevel) {
		bad("LOG_LEVEL", "must be one of %s, got %q", strings.Join(validLogLevels, ", "), c.LogLevel)
	}
	if !slices.Contains(validLogFormats, c.LogFormat) {
		bad("LOG_FORMAT", "must be one of %s, got %q", strings.Join(validLogFormats, ", "), c.LogFormat)
	}

	// GO CONCEPT: errors.Join (Go 1.20+) bundles several errors into one.
	// Its message is each error on its own line. Join of nothing is nil.
	return errors.Join(errs...)
}

// FromLookup builds a Config from settings provided by lookup, applies
// defaults, and validates the result. All problems are reported together.
func FromLookup(lookup LookupFunc) (Config, error) {
	l := &loader{lookup: lookup}
	d := Default()

	// Step 1: parse. Each setting falls back to its default when missing or
	// when it can't be parsed (the parse error is recorded either way).
	cfg := Config{
		AppName: l.str("APP_NAME", d.AppName),
		Env:     l.lower("APP_ENV", d.Env),
		Debug:   l.bool("DEBUG", d.Debug),

		Host: l.str("HOST", d.Host),
		Port: l.int("PORT", d.Port),

		ReadTimeout:     l.duration("READ_TIMEOUT", d.ReadTimeout),
		WriteTimeout:    l.duration("WRITE_TIMEOUT", d.WriteTimeout),
		IdleTimeout:     l.duration("IDLE_TIMEOUT", d.IdleTimeout),
		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", d.ShutdownTimeout),

		LogLevel:  l.lower("LOG_LEVEL", d.LogLevel),
		LogFormat: l.lower("LOG_FORMAT", d.LogFormat),
	}

	// Step 2: validate. The same rules as for a Config built in code, so
	// there's exactly one place that defines what "valid" means.
	errs := l.errs
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
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
	return d // whether it's > 0 is checked by Validate
}

// lower reads a choice-type value (like LOG_LEVEL) and lower-cases it, so
// "WARN" and "warn" both work. Whether it's an allowed choice is up to Validate.
func (l *loader) lower(key, def string) string {
	if v, ok := l.get(key); ok {
		return strings.ToLower(v)
	}
	return def
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

// validHost accepts "", an IP address (v4 or v6), or a hostname made of
// letters, digits, '-' and '.'. A host with a port ("localhost:80") is
// rejected, because the port belongs in PORT.
func validHost(h string) bool {
	if h == "" || net.ParseIP(h) != nil {
		return true
	}
	if len(h) > 253 {
		return false
	}
	for _, r := range h {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}
