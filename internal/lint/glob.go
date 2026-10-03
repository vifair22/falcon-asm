package lint

import (
	"fmt"
	"path"
	"strings"
)

// Glob is a compiled path pattern from the overrides of .falcon-asm.json.
//
// Patterns are slash-separated and relative to the directory holding the
// configuration file. Each segment is a path.Match pattern (*, ?, [...]),
// and a segment that is exactly "**" matches any number of directories,
// including none. A pattern with no slash matches the file's name at any
// depth ("*.S" is "**/*.S"), as in .gitignore, because "every .S file" is
// what such a pattern almost always means. A pattern that matches a
// directory also matches everything below it, so "legacy" and "legacy/"
// both cover legacy/boot/mbr.S.
type Glob struct {
	src  string
	segs []string
}

// CompileGlob checks and compiles a pattern.
func CompileGlob(pattern string) (Glob, error) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return Glob{}, fmt.Errorf("empty pattern")
	}
	p = strings.TrimPrefix(p, "./")
	anchored := strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.Trim(p, "/")
	if p == "" {
		// "/" alone: the whole tree.
		p = "**"
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if s == "" {
			return Glob{}, fmt.Errorf("pattern %q has an empty path segment", pattern)
		}
		if _, err := path.Match(s, ""); err != nil {
			return Glob{}, fmt.Errorf("pattern %q: %w", pattern, err)
		}
	}
	if !anchored {
		segs = append([]string{"**"}, segs...)
	}
	// Everything under a matched directory matches too.
	segs = append(segs, "**")
	return Glob{src: pattern, segs: segs}, nil
}

// String returns the pattern as written.
func (g Glob) String() string { return g.src }

// Match reports whether the slash-separated relative path matches.
func (g Glob) Match(rel string) bool {
	if len(g.segs) == 0 {
		return false
	}
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	return matchSegs(g.segs, strings.Split(rel, "/"))
}

// matchSegs matches pattern segments against path segments. "**" tries
// every split point; patterns are short, so the backtracking is cheap.
func matchSegs(p, s []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for len(p) > 1 && p[1] == "**" {
				p = p[1:]
			}
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if matchSegs(p[1:], s[i:]) {
					return true
				}
			}
			return false
		}
		if len(s) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], s[0]); !ok {
			return false
		}
		p, s = p[1:], s[1:]
	}
	return len(s) == 0
}
