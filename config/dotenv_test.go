package config

// These tests are the spec for the .env parser. Each case is one line of
// input and the key/value pairs it should produce.
//
// TRY: add your own case to the table in TestParseDotEnv, for example a value
// with an equals sign in it (URL=https://x.com/?a=b), and see if it passes.

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{"simple pair", "PORT=4000", map[string]string{"PORT": "4000"}},
		{"several pairs", "A=1\nB=2", map[string]string{"A": "1", "B": "2"}},
		{"blank lines ignored", "\n\nA=1\n\n", map[string]string{"A": "1"}},
		{"comment lines ignored", "# a comment\nA=1\n  # indented comment", map[string]string{"A": "1"}},
		{"export prefix", "export A=1", map[string]string{"A": "1"}},
		{"spaces around key and value", "  A  =  hello  ", map[string]string{"A": "hello"}},
		{"empty value", "A=", map[string]string{"A": ""}},
		{"equals inside value", "URL=https://x.com/?a=b", map[string]string{"URL": "https://x.com/?a=b"}},
		{"inline comment after space", "A=1 # the port", map[string]string{"A": "1"}},
		{"hash without space is part of value", "COLOR=#ff0000", map[string]string{"COLOR": "#ff0000"}},
		{"hash inside value", "A=abc#def", map[string]string{"A": "abc#def"}},
		{"double quotes keep spaces", `A="  hi  "`, map[string]string{"A": "  hi  "}},
		{"double quotes keep hash", `A="a # b"`, map[string]string{"A": "a # b"}},
		{"double quote escapes", `A="line1\nline2\ttab \"q\" back\\slash"`, map[string]string{"A": "line1\nline2\ttab \"q\" back\\slash"}},
		{"single quotes are literal", `A='no\nescape'`, map[string]string{"A": `no\nescape`}},
		{"comment after quoted value", `A="x" # note`, map[string]string{"A": "x"}},
		{"last duplicate wins", "A=1\nA=2", map[string]string{"A": "2"}},
		{"windows line endings", "A=1\r\nB=2\r\n", map[string]string{"A": "1", "B": "2"}},
		{"underscores and digits in key", "_MY_KEY2=v", map[string]string{"_MY_KEY2": "v"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDotEnv(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// maps.Equal (Go 1.21+) compares two maps key by key.
			if !maps.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseDotEnv_Errors(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantLine int    // which line the error should point at
		wantMsg  string // a fragment of the message
	}{
		{"missing equals", "A=1\nJUSTAWORD", 2, "expected KEY=VALUE"},
		{"empty key", "=value", 1, "invalid key"},
		{"key with dash", "MY-KEY=1", 1, "invalid key"},
		{"key starting with digit", "1KEY=1", 1, "invalid key"},
		{"unterminated double quote", "A=1\n\nB=\"oops", 3, "unterminated"},
		{"unterminated single quote", "A='oops", 1, "unterminated"},
		{"junk after closing quote", `A="x"y`, 1, "after closing quote"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDotEnv(strings.NewReader(tc.input))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}

			// GO CONCEPT: errors.As checks whether err is (or wraps) a
			// *ParseError and, if so, fills in pe so we can read its fields.
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error %v (%T) is not a *ParseError", err, err)
			}
			if pe.Line != tc.wantLine {
				t.Errorf("line = %d, want %d", pe.Line, tc.wantLine)
			}
			if !strings.Contains(pe.Msg, tc.wantMsg) {
				t.Errorf("message %q does not contain %q", pe.Msg, tc.wantMsg)
			}
		})
	}
}
