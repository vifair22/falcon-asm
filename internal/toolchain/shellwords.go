package toolchain

import (
	"errors"
	"strings"
)

// errUnterminated is returned for a command string whose quoting never
// closes. Such an entry is skipped rather than guessed at, because a wrong
// split would hand the compiler a mangled include path.
var errUnterminated = errors.New("unterminated quote or trailing backslash")

// SplitCommand splits a compile_commands.json "command" string into argv
// the way a POSIX shell would, without expanding anything: single quotes
// are literal, double quotes honour backslash before \ " $ ` and newline,
// and an unquoted backslash escapes the next character. CMake and Bear both
// quote with these rules, so this is enough to recover their arguments.
func SplitCommand(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inWord  bool
		inSingl bool
		inDoubl bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingl:
			if c == '\'' {
				inSingl = false
			} else {
				cur.WriteByte(c)
			}
		case inDoubl:
			switch c {
			case '"':
				inDoubl = false
			case '\\':
				if i+1 >= len(s) {
					return nil, errUnterminated
				}
				n := s[i+1]
				switch n {
				case '\\', '"', '$', '`':
					cur.WriteByte(n)
					i++
				case '\n':
					i++
				default:
					cur.WriteByte(c)
				}
			default:
				cur.WriteByte(c)
			}
		case c == '\'':
			inSingl, inWord = true, true
		case c == '"':
			inDoubl, inWord = true, true
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errUnterminated
			}
			i++
			if s[i] != '\n' {
				cur.WriteByte(s[i])
				inWord = true
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inSingl || inDoubl {
		return nil, errUnterminated
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}
