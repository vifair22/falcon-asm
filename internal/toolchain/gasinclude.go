package toolchain

import (
	"os"
	"path/filepath"
	"strings"
)

// GasIncludeDirs is the assembler's own search list for .include: the
// -I directories passed through -Wa (gas does not see the compiler's -I),
// absolute.
func (f Flags) GasIncludeDirs() []string {
	var out []string
	add := func(d string) {
		if d == "" {
			return
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(f.Dir, d)
		}
		out = append(out, filepath.Clean(d))
	}
	for _, a := range f.Args {
		rest, ok := strings.CutPrefix(a, "-Wa,")
		if !ok {
			continue
		}
		parts := strings.Split(rest, ",")
		for i := 0; i < len(parts); i++ {
			v, ok := strings.CutPrefix(parts[i], "-I")
			if !ok {
				continue
			}
			if v == "" && i+1 < len(parts) {
				i++
				v = parts[i]
			}
			add(strings.TrimSpace(v))
		}
	}
	return out
}

// ResolveGasInclude finds the file a gas `.include "name"` in fromFile
// names. gas itself tries the name as given, relative to its working
// directory (Flags.Dir), then each -Wa,-I directory. When neither has it,
// the including file's directory and the compiler's include path are
// tried as well: an editor is asked about files a build may assemble from
// another directory, and finding the obvious file beats finding nothing.
func (f Flags) ResolveGasInclude(fromFile, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if filepath.IsAbs(name) {
		return name, isFile(name)
	}
	var dirs []string
	if f.Dir != "" {
		dirs = append(dirs, f.Dir)
	}
	dirs = append(dirs, f.GasIncludeDirs()...)
	if fromFile != "" {
		dirs = append(dirs, filepath.Dir(fromFile))
	}
	dirs = append(dirs, f.includeDirs...)
	for _, d := range dirs {
		if p := filepath.Join(d, name); isFile(p) {
			return filepath.Clean(p), true
		}
	}
	return "", false
}

// gasIncludeName returns the file name of a `.include "name"` statement on
// a listing source row.
func gasIncludeName(src string) (string, bool) {
	for _, st := range statements(src) {
		if len(st) < len(".include") || !strings.EqualFold(st[:len(".include")], ".include") {
			continue
		}
		arg := strings.TrimLeft(st[len(".include"):], " \t")
		if strings.HasPrefix(arg, "\"") {
			if name, _, ok := cutCString(arg[1:]); ok && name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// incLevel is one open .include while walking a listing: the row number
// at which the including file resumes, and that file's lines.
type incLevel struct {
	resume int
	lines  []string // nil when the file could not be read
}

// markIncluded flags the listing rows that come from files gas read with
// .include. gas lists an included file inline, numbered by its own lines
// and with no marker where it ends, so the only way to tell is to know
// the include happened and recognize the row where the including file
// resumes: the row after the .include, whose text is that file's next
// line. mainLines are the lines of the file gas assembled (the cpp output
// for a .S); resolve finds an included file.
//
// It returns, for each file pulled in this way (nested ones included),
// the main-file line of the outermost .include that led to it, so an
// assembler error inside it can be mirrored there.
func markIncluded(rows []rawListing, mainLines []string, lm *LineMap, resolve func(name string) (string, bool)) map[string]int {
	var via map[string]int
	var stack []incLevel
	cur := mainLines
	viaLine := -1
	for i := range rows {
		r := &rows[i]
		if n := len(stack); n > 0 && !r.expansion && r.num == stack[n-1].resume && lineMatches(stack[n-1].lines, r.num, r.source) {
			cur = stack[n-1].lines
			stack = stack[:n-1]
		}
		r.included = len(stack) > 0
		if r.expansion {
			continue
		}
		name, ok := gasIncludeName(r.source)
		if !ok {
			continue
		}
		if len(stack) == 0 {
			viaLine = -1
			if lm != nil && r.num-1 >= 0 && r.num-1 < len(lm.Lines) && lm.Lines[r.num-1].File == lm.Main {
				viaLine = lm.Lines[r.num-1].Line
			}
		}
		stack = append(stack, incLevel{resume: r.num + 1, lines: cur})
		cur = nil
		if p, ok := resolve(name); ok {
			if data, err := os.ReadFile(p); err == nil {
				cur = splitLines(string(data))
			}
			if viaLine >= 0 {
				if via == nil {
					via = map[string]int{}
				}
				if _, seen := via[p]; !seen {
					via[p] = viaLine
				}
			}
		}
	}
	return via
}

// lineMatches reports whether a listing row's source is line num (1-based)
// of lines. gas truncates long sources in the listing, so a prefix
// matches; unknown lines match anything.
func lineMatches(lines []string, num int, src string) bool {
	if lines == nil {
		return true
	}
	if num-1 < 0 || num-1 >= len(lines) {
		return false
	}
	l := strings.TrimRight(lines[num-1], " \t\r")
	s := strings.TrimRight(src, " \t\r")
	return l == s || s != "" && strings.HasPrefix(l, s)
}

// mirrorGasIncludes gives errors in .include'd files the main-file line of
// the .include, which the cpp line map cannot know about, so they are
// mirrored there like errors in #include'd headers.
func mirrorGasIncludes(raw []rawDiag, via map[string]int, main string) {
	for i := range raw {
		d := &raw[i]
		if d.file == main || d.via >= 0 {
			continue
		}
		if v, ok := via[d.file]; ok {
			d.via = v
		}
	}
}
