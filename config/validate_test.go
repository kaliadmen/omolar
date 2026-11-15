package config

// Review finding: "applications cannot bypass important validation rules by
// constructing config.Config directly." These tests pin down the rules so they
// apply to EVERY Config, however it was made, not just ones loaded from .env.

import (
	"strings"
	"testing"
	"time"
)

func TestDefault_IsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default() should be valid, got:\n%v", err)
	}
}

func TestDefault_MatchesLoadingNothing(t *testing.T) {
	loaded, err := FromLookup(fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if Default() != loaded {
		t.Errorf("Default() =\n  %+v\nbut loading no settings gives\n  %+v", Default(), loaded)
	}
}

func TestValidate_ZeroConfigIsInvalid(t *testing.T) {
	// config.Config{} is what you get by forgetting to set anything. It must
	// not be usable: zero timeouts mean "wait forever" and port 0 means
	// "pick a random port".
	err := Config{}.Validate()
	if err == nil {
		t.Fatal("Config{} should be invalid")
	}
	for _, field := range []string{"APP_NAME", "APP_ENV", "PORT", "READ_TIMEOUT",
		"WRITE_TIMEOUT", "IDLE_TIMEOUT", "SHUTDOWN_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error should mention %s:\n%v", field, err)
		}
	}
}

func TestValidate_Rules(t *testing.T) {
	cases := []struct {
		name   string
		change func(c *Config) // GO CONCEPT: a func field lets each case say what to break
		want   string          // fragment expected in the error
	}{
		{"empty app name", func(c *Config) { c.AppName = "" }, "APP_NAME"},
		{"app name with spaces", func(c *Config) { c.AppName = "my app" }, "APP_NAME"},
		{"unknown env", func(c *Config) { c.Env = "staging" }, "APP_ENV"},
		{"env must be lower case", func(c *Config) { c.Env = "Production" }, "APP_ENV"},
		{"port zero", func(c *Config) { c.Port = 0 }, "PORT"},
		{"port negative", func(c *Config) { c.Port = -1 }, "PORT"},
		{"port too big", func(c *Config) { c.Port = 70000 }, "PORT"},
		{"host with a port in it", func(c *Config) { c.Host = "localhost:80" }, "HOST"},
		{"host with spaces", func(c *Config) { c.Host = "bad host" }, "HOST"},
		{"zero read timeout", func(c *Config) { c.ReadTimeout = 0 }, "READ_TIMEOUT"},
		{"negative read timeout", func(c *Config) { c.ReadTimeout = -time.Second }, "READ_TIMEOUT"},
		{"zero write timeout", func(c *Config) { c.WriteTimeout = 0 }, "WRITE_TIMEOUT"},
		{"negative idle timeout", func(c *Config) { c.IdleTimeout = -time.Second }, "IDLE_TIMEOUT"},
		{"zero shutdown timeout", func(c *Config) { c.ShutdownTimeout = 0 }, "SHUTDOWN_TIMEOUT"},
		{"unknown log level", func(c *Config) { c.LogLevel = "loud" }, "LOG_LEVEL"},
		{"unknown log format", func(c *Config) { c.LogFormat = "xml" }, "LOG_FORMAT"},
		{"debug in production", func(c *Config) { c.Env = "production"; c.Debug = true }, "DEBUG"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.change(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected an error, config was %+v", c)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestValidate_AcceptsGoodHosts(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1", "0.0.0.0", "::1", "::", "localhost", "api.example.com"} {
		c := Default()
		c.Host = host
		if err := c.Validate(); err != nil {
			t.Errorf("host %q should be valid, got: %v", host, err)
		}
	}
}

func TestValidate_ReportsEveryProblem(t *testing.T) {
	c := Default()
	c.Port = 0
	c.LogLevel = "loud"
	c.WriteTimeout = -time.Second

	err := c.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, field := range []string{"PORT", "LOG_LEVEL", "WRITE_TIMEOUT"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error should mention %s:\n%v", field, err)
		}
	}
}
