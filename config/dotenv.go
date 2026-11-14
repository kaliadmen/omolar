package config

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

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
	values := make(map[string]string)

	// bufio.ScanLines (the Scanner default) also strips a trailing \r, so
	// files saved on Windows (\r\n line endings) work without extra code.
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		key, value, ok, err := parseLine(sc.Text(), lineNo)
		if err != nil {
			return nil, err
		}
		if ok {
			values[key] = value
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading .env: %w", err)
	}
	return values, nil
}

// parseLine handles one line. ok is false for blank and comment lines.
//
// GO CONCEPT: multiple return values. Go functions can return several values;
// returning (result, error) is the standard way to report failure.
func parseLine(line string, lineNo int) (key, value string, ok bool, err error) {
	// GO CONCEPT: named results. key, value, ok and err are declared in the
	// signature and start as zero values (empty string, false, nil). A bare
	// `return` would return their current values; we still return explicitly
	// below because it's easier to read.
	fail := func(msg string) (string, string, bool, error) {
		return "", "", false, &ParseError{Line: lineNo, Msg: msg}
	}

	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false, nil
	}

	// "export KEY=value" is valid shell syntax; accept it and ignore "export".
	if rest, found := strings.CutPrefix(s, "export"); found && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")) {
		s = strings.TrimSpace(rest)
	}

	// strings.Cut splits on the FIRST "=", so values may contain "=" too.
	rawKey, rawValue, found := strings.Cut(s, "=")
	if !found {
		return fail("expected KEY=VALUE")
	}

	key = strings.TrimSpace(rawKey)
	if !validKey(key) {
		return fail(fmt.Sprintf("invalid key %q (use letters, digits and _; don't start with a digit)", key))
	}

	rawValue = strings.TrimLeft(rawValue, " \t")

	// Byte-by-byte scanning note: Go strings are UTF-8. Characters like ", '
	// and # are single bytes, and UTF-8 guarantees those byte values never
	// appear inside a multi-byte character. So checking individual bytes for
	// quotes is safe even if the value contains emoji or accented letters.
	switch {
	case strings.HasPrefix(rawValue, `"`):
		v, rest, ok := parseDoubleQuoted(rawValue[1:])
		if !ok {
			return fail("unterminated double-quoted value")
		}
		if !onlyCommentLeft(rest) {
			return fail("unexpected text after closing quote")
		}
		return key, v, true, nil

	case strings.HasPrefix(rawValue, `'`):
		end := strings.IndexByte(rawValue[1:], '\'')
		if end == -1 {
			return fail("unterminated single-quoted value")
		}
		v := rawValue[1 : end+1]
		if !onlyCommentLeft(rawValue[end+2:]) {
			return fail("unexpected text after closing quote")
		}
		return key, v, true, nil

	default:
		return key, stripInlineComment(rawValue), true, nil
	}
}

// parseDoubleQuoted reads up to the closing quote, handling escapes.
// It returns the value, whatever follows the closing quote, and false if no
// closing quote was found.
func parseDoubleQuoted(s string) (value, rest string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), s[i+1:], true
		case c == '\\' && i+1 < len(s):
			i++ // consume the character after the backslash
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\':
				b.WriteByte(s[i])
			default:
				// Unknown escape like \q: keep it exactly as written.
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", false
}

// stripInlineComment removes a trailing " # comment" from an unquoted value.
// A # only starts a comment when whitespace comes before it, so values like
// COLOR=#ff0000 and URL=https://x.com/#top keep their #.
func stripInlineComment(s string) string {
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			s = s[:i]
			break
		}
	}
	return strings.TrimSpace(s)
}

// onlyCommentLeft reports whether s is empty, whitespace, or a # comment.
func onlyCommentLeft(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "#")
}

// validKey reports whether k looks like an environment variable name.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !(isDigit && i > 0) {
			return false
		}
	}
	return true
}
