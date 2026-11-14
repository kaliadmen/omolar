// Package config loads application settings from environment variables and an
// optional .env file into a typed Config struct.
//
// Why typed? Reading os.Getenv("PORT") all over the codebase means every caller
// re-parses strings and handles mistakes differently. Loading once, validating
// once, and handing out a struct means a typo in .env fails loudly at startup
// instead of quietly misbehaving later.
package config

import (
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
type LookupFunc func(key string) (string, bool)

// FromLookup builds a Config from settings provided by lookup, applies
// defaults, and validates the result. All problems are reported together.
func FromLookup(lookup LookupFunc) (Config, error) {
	return Config{}, errNotImplemented
}

// Load reads the .env file at path (a missing file is fine), then builds a
// Config where real environment variables take priority over the file.
// Pass "" to skip the file entirely.
func Load(path string) (Config, error) {
	return Config{}, errNotImplemented
}

// Addr returns the "host:port" the server should listen on.
func (c Config) Addr() string {
	return ""
}

// IsProduction reports whether the app is running in production.
func (c Config) IsProduction() bool {
	return false
}
