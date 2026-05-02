// Package dotenv loads KEY=VALUE pairs from .env files into the process
// environment. Existing variables are never overridden, so a value already
// exported in the shell always wins over a file entry — that's the
// behavior callers expect from a "fall back" config source.
//
// Supported syntax:
//
//	KEY=value
//	KEY="quoted value"      # double-quoted; \" \\ \n \t \r decoded
//	KEY='single quoted'     # single-quoted; literal contents
//	export KEY=value        # leading "export " is stripped
//	# comment
//
// Trailing inline comments are honored only on unquoted values
// (`KEY=value  # note`). Empty keys and malformed lines are ignored.
package dotenv

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// Load reads path and sets any unset variables. Missing files are not an
// error — callers can speculatively probe several locations.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	return loadFrom(f)
}

func loadFrom(r io.Reader) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := parseLine(sc.Text())
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(k); set {
			continue
		}
		_ = os.Setenv(k, v)
	}
	return sc.Err()
}

func parseLine(line string) (key, val string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	s = strings.TrimPrefix(s, "export ")
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:eq])
	rest := strings.TrimLeft(s[eq+1:], " \t")
	val = parseValue(rest)
	if key == "" {
		return "", "", false
	}
	return key, val, true
}

func parseValue(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '"':
		return readQuoted(s, '"', true)
	case '\'':
		return readQuoted(s, '\'', false)
	}
	// Unquoted: strip trailing whitespace and an inline `# comment`.
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// readQuoted consumes everything up to the matching close quote. When
// expandEscapes is true (double-quoted), common escape sequences are
// decoded; otherwise the contents are taken literally.
func readQuoted(s string, quote byte, expandEscapes bool) string {
	if len(s) < 2 {
		return ""
	}
	body := s[1:]
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == quote {
			return b.String()
		}
		if expandEscapes && c == '\\' && i+1 < len(body) {
			i++
			switch body[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				b.WriteByte('\\')
				b.WriteByte(body[i])
			}
			continue
		}
		b.WriteByte(c)
	}
	// Unterminated quote — return what we got rather than fail loud, since
	// dotenv loaders historically tolerate sloppy files.
	return b.String()
}
