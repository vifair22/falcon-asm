package toolchain

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Source is the Diagnostic.Source for everything the toolchain reports, so
// a user can tell assembler errors from falcon-asm's own analysis.
const Source = "gas"

// rawDiag is a compiler message before it is turned into an LSP range: the
// line text is needed for that, and is read once per file afterwards.
type rawDiag struct {
	file string
	line int // 0-based
	col  int // 0-based byte column, -1 when the tool gave none
	sev  protocol.DiagnosticSeverity
	msg  string
	// via is the main-file line of the #include that led to file, -1 if
	// none; the message is mirrored there so an error in an unopened header
	// still shows up in the file being edited.
	via int
}

// parseGasStderr reads `file:line: Error: msg` style messages. Lines that
// name tmpS (the preprocessed file) are mapped through lm; gas normally
// honours the line markers itself and names the original files already.
func parseGasStderr(stderr, tmpS, dir string, lm *LineMap) []rawDiag {
	var out []rawDiag
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimRight(l, "\r")
		file, line, rest, ok := splitFileLine(l)
		if !ok {
			continue
		}
		sev, msg, ok := gasSeverity(rest)
		if !ok {
			continue
		}
		d := rawDiag{line: line - 1, col: -1, sev: sev, msg: msg, via: -1}
		if absIn(file, dir) == tmpS {
			if lm == nil || line-1 >= len(lm.Lines) || lm.Lines[line-1].File == "" {
				continue
			}
			o := lm.Lines[line-1]
			d.file, d.line, d.via = o.File, o.Line, o.Via
		} else {
			d.file = absIn(file, dir)
			if lm != nil && d.file != lm.Main {
				if v, ok := lm.ViaFor(d.file, d.line); ok {
					d.via = v
				}
			}
		}
		out = append(out, d)
	}
	return out
}

// gasSeverity: gas prefixes "Error:", "Warning:" and, for the macro call
// chain under an error, "Info:".
func gasSeverity(rest string) (protocol.DiagnosticSeverity, string, bool) {
	rest = strings.TrimSpace(rest)
	for _, p := range []struct {
		prefix string
		sev    protocol.DiagnosticSeverity
	}{
		{"Error:", protocol.SeverityError},
		{"Fatal error:", protocol.SeverityError},
		{"Warning:", protocol.SeverityWarning},
		{"Info:", protocol.SeverityInformation},
	} {
		if m, ok := strings.CutPrefix(rest, p.prefix); ok {
			return p.sev, strings.TrimSpace(m), true
		}
	}
	return 0, "", false
}

// splitFileLine splits `file:line:rest`. File names may contain colons, so
// the split is at the first `:<digits>:`.
func splitFileLine(l string) (string, int, string, bool) {
	for i := 0; i < len(l); i++ {
		if l[i] != ':' || i == 0 {
			continue
		}
		j := i + 1
		for j < len(l) && l[j] >= '0' && l[j] <= '9' {
			j++
		}
		if j == i+1 || j >= len(l) || l[j] != ':' {
			continue
		}
		n, err := strconv.Atoi(l[i+1 : j])
		if err != nil || n < 1 {
			continue
		}
		return l[:i], n, l[j+1:], true
	}
	return "", 0, "", false
}

// parseCppStderr reads gcc/clang style `file:line:col: error: msg`
// messages plus the `In file included from a:1,` chain that precedes an
// error in a header; the outermost main-file link of that chain is where
// the message is mirrored. Messages without a position (`<command-line>:
// fatal error: ...`, `cc1: error: ...`) are pinned to line 0 of main.
func parseCppStderr(stderr, main, dir string) []rawDiag {
	var out []rawDiag
	via := -1
	inChain := false
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimRight(l, "\r")
		trim := strings.TrimSpace(l)
		if inc, ok := strings.CutPrefix(trim, "In file included from "); ok {
			via, inChain = viaFromIncludeLine(inc, main, dir, -1), true
			continue
		}
		if inc, ok := strings.CutPrefix(trim, "from "); ok && inChain {
			via = viaFromIncludeLine(inc, main, dir, via)
			continue
		}
		inChain = false
		file, line, rest, ok := splitFileLine(l)
		col := -1
		if ok {
			// Optional column: `line:col:`.
			if k := strings.IndexByte(rest, ':'); k > 0 {
				if c, err := strconv.Atoi(rest[:k]); err == nil {
					col = c - 1
					rest = rest[k+1:]
				}
			}
		} else {
			// `<command-line>: error: ...` or `cc1: fatal error: ...`.
			name, r, found := strings.Cut(l, ": ")
			if !found || strings.ContainsAny(name, " \t") {
				continue
			}
			file, line, rest = "", 1, r
		}
		sev, msg, ok := cppSeverity(rest)
		if !ok {
			continue
		}
		d := rawDiag{line: line - 1, col: col, sev: sev, msg: msg, via: -1}
		if file == "" || strings.HasPrefix(file, "<") {
			d.file, d.line, d.col = main, 0, -1
		} else {
			d.file = absIn(file, dir)
			if d.file != main && via >= 0 {
				d.via = via
			}
		}
		out = append(out, d)
		via = -1
	}
	return out
}

// viaFromIncludeLine reads one `file:line[:col][,:]` link of an include
// chain; gcc lists innermost first, so a later main-file link replaces an
// earlier one.
func viaFromIncludeLine(s, main, dir string, prev int) int {
	s = strings.TrimRight(s, ",:")
	file, line, _, ok := splitFileLine(s + ":")
	if !ok {
		return prev
	}
	if absIn(file, dir) == main {
		return line - 1
	}
	return prev
}

func cppSeverity(rest string) (protocol.DiagnosticSeverity, string, bool) {
	rest = strings.TrimSpace(rest)
	for _, p := range []struct {
		prefix string
		sev    protocol.DiagnosticSeverity
	}{
		{"fatal error:", protocol.SeverityError},
		{"error:", protocol.SeverityError},
		{"warning:", protocol.SeverityWarning},
		{"note:", protocol.SeverityInformation},
	} {
		if m, ok := strings.CutPrefix(rest, p.prefix); ok {
			return p.sev, strings.TrimSpace(m), true
		}
	}
	return 0, "", false
}

func absIn(file, dir string) string {
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	return filepath.Clean(file)
}

// toDiagnostics turns raw messages into LSP diagnostics keyed by file. The
// files are read from disk to give each range the line's real extent: gas
// has no columns, and a range over the statement (leading blanks skipped)
// reads better than a zero-width mark at column 0.
func toDiagnostics(raw []rawDiag, main string) map[string][]protocol.Diagnostic {
	if len(raw) == 0 {
		return nil
	}
	texts := map[string][]string{}
	lineText := func(file string, line int) string {
		ls, ok := texts[file]
		if !ok {
			if b, err := os.ReadFile(file); err == nil {
				ls = strings.Split(string(b), "\n")
			}
			texts[file] = ls
		}
		if line < 0 || line >= len(ls) {
			return ""
		}
		return strings.TrimRight(ls[line], "\r")
	}
	out := map[string][]protocol.Diagnostic{}
	seen := map[string]bool{}
	add := func(file string, line, col int, sev protocol.DiagnosticSeverity, msg string) {
		key := file + "\x00" + strconv.Itoa(line) + "\x00" + msg
		if seen[key] {
			return
		}
		seen[key] = true
		text := lineText(file, line)
		start := len(text) - len(strings.TrimLeft(text, " \t"))
		if col >= 0 && col <= len(text) {
			start = col
		}
		end := len(text)
		if end <= start {
			end = start
		}
		out[file] = append(out[file], protocol.Diagnostic{
			Range: protocol.Range{
				Start: protocol.Position{Line: line, Character: docstore.UTF16Len(text[:start])},
				End:   protocol.Position{Line: line, Character: docstore.UTF16Len(text[:end])},
			},
			Severity: sev,
			Source:   Source,
			Message:  msg,
		})
	}
	for _, d := range raw {
		if d.line < 0 {
			d.line = 0
		}
		add(d.file, d.line, d.col, d.sev, d.msg)
		if d.via >= 0 && d.file != main {
			rel := filepath.Base(d.file)
			add(main, d.via, -1, d.sev, rel+":"+strconv.Itoa(d.line+1)+": "+d.msg)
		}
	}
	return out
}
