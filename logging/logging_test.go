package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNew_JSONFormat(t *testing.T) {
	// bytes.Buffer is an in-memory io.Writer: perfect for capturing output.
	var buf bytes.Buffer
	log, err := New(&buf, Options{Level: "info", Format: "json", AppName: "testapp"})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	log.Info("server started", "addr", ":4000")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, buf.String())
	}
	want := map[string]string{
		"msg":   "server started",
		"level": "INFO",
		"app":   "testapp",
		"addr":  ":4000",
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %q", k, line[k], v)
		}
	}
	if _, ok := line["time"]; !ok {
		t.Error("every line should have a time")
	}
}

func TestNew_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, Options{Level: "info", Format: "text", AppName: "testapp"})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	log.Info("hello", "user", "quinze")

	out := buf.String()
	for _, want := range []string{"level=INFO", `msg=hello`, "app=testapp", "user=quinze"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output %q is missing %q", out, want)
		}
	}
}

func TestNew_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, Options{Level: "warn", Format: "text"})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	log.Debug("too quiet")
	log.Info("still too quiet")
	log.Warn("loud enough")
	log.Error("definitely")

	out := buf.String()
	if strings.Contains(out, "too quiet") {
		t.Errorf("messages below the level should be dropped:\n%s", out)
	}
	if !strings.Contains(out, "loud enough") || !strings.Contains(out, "definitely") {
		t.Errorf("messages at or above the level should be kept:\n%s", out)
	}
}

func TestNew_NoAppNameNoAttribute(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, Options{Level: "info", Format: "text"})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	log.Info("x")
	if strings.Contains(buf.String(), "app=") {
		t.Errorf("no app attribute expected when AppName is empty: %s", buf.String())
	}
}

func TestNew_RejectsUnknownFormat(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, Options{Level: "info", Format: "xml"}); err == nil {
		t.Error("expected an error for format xml")
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"ERROR", slog.LevelError, false},
		{"", slog.LevelInfo, false}, // empty means the default
		{"loud", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseLevel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseLevel(%q) expected an error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseLevel(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
