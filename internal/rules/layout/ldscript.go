package layout

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// The linker script reader understands the subset boot code uses: a
// SECTIONS block of `. = <number>;` and output sections with a literal
// address or one following such an assignment, filled from input section
// lists. Anything it does not understand makes the location unknown, and
// an unknown address is never compared, so a complex script costs only
// silence.

// outSect is one output section of a script.
type outSect struct {
	name      string
	addr      int64
	addrKnown bool
	discard   bool
	line      int // zero-based line in the script
	inputs    []inputSpec
}

// inputSpec is `filepat(secpat ...)`.
type inputSpec struct {
	file string
	secs []string
}

type ldScript struct {
	path  string
	sects []outSect
	names map[string]bool // section names written literally in the script
}

// fsys is the file access of the script search; tests replace it.
var fsys = struct {
	readDir  func(string) ([]os.DirEntry, error)
	readFile func(string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
}{os.ReadDir, os.ReadFile, os.Stat}

// maxScriptDepth bounds the walk up from a source file: linker scripts sit
// beside the sources or a few directories above them, and walking to / on
// every save would read unrelated trees.
const maxScriptDepth = 8

// scriptCache keeps parsed scripts by path, keyed on size and mtime, so a
// save re-reads only scripts that changed.
var scriptCache = struct {
	sync.Mutex
	m map[string]cachedScript
}{m: map[string]cachedScript{}}

type cachedScript struct {
	mod  time.Time
	size int64
	s    *ldScript
}

// findScripts returns the parsed *.ld and *.lds scripts in the source's
// directory and its parents, up to and including the first directory that
// holds .git or .falcon-asm.json (the project root).
func findScripts(src string) []*ldScript {
	var out []*ldScript
	dir := filepath.Dir(src)
	for depth := 0; depth < maxScriptDepth; depth++ {
		ents, err := fsys.readDir(dir)
		if err == nil {
			root := false
			for _, e := range ents {
				n := e.Name()
				if n == ".git" || n == lint.ConfigFile {
					root = true
				}
				if e.IsDir() || (filepath.Ext(n) != ".ld" && filepath.Ext(n) != ".lds") {
					continue
				}
				if s := loadScript(filepath.Join(dir, n)); s != nil {
					out = append(out, s)
				}
			}
			if root {
				break
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

func loadScript(p string) *ldScript {
	fi, err := fsys.stat(p)
	if err != nil {
		return nil
	}
	scriptCache.Lock()
	c, ok := scriptCache.m[p]
	scriptCache.Unlock()
	if ok && c.mod.Equal(fi.ModTime()) && c.size == fi.Size() {
		return c.s
	}
	data, err := fsys.readFile(p)
	if err != nil {
		return nil
	}
	s := parseScript(p, string(data))
	scriptCache.Lock()
	scriptCache.m[p] = cachedScript{mod: fi.ModTime(), size: fi.Size(), s: s}
	scriptCache.Unlock()
	return s
}

// ldTok is a script token with its line.
type ldTok struct {
	s    string
	line int
}

// lexScript splits a script into words and the punctuation that
// structures it, dropping /* */ comments.
func lexScript(src string) []ldTok {
	var out []ldTok
	line := 0
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out
			}
			line += strings.Count(src[i:i+2+end+2], "\n")
			i += 2 + end + 2
		case strings.IndexByte("{}();:=,", c) >= 0:
			out = append(out, ldTok{string(c), line})
			i++
		default:
			j := i
			for j < len(src) && !strings.ContainsRune(" \t\r\n{}();:=,", rune(src[j])) &&
				!(src[j] == '/' && j+1 < len(src) && src[j+1] == '*') {
				j++
			}
			out = append(out, ldTok{src[i:j], line})
			i = j
		}
	}
	return out
}

// sectionTypes are the output section types written in parentheses before
// the colon; they do not affect the address.
var sectionTypes = map[string]bool{"NOLOAD": true, "DSECT": true, "COPY": true, "INFO": true, "OVERLAY": true, "READONLY": true}

// parseScript reads the SECTIONS block. It returns a script with no
// sections when the block is missing or uses something unsupported
// before any address could be trusted.
func parseScript(p, src string) *ldScript {
	s := &ldScript{path: p, names: map[string]bool{}}
	toks := lexScript(src)
	i := 0
	for i < len(toks) && !(toks[i].s == "SECTIONS" && i+1 < len(toks) && toks[i+1].s == "{") {
		i++
	}
	if i >= len(toks) {
		return s
	}
	i += 2
	loc, locKnown := int64(0), false
	for i < len(toks) && toks[i].s != "}" {
		t := toks[i]
		switch {
		case t.s == ";":
			i++
		case i+1 < len(toks) && toks[i+1].s == "=":
			// An assignment: to `.` it moves the location; to a symbol
			// it changes nothing that matters here.
			j := i + 2
			for j < len(toks) && toks[j].s != ";" && toks[j].s != "}" {
				j++
			}
			if t.s == "." {
				loc, locKnown = 0, false
				if j == i+3 {
					if v, ok := parseNum(toks[i+2].s); ok {
						loc, locKnown = v, true
					}
				}
			}
			i = j
		case i+1 < len(toks) && toks[i+1].s == "(" && isKeyword(t.s):
			// ENTRY(...), ASSERT(...), PROVIDE(...) at the top level.
			i = skipParens(toks, i+1)
		case isKeyword(t.s):
			// OVERLAY and anything else structural: give up on
			// addresses from here on.
			return s
		default:
			o, next, ok := parseOutSect(toks, i, loc, locKnown)
			if !ok {
				return s
			}
			for _, in := range o.inputs {
				for _, sec := range in.secs {
					if !strings.ContainsAny(sec, "*?[") {
						s.names[sec] = true
					}
				}
			}
			s.sects = append(s.sects, o)
			// The next section follows this one at an address that
			// depends on every object's contribution: unknown.
			loc, locKnown = 0, false
			i = next
		}
	}
	return s
}

func isKeyword(w string) bool {
	if w == "" || w[0] < 'A' || w[0] > 'Z' {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := w[i]
		if !(c >= 'A' && c <= 'Z' || c == '_' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// skipParens returns the index after the parenthesis group opening at i.
func skipParens(toks []ldTok, i int) int {
	depth := 0
	for ; i < len(toks); i++ {
		switch toks[i].s {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return i
}

func parseNum(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		u, err2 := strconv.ParseUint(s, 0, 64)
		if err2 != nil {
			return 0, false
		}
		v = int64(u)
	}
	return v, true
}

// parseOutSect reads `name [addr] [(TYPE)] : [AT(...)] { inputs } [> region]`.
func parseOutSect(toks []ldTok, i int, loc int64, locKnown bool) (outSect, int, bool) {
	o := outSect{name: toks[i].s, line: toks[i].line, discard: toks[i].s == "/DISCARD/"}
	i++
	var addr []string
	for i < len(toks) && toks[i].s != ":" {
		switch {
		case toks[i].s == "(" && i+2 < len(toks) && toks[i+2].s == ")" && sectionTypes[toks[i+1].s]:
			i += 3
		case toks[i].s == "{" || toks[i].s == "}" || toks[i].s == ";":
			return o, i, false
		default:
			addr = append(addr, toks[i].s)
			i++
		}
	}
	if i >= len(toks) {
		return o, i, false
	}
	i++ // ':'
	switch len(addr) {
	case 0:
		o.addr, o.addrKnown = loc, locKnown
	case 1:
		o.addr, o.addrKnown = parseNum(addr[0])
	}
	// AT(lma), ALIGN(n), SUBALIGN(n) before the brace. ALIGN moves the
	// address, so it makes an implicit address unknown.
	for i < len(toks) && toks[i].s != "{" {
		if toks[i].s == "ALIGN" && len(addr) == 0 {
			o.addrKnown = false
		}
		if i+1 < len(toks) && toks[i+1].s == "(" {
			i = skipParens(toks, i+1)
			continue
		}
		i++
	}
	if i >= len(toks) {
		return o, i, false
	}
	end := i
	depth := 0
	for ; end < len(toks); end++ {
		if toks[end].s == "{" {
			depth++
		} else if toks[end].s == "}" {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if end >= len(toks) {
		return o, end, false
	}
	o.inputs = parseInputs(toks[i+1 : end])
	i = end + 1
	// Trailing region, phdr and fill: `> RAM`, `AT> ROM`, `:hdr`, `=0x90`.
	for i < len(toks) {
		switch w := toks[i].s; {
		case w == ">" || w == "AT>" || w == ":" || w == "=":
			i += 2
		case strings.HasPrefix(w, ">") || strings.HasPrefix(w, "AT>"):
			i++
		default:
			return o, i, true
		}
	}
	return o, i, true
}

// parseInputs reads the input section descriptions of one output
// section, looking through KEEP and SORT wrappers. Assignments and
// symbol definitions inside are skipped.
func parseInputs(toks []ldTok) []inputSpec {
	var out []inputSpec
	for i := 0; i < len(toks); i++ {
		w := toks[i].s
		if i+1 >= len(toks) || toks[i+1].s != "(" {
			continue
		}
		switch {
		case w == "KEEP":
			i++ // look inside: the next word is the file pattern
			continue
		case isKeyword(w):
			// SORT(...) at file level, PROVIDE(...), LONG(...), ...
			i = skipParens(toks, i+1) - 1
			continue
		}
		spec := inputSpec{file: w}
		end := skipParens(toks, i+1)
		for j := i + 2; j < end-1; j++ {
			s := toks[j].s
			switch {
			case s == "(" || s == ")" || s == ",":
			case s == "EXCLUDE_FILE" && j+1 < end && toks[j+1].s == "(":
				j = skipParens(toks, j+1) - 1
			case isKeyword(s):
				// SORT_BY_NAME( ... ): its words are the patterns.
			default:
				spec.secs = append(spec.secs, s)
			}
		}
		out = append(out, spec)
		i = end - 1
	}
	return out
}

// standardSections are too common to tie a script to a file: every object
// has them, so a script mentioning .text says nothing about which sources
// it links.
var standardSections = map[string]bool{".text": true, ".data": true, ".bss": true, ".rodata": true}

// placement is one output section with a known address and the lower
// bound on its size this file alone contributes.
type placement struct {
	out    *outSect
	size   uint64
	inputs []string
}

// place assigns this object's sections to the script's output sections the
// way ld does (the first matching input description wins) and returns the
// output sections with a known address that receive any bytes. ok is false
// when the script does not name, literally, any non-standard section of
// this file: then there is no evidence the file is linked with it at all.
func (s *ldScript) place(obj string, sizes map[string]uint64, used map[string]bool) ([]placement, bool) {
	tied := false
	for sec := range used {
		if s.names[sec] && !standardSections[sec] {
			tied = true
		}
	}
	if !tied {
		return nil, false
	}
	secs := make([]string, 0, len(used))
	for sec := range used {
		secs = append(secs, sec)
	}
	sort.Strings(secs)
	byOut := map[int]*placement{}
	for _, sec := range secs {
		sz := sizes[sec]
		if sz == 0 {
			continue
		}
		oi := s.match(obj, sec)
		if oi < 0 || s.sects[oi].discard {
			continue
		}
		pl := byOut[oi]
		if pl == nil {
			pl = &placement{out: &s.sects[oi]}
			byOut[oi] = pl
		}
		pl.size += sz
		pl.inputs = append(pl.inputs, sec)
	}
	var out []placement
	for oi := range s.sects {
		if pl := byOut[oi]; pl != nil && pl.out.addrKnown {
			out = append(out, *pl)
		}
	}
	return out, true
}

// match finds the output section an input section goes to: the first
// input description whose file pattern can name this object and whose
// section pattern matches. -1 when none does (an orphan, placed by ld's
// own rules).
func (s *ldScript) match(obj, sec string) int {
	for oi := range s.sects {
		for _, in := range s.sects[oi].inputs {
			if in.file != "*" {
				if ok, _ := path.Match(in.file, obj); !ok {
					continue
				}
			}
			for _, pat := range in.secs {
				if ok, _ := path.Match(pat, sec); ok {
					return oi
				}
			}
		}
	}
	return -1
}

func checkSectionOverlap(c *lint.Context) {
	p, res := c.Prog, c.Listing
	src, ok := docPath(p)
	if !ok || res == nil || len(res.SectionSizes) == 0 {
		return
	}
	used := map[string]bool{}
	for i := range p.Stmts {
		if st := &p.Stmts[i]; !st.InMacroDef && emitting(st) {
			used[st.Section] = true
		}
	}
	obj := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)) + ".o"
	for _, s := range findScripts(src) {
		pls, ok := s.place(obj, res.SectionSizes, used)
		if !ok {
			continue
		}
		reported := map[string]bool{}
		for i := range pls {
			a := &pls[i]
			end := uint64(a.out.addr) + a.size
			// Every fixed-address output section of the script is an
			// obstacle, not just the ones this file fills: a split loader
			// links stage 2 from other objects, and stage 1 running into
			// its address is the overflow that matters. An output section
			// with no input descriptions only reserves space and is not
			// one.
			for k := range s.sects {
				b := &s.sects[k]
				if b == a.out || !b.addrKnown || b.discard || len(b.inputs) == 0 {
					continue
				}
				if b.addr < a.out.addr || b.addr == a.out.addr && k < outIndex(s, a.out) {
					continue
				}
				if uint64(b.addr) >= end || reported[a.out.name] {
					continue
				}
				reported[a.out.name] = true
				reportOverlap(c, s, a, b)
			}
		}
	}
}

// outIndex is the position of o in the script's output sections.
func outIndex(s *ldScript, o *outSect) int {
	for k := range s.sects {
		if &s.sects[k] == o {
			return k
		}
	}
	return -1
}

func reportOverlap(c *lint.Context, s *ldScript, a *placement, b *outSect) {
	p := c.Prog
	at := sectionStmt(p, a.inputs)
	if at < 0 {
		return
	}
	msg := fmt.Sprintf("%s goes to output section %s at %#x, which holds at least %d bytes from this file and so runs to %#x, "+
		"past output section %s at %#x (%s line %d)",
		strings.Join(a.inputs, ", "), a.out.name, a.out.addr, a.size, uint64(a.out.addr)+a.size,
		b.name, b.addr, filepath.Base(s.path), b.line+1)
	st := &p.Stmts[at]
	pos := protocol.Position{Line: b.line}
	c.ReportFinding(lint.Finding{
		Range:   p.Range(st.Start, st.End),
		Message: msg,
		Related: []protocol.Location{{URI: toolchain.PathToURI(s.path), Range: protocol.Range{Start: pos, End: pos}}},
	})
}

// sectionStmt is the statement that opens one of secs: a .section,
// .pushsection, .text, .data or .bss naming it, else the first statement
// placed in it.
func sectionStmt(p *asm.Program, secs []string) int {
	want := map[string]bool{}
	for _, s := range secs {
		want[s] = true
	}
	first := -1
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.InMacroDef || st.Kind != asm.Directive && st.Kind != asm.Instr {
			continue
		}
		switch st.Name {
		case ".text", ".data", ".bss":
			if want[st.Name] {
				return i
			}
		case ".section", ".pushsection":
			if len(st.Args) > 0 {
				if f := strings.Fields(st.Args[0]); len(f) > 0 && want[strings.Trim(f[0], "\"")] {
					return i
				}
			}
		}
		if first < 0 && want[st.Section] {
			first = i
		}
	}
	return first
}

func init() {
	lint.Register(&lint.Rule{
		Code:         "section-overlap",
		Section:      6,
		Default:      lint.Error,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "Sections a linker script places at fixed addresses collide.",
			Why: "Boot images place each stage at the address it is loaded to (`.stage1 0x7C00 : { ... }`, " +
				"`.stage2 0x7E00 : { ... }`). When stage 1 grows past 512 bytes it runs into stage 2, and " +
				"depending on the script ld either stops with a terse VMA overlap error at link time or " +
				"the image is simply wrong. This rule reads the *.ld and *.lds scripts beside the file " +
				"and in the directories above it (up to the project root), places the file's sections " +
				"the way ld does, and compares the address ranges using the section sizes from the " +
				"assembled object. It is conservative: only literal addresses (`. = 0x7C00;` or an " +
				"address on the output section) are used, sizes are this file's contribution only (a " +
				"lower bound), and a script is only considered when it names one of the file's own " +
				"non-standard sections literally. Needs the toolchain.",
			Bad:  "/* boot.ld: SECTIONS { .stage1 0x7C00 : { *(.text.stage1) }  .stage2 0x7D00 : { *(.text.stage2) } } */\n\t.section .text.stage1, \"ax\"\n\t.fill 300, 1, 0x90",
			Good: "/* boot.ld: SECTIONS { .stage1 0x7C00 : { *(.text.stage1) }  .stage2 0x7E00 : { *(.text.stage2) } } */\n\t.section .text.stage1, \"ax\"\n\t.fill 300, 1, 0x90",
		},
		Check: checkSectionOverlap,
	})
}
