package toolchain

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Origin is where one line of preprocessed output came from.
type Origin struct {
	File string // absolute; "" for marker lines and cpp pseudo files such as <built-in>
	Line int    // 0-based line in File
	// Via is the 0-based line of the main file whose #include (directly or
	// through other headers) pulled this line in; -1 for main-file lines and
	// for lines that did not come through the main file.
	Via int
}

// LineMap maps preprocessed output lines (0-based) back to their sources.
type LineMap struct {
	Main  string   // the main file, as named in the markers (absolute)
	Lines []Origin // one per output line
	// Includes holds the 0-based main-file lines of #include directives the
	// preprocessor acted on; the include itself produces no output line,
	// only a marker, so this is how inactive-region detection sees it.
	Includes map[int]bool
}

type incFrame struct {
	file string
	line int
}

// ParseLineMap reads cpp output with GNU line markers (`# N "file" flags`,
// flag 1 entering an include, 2 returning from one) and records each
// output line's origin. Relative names in markers are resolved against dir,
// the directory cpp ran in, since that is what cpp resolved them against.
func ParseLineMap(out, main, dir string) *LineMap {
	lm := &LineMap{Main: main, Includes: map[int]bool{}}
	lines := splitLines(out)
	lm.Lines = make([]Origin, len(lines))
	cur := ""
	next := 0 // 0-based line number the next text line has in cur
	var stack []incFrame
	for i, l := range lines {
		if num, name, flags, ok := parseMarker(l); ok {
			file := resolveMarkerName(name, dir)
			switch {
			case hasFlag(flags, "1"):
				stack = append(stack, incFrame{file: cur, line: next})
			case hasFlag(flags, "2"):
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				// The directive line sits just before the line cpp resumes at.
				if file == main && num >= 2 {
					lm.Includes[num-2] = true
				}
			}
			cur = file
			next = num - 1
			lm.Lines[i] = Origin{Via: -1}
			continue
		}
		o := Origin{File: cur, Line: next, Via: -1}
		if cur != main {
			for _, fr := range stack {
				if fr.file == main {
					o.Via = fr.line
					break
				}
			}
		}
		lm.Lines[i] = o
		next++
	}
	return lm
}

// Present reports the main-file lines that produced non-blank output,
// which is the evidence that a line survived the preprocessor: cpp also
// emits blank lines for what it removes, so a blank line proves nothing.
func (lm *LineMap) Present(out string) map[int]bool {
	present := map[int]bool{}
	for i, l := range splitLines(out) {
		if i >= len(lm.Lines) {
			break
		}
		o := lm.Lines[i]
		if o.File == lm.Main && o.File != "" && strings.TrimSpace(l) != "" {
			present[o.Line] = true
		}
	}
	for l := range lm.Includes {
		present[l] = true
	}
	return present
}

// ViaFor finds the main-file include line through which (file, line) was
// reached, for mirroring an include's error onto the main file.
func (lm *LineMap) ViaFor(file string, line int) (int, bool) {
	for _, o := range lm.Lines {
		if o.File == file && o.Line == line && o.Via >= 0 {
			return o.Via, true
		}
	}
	return -1, false
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// parseMarker recognises `# N "name" [flags]` and the older `#line N "name"`.
func parseMarker(l string) (int, string, []string, bool) {
	l = strings.TrimRight(l, "\r")
	rest, ok := strings.CutPrefix(l, "# ")
	if !ok {
		if rest, ok = strings.CutPrefix(l, "#line "); !ok {
			return 0, "", nil, false
		}
	}
	sp := strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return 0, "", nil, false
	}
	n, err := strconv.Atoi(rest[:sp])
	if err != nil {
		return 0, "", nil, false
	}
	rest = rest[sp+1:]
	if !strings.HasPrefix(rest, "\"") {
		return 0, "", nil, false
	}
	name, tail, ok := cutCString(rest[1:])
	if !ok {
		return 0, "", nil, false
	}
	return n, name, strings.Fields(tail), true
}

// cutCString unescapes a C string body up to its closing quote. cpp escapes
// backslashes and quotes in file names, which matters on paths with either.
func cutCString(s string) (string, string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], true
		case '\\':
			if i+1 < len(s) {
				i++
				c = s[i]
			}
		}
		b.WriteByte(c)
	}
	return "", "", false
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

// resolveMarkerName returns "" for cpp pseudo files (<built-in>,
// <command-line>) so their lines never match a real path.
func resolveMarkerName(name, dir string) string {
	if strings.HasPrefix(name, "<") {
		return ""
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(dir, name)
	}
	return filepath.Clean(name)
}
