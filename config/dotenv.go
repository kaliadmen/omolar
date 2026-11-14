package config

import (
	"errors"
	"fmt"
	"io"
)

// errNotImplemented is returned by stubs during the "red" step of TDD.
var errNotImplemented = errors.New("not implemented")

// ParseError describes a problem on a specific line of a .env file.
//
// GO CONCEPT: custom error types. Any type with an `Error() string` method
// satisfies Go's built-in `error` interface. Using a struct lets callers pull
// out details (like the line number) with errors.As, instead of parsing text.
type ParseError struct {
	Line int    // 1-based line number
	Msg  string // what went wrong
}

func (e *ParseError) Error() string {
	return fmt.Sprintf(".env line %d: %s", e.Line, e.Msg)
}

// ParseDotEnv reads KEY=VALUE pairs in .env format.
//
// Rules:
//   - blank lines and lines starting with # are ignored
//   - an optional leading "export " is allowed (so the file also works in a shell)
//   - keys are letters, digits and underscores, not starting with a digit
//   - unquoted values are trimmed; " #" starts a comment
//   - "double quotes" support \n \t \" and \\ escapes
//   - 'single quotes' are taken literally
//   - if a key appears twice, the last value wins
func ParseDotEnv(r io.Reader) (map[string]string, error) {
	return nil, errNotImplemented
}
