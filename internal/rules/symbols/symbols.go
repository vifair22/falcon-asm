// Package symbols holds the rules of roadmap section 10 (symbols and
// structure): undefined and unused labels, unused #define and .set
// symbols, duplicate definitions, unresolved and far-reaching local labels,
// and .globl/.extern hygiene.
//
// The document's own symbols come from one pass over the parsed program,
// shared through the run's memo. Everything outside the document (included
// files, the files that include it, other sources' globals, C sources and
// linker scripts) comes from lint.Workspace, which the caller builds over
// the workspace symbol index. Without a workspace the rules that need one
// stay quiet: a name unknown here may be defined anywhere.
package symbols

import (
	"fmt"
	"path"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Rule codes.
const (
	CodeUndefinedLabel       = "undefined-label"
	CodeUnusedLabel          = "unused-label"
	CodeUnusedDefine         = "unused-define"
	CodeDuplicateDefinition  = "duplicate-definition"
	CodeDuplicateLabel       = "duplicate-label"
	CodeLocalLabelUnresolved = "local-label-unresolved"
	CodeLocalLabelFar        = "local-label-far"
	CodeGloblUndefined       = "globl-undefined"
	CodeExternUnused         = "extern-unused"
)

// linkerSymbols are defined by the GNU linker's default scripts and by
// the toolchain itself, so no source defines them.
var linkerSymbols = map[string]bool{
	"_GLOBAL_OFFSET_TABLE_": true, "_DYNAMIC": true, "__ImageBase": true, "_end": true,
	"end": true, "_edata": true, "edata": true, "_etext": true, "etext": true,
	"__bss_start": true, "__executable_start": true, "__ehdr_start": true,
	"__init_array_start": true, "__init_array_end": true, "__fini_array_start": true,
	"__fini_array_end": true, "__preinit_array_start": true, "__preinit_array_end": true,
	"__dso_handle": true, "__TMC_END__": true, "_init": true, "_fini": true,
}

// entryNames are where execution starts without anything in the sources
// referring to them: the linker's ENTRY default, or the first byte of a
// flat binary.
var entryNames = map[string]bool{"_start": true, "__start": true, "start": true, "main": true, "_main": true}

func init() {
	register(CodeUndefinedLabel, lint.Warning, checkUndefined, lint.Doc{
		Summary: "A branch, call, $symbol or data reference to a symbol defined nowhere in the workspace.",
		Why: "gas does not complain about an undefined symbol: it emits a relocation and leaves the " +
			"problem to the linker, which reports it far from the typo, or, for a flat binary " +
			"linked with --noinhibit-exec or a forgiving script, resolves it to 0. The rule looks " +
			"in the file, the files it includes and that include it, every source's global labels, " +
			"the C sources and the linker scripts of the workspace, and the macro table. It stays " +
			"quiet when an include could not be found, and, for a .S file without the toolchain's " +
			"macro table, about ALL_CAPS names that a -D flag may define.",
		Bad:  "\tcall print_strng\nprint_string:\n\tret",
		Good: "\tcall print_string\nprint_string:\n\tret",
	})
	register(CodeUnusedLabel, lint.Info, checkUnusedLabel, lint.Doc{
		Summary: "A label that is not global and that nothing references, in the file or the workspace.",
		Why: "A label nobody jumps to, calls or takes the address of is either dead code left over " +
			"from a refactor or a reference that was meant to use it and spells it differently. " +
			"Global labels, entry points (_start and friends), labels inside .macro bodies and " +
			"labels a linker script names are never reported. Some labels are reached by offset " +
			"rather than by name: code placed with .org and entered through a vector or a far " +
			"pointer built elsewhere, an option ROM's entry at offset 3, strings walked one after " +
			"another. A label directly after `.org <constant>` or `. = <constant>` (only label " +
			"lines and directives that emit nothing between) is presumed reachable. Anywhere " +
			"else, say so on the label's line or in the comment lines directly above it: " +
			"`falcon-asm: reached=offset` (entered at its offset), `reached=external` (entered " +
			"from outside the sources), or `reached=sequence` (data walked from a previous label), " +
			"which also covers the labels of the data lines that directly follow, up to the " +
			"first blank, comment or code line. Text after a colon is a free-form reason.",
		Bad:  "\tjmp done\nretry:\n\tnop\ndone:\n\tret",
		Good: "\tjmp done\ndone:\n\tret",
	})
	register(CodeUnusedDefine, lint.Info, checkUnusedDefine, lint.Doc{
		Summary: "A #define, .set or .equ symbol that neither the file nor the files including it use.",
		Why: "Constants outlive the code that used them; an unused one misleads the next reader into " +
			"thinking some hardware value matters here. Exported (.globl) symbols are skipped, and " +
			"the rule is quiet when an include could not be found, since a header may be the user.",
		Bad:  "#define KBD_PORT 0x60\n#define PIT_PORT 0x40\n\tinb $KBD_PORT, %al",
		Good: "#define KBD_PORT 0x60\n\tinb $KBD_PORT, %al",
	})
	register(CodeDuplicateDefinition, lint.Warning, checkDuplicateGlobal, lint.Doc{
		Summary: "A global symbol also defined by a file assembled into the same program.",
		Why: "Two definitions of one global symbol fail at link time with \"multiple definition\", or " +
			"with -z muldefs silently keep the first. The rule reports a label defined both here and " +
			"in a file this one includes or is included by, and a global label also defined by " +
			"another source that is linked with this one; linked means one of the two references a " +
			"global only the other defines, so standalone programs that each have a _start are " +
			"not reported. The other definitions are listed as related locations.",
		Bad:  "# a.S\n\t.globl init\ninit:\n\tcall setup\n\tret\n# b.S\n\t.globl init, setup\ninit:\nsetup:\n\tret",
		Good: "# a.S\n\t.globl init\ninit:\n\tcall setup\n\tret\n# b.S\n\t.globl setup\nsetup:\n\tret",
	})
	register(CodeDuplicateLabel, lint.Error, checkDuplicateLabel, lint.Doc{
		Summary: "The same label defined twice in one file (outside alternative #if/.if branches).",
		Why: "gas rejects the second definition with \"symbol already defined\" but points only at " +
			"the second one; in a long boot file the first can be hundreds of lines away. The " +
			"finding names it and links to it. Definitions in different branches of one #if or " +
			".if chain are not duplicates, since only one is assembled.",
		Bad:  "loop:\n\tjmp loop\nloop:\n\thlt",
		Good: "spin:\n\tjmp spin\nhalt:\n\thlt",
	})
	register(CodeLocalLabelUnresolved, lint.Error, checkLocalUnresolved, lint.Doc{
		Summary: "A 1f with no following 1:, or a 1b with no preceding one.",
		Why: "A local label reference that has no target is an assembler error, but gas only says " +
			"\"undefined local label\" once the whole file has been read. A reference inside a .macro " +
			"body, or one whose search crosses a call to a macro that defines the label, is " +
			"resolved by the expansion and is not reported.",
		Bad:  "\tjz 1f\n\tnop\n2:\n\tret",
		Good: "\tjz 1f\n\tnop\n1:\n\tret",
	})
	register(CodeLocalLabelFar, lint.Info, checkLocalFar, lint.Doc{
		Summary: "A 1b/1f that resolves across a global or @function label, into another function.",
		Why: "Local labels are reused in every function, so a 1b meant for this function's loop " +
			"silently binds to the previous function's 1: when this one's was deleted or renamed. " +
			"A reference that crosses a .globl or .type @function label is almost always that " +
			"mistake.",
		Bad:  "1:\n\tret\n\t.globl g\ng:\n\tjmp 1b",
		Good: "\t.globl g\ng:\n1:\n\tjmp 1b",
	})
	register(CodeGloblUndefined, lint.Warning, checkGloblUndefined, lint.Doc{
		Summary: ".globl/.global naming a symbol this file does not define and nothing in the workspace defines.",
		Why: "gas accepts .globl for a symbol the file never defines and turns it into an undefined " +
			"external reference, so a typo in the export list silently exports nothing and the " +
			"link fails somewhere else. A name another source or a C file defines is an import " +
			"written as .globl, which is legal and not reported.",
		Bad:  "\t.globl stage2_entyr\nstage2_entry:\n\tret",
		Good: "\t.globl stage2_entry\nstage2_entry:\n\tret",
	})
	register(CodeExternUnused, lint.Info, checkExternUnused, lint.Doc{
		Summary: "An .extern declaration whose symbol the file never uses.",
		Why: "gas treats every undefined symbol as external, so .extern is documentation; one that " +
			"names a symbol nothing uses documents a dependency that no longer exists.",
		Bad:  "\t.extern panic\n\t.extern puts\n\tcall puts",
		Good: "\t.extern puts\n\tcall puts",
	})
}

func register(code string, sev lint.Severity, check func(*lint.Context, *table), doc lint.Doc) {
	lint.Register(&lint.Rule{
		Code: code, Section: 10, Default: sev, Langs: lint.LangAsm, Doc: doc,
		SkipExpansions: true,
		Check: func(c *lint.Context) {
			if c.Prog != nil && c.Prog.Doc != nil && isLinkerScript(c.Prog.Doc.URI) {
				return
			}
			if t := tableOf(c); t != nil {
				check(c, t)
			}
		},
	})
}

// isLinkerScript reports a preprocessed linker script (jmod.ld.S): it is a
// .S file to the build and the walk, but its "labels" are output sections
// and its symbols are the linker's.
func isLinkerScript(uri string) bool {
	b := strings.ToLower(path.Base(uri))
	ext := path.Ext(b)
	stem := strings.TrimSuffix(b, ext)
	return ext == ".ld" || ext == ".lds" || strings.HasSuffix(stem, ".ld") || strings.HasSuffix(stem, ".lds")
}

// macrosComplete reports whether the parse knew the whole cpp macro table:
// a plain .s file is not preprocessed, and a toolchain table always holds
// the compiler's builtins. Without it a -D on the command line may define
// any name.
func macrosComplete(c *lint.Context) bool {
	if strings.HasSuffix(c.Prog.Doc.URI, ".s") {
		return true
	}
	_, gnu := c.Prog.Macros["__GNUC__"]
	_, asmb := c.Prog.Macros["__ASSEMBLER__"]
	return gnu || asmb
}

// looksLikeMacro is the shape -D macros take: no lowercase letter.
func looksLikeMacro(name string) bool {
	upper := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			return false
		}
		upper = upper || c >= 'A' && c <= 'Z'
	}
	return upper
}

// knownElsewhere reports a name the document does not define but can
// still legitimately use.
func knownElsewhere(c *lint.Context, t *table, ws lint.Workspace, name string) bool {
	if _, ok := c.Prog.Macros[name]; ok {
		return true
	}
	if linkerSymbols[name] || strings.HasPrefix(name, "__start_") || strings.HasPrefix(name, "__stop_") {
		return true
	}
	if !macrosComplete(c) && looksLikeMacro(name) {
		return true
	}
	return ws.Defined(name)
}

func checkUndefined(c *lint.Context, t *table) {
	ws := c.Workspace()
	if ws == nil || !ws.Complete() {
		return
	}
	verdict := map[string]bool{}
	for _, r := range t.refs {
		if t.defined[r.name] || t.declared[r.name] {
			continue
		}
		bad, seen := verdict[r.name]
		if !seen {
			bad = !knownElsewhere(c, t, ws, r.name)
			verdict[r.name] = bad
		}
		if !bad {
			continue
		}
		msg := fmt.Sprintf("undefined symbol %s: not defined in this file, the files it includes or the workspace", r.name)
		c.ReportRange(r.start, r.end, msg, renameFix(c, t, r.name, r.start, r.end)...)
	}
}

func checkUnusedLabel(c *lint.Context, t *table) {
	ws := c.Workspace()
	if ws == nil {
		return
	}
	seen := map[string]bool{}
	var ri *reachInfo
	for _, d := range t.labels {
		if seen[d.name] {
			continue
		}
		seen[d.name] = true
		if t.exported[d.name] || entryNames[d.name] || t.uses[d.name] > 0 || ws.Referenced(d.name) {
			continue
		}
		// Only computed once a label is otherwise unused, which most
		// documents never reach.
		if ri == nil {
			r := reachedLabels(c.Prog, t)
			ri = &r
		}
		if ri.reached[d.name] {
			continue
		}
		msg := fmt.Sprintf("label %s is never referenced", d.name)
		if v, ok := ri.bad[d.name]; ok {
			msg += fmt.Sprintf(" (reached=%s is not one of offset, sequence, external)", v)
		}
		c.ReportRange(d.start, d.end, msg)
	}
}

func checkUnusedDefine(c *lint.Context, t *table) {
	ws := c.Workspace()
	if ws == nil || !ws.Complete() {
		return
	}
	seen := map[string]bool{}
	report := func(d def, what string) {
		if seen[d.name] {
			return
		}
		seen[d.name] = true
		if t.exported[d.name] || t.uses[d.name] > 0 || ws.Referenced(d.name) {
			return
		}
		c.ReportRange(d.start, d.end, fmt.Sprintf("%s %s is never used", what, d.name))
	}
	for _, d := range t.defines {
		report(d, "macro")
	}
	for _, d := range t.equs {
		report(d, "symbol")
	}
}

func checkDuplicateLabel(c *lint.Context, t *table) {
	first := map[string][]def{}
	for _, d := range t.labels {
		prev := first[d.name]
		for _, p := range prev {
			if exclusive(t.cond[p.stmt], t.cond[d.stmt]) {
				continue
			}
			c.ReportFinding(lint.Finding{
				Range:   rangeOf(c, d.start, d.end),
				Message: fmt.Sprintf("label %s is already defined at line %d", d.name, c.Prog.Stmts[p.stmt].Line+1),
				Related: []protocol.Location{{URI: c.Prog.Doc.URI, Range: rangeOf(c, p.start, p.end)}},
			})
			break
		}
		first[d.name] = append(prev, d)
	}
}

func checkDuplicateGlobal(c *lint.Context, t *table) {
	ws := c.Workspace()
	if ws == nil {
		return
	}
	done := map[string]bool{}
	for _, d := range t.labels {
		if !t.exported[d.name] || done[d.name] {
			continue
		}
		done[d.name] = true
		locs := ws.GlobalDefinitions(d.name)
		if len(locs) == 0 {
			continue
		}
		where := make([]string, 0, len(locs))
		for _, l := range locs {
			where = append(where, fmt.Sprintf("%s line %d", path.Base(l.URI), l.Range.Start.Line+1))
		}
		c.ReportFinding(lint.Finding{
			Range:   rangeOf(c, d.start, d.end),
			Message: fmt.Sprintf("global symbol %s is also defined in %s", d.name, strings.Join(where, ", ")),
			Related: locs,
		})
	}
}

func checkLocalUnresolved(c *lint.Context, t *table) {
	for _, r := range t.localRefs {
		if _, st := c.Prog.ResolveLocal(r.stmt, r.text); st == asm.LocalMissing {
			name := r.text[:len(r.text)-1]
			msg := fmt.Sprintf("%s: no local label %s: follows", r.text, name)
			if r.text[len(r.text)-1] == 'b' {
				msg = fmt.Sprintf("%s: no local label %s: precedes it", r.text, name)
			}
			c.ReportRange(r.start, r.end, msg)
		}
	}
}

// boundary reports a label that starts a function: global, or typed
// @function. Assembler-local .L labels never do.
func (t *table) boundary(name string) bool {
	return !strings.HasPrefix(name, ".L") && (t.exported[name] || t.function[name])
}

func checkLocalFar(c *lint.Context, t *table) {
	p := c.Prog
	for _, r := range t.localRefs {
		target, st := p.ResolveLocal(r.stmt, r.text)
		if st != asm.LocalFound {
			continue
		}
		name := r.text[:len(r.text)-1]
		crossed := ""
		line := 0
		check := func(i int, labels []string) {
			for _, l := range labels {
				if crossed == "" && t.boundary(l) {
					crossed, line = l, p.Stmts[i].Line+1
				}
			}
		}
		if r.text[len(r.text)-1] == 'b' {
			// Labels after N: on the target's statement, everything up to
			// and including the reference's own labels.
			check(target, after(p.Stmts[target].Labels, name))
			for i := target + 1; i <= r.stmt; i++ {
				check(i, p.Stmts[i].Labels)
			}
		} else {
			for i := r.stmt + 1; i < target; i++ {
				check(i, p.Stmts[i].Labels)
			}
			check(target, before(p.Stmts[target].Labels, name))
		}
		if crossed != "" {
			c.ReportRange(r.start, r.end, fmt.Sprintf("%s crosses the global label %s (line %d): it resolves to a local label of another function", r.text, crossed, line))
		}
	}
}

func after(labels []string, name string) []string {
	for i, l := range labels {
		if l == name {
			return labels[i+1:]
		}
	}
	return nil
}

func before(labels []string, name string) []string {
	for i, l := range labels {
		if l == name {
			return labels[:i]
		}
	}
	return labels
}

func checkGloblUndefined(c *lint.Context, t *table) {
	ws := c.Workspace()
	if ws == nil || !ws.Complete() {
		return
	}
	for _, g := range t.globals {
		if t.defined[g.name] || strings.Contains(g.name, "\\") || knownElsewhere(c, t, ws, g.name) {
			continue
		}
		msg := fmt.Sprintf(".globl %s: %s is not defined in this file or anywhere in the workspace, so gas makes it an undefined external", g.name, g.name)
		c.ReportRange(g.start, g.end, msg, renameFix(c, t, g.name, g.start, g.end)...)
	}
}

func checkExternUnused(c *lint.Context, t *table) {
	ws := c.Workspace()
	for _, e := range t.externs {
		if t.uses[e.name] > 0 || ws != nil && ws.Referenced(e.name) {
			continue
		}
		var fixes []lint.Fix
		if s := &c.Prog.Stmts[e.stmt]; len(s.Args) == 1 && len(c.Prog.StmtAt(s.Line)) == 1 && s.Line == s.EndLine {
			fixes = append(fixes, lint.NewFix("Remove the .extern", deleteLine(c, s)))
		}
		c.ReportRange(e.start, e.end, fmt.Sprintf(".extern %s is never used", e.name), fixes...)
	}
}

// deleteLine is an edit removing a statement's whole line, line break
// included.
func deleteLine(c *lint.Context, s *asm.Stmt) protocol.TextEdit {
	src := c.Prog.Doc.Text
	start := s.Start
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	end := s.End
	for end < len(src) && src[end] != '\n' {
		end++
	}
	if end < len(src) {
		end++
	}
	return c.ReplaceRange(start, end, "")
}

func rangeOf(c *lint.Context, start, end int) protocol.Range {
	return c.Prog.Range(start, end)
}

// renameFix offers the closest label of the file when it is within two
// edits of name: the usual undefined symbol is a typo of a defined one.
func renameFix(c *lint.Context, t *table, name string, start, end int) []lint.Fix {
	best, bestD := "", 3
	try := func(cand string) {
		if cand == name || len(cand) < 3 {
			return
		}
		if d := distance(name, cand, bestD); d < bestD {
			best, bestD = cand, d
		}
	}
	for _, d := range t.labels {
		try(d.name)
	}
	for _, d := range t.equs {
		try(d.name)
	}
	if best == "" || bestD*3 >= len(name) {
		return nil
	}
	return []lint.Fix{lint.NewFix("Change to "+best, c.ReplaceRange(start, end, best))}
}

// distance is the Levenshtein distance of a and b, or limit when it is at
// least limit (the early exit keeps a large file's candidates cheap).
func distance(a, b string, limit int) int {
	if d := len(a) - len(b); d >= limit || -d >= limit {
		return limit
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		low := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			low = min(low, cur[j])
		}
		if low >= limit {
			return limit
		}
		prev, cur = cur, prev
	}
	return min(prev[len(b)], limit)
}
