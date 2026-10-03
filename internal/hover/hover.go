// Package hover answers textDocument/hover: it finds the token under the
// cursor and renders a few lines of markdown about it, drawn from the isa
// tables (instructions, registers, directives, prefixes) or from the
// document itself (labels, local labels, numeric literals).
//
// The whole document is tokenized on every request. The lexer is fast enough
// that this costs well under a millisecond for real files, and statement
// context (is this word a mnemonic or a label reference? where is the next
// "1:"?) is only correct when lexing starts at the top.
package hover

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/constexpr"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/symbols"
)

// Hover returns the hover for pos in doc, or nil when the cursor is not on a
// token there is anything useful to say about. The signature matches
// server.HoverFunc.
func Hover(doc *docstore.Document, pos protocol.Position) (*protocol.Hover, error) {
	return HoverWith(doc, pos, Options{})
}

// Macro is a cpp macro as hover shows it. It mirrors toolchain.Macro so this
// package stays free of the exec side; the caller converts.
type Macro struct {
	Value    string
	Function bool   // function-like: NAME(Params) Value
	Params   string // parameter list without parentheses
	Def      *protocol.Location
}

// Options carries what the caller knows beyond the document text. The zero
// value gives exactly Hover's result.
type Options struct {
	// Tokens is lexer.Tokenize(doc.Text) when the caller already has it;
	// nil means tokenize here.
	Tokens []lexer.Token
	// Macro resolves a cpp macro name: the toolchain's table and the
	// document's own #defines. A word that names a macro is hovered as the
	// macro, since cpp substitutes it before the assembler sees a label.
	Macro func(name string) (Macro, bool)
	// LineNumber maps a zero-based line of doc to the zero-based line shown
	// to the user. Inline asm hovers over the template as a document of its
	// own, whose line 3 is somewhere else entirely in the C file; nil means
	// lines are shown as they are.
	LineNumber func(line int) int
}

// HoverWith is Hover with outside knowledge applied; see Options.
func HoverWith(doc *docstore.Document, pos protocol.Position, opts Options) (*protocol.Hover, error) {
	off, err := doc.Offset(pos)
	if err != nil {
		return nil, fmt.Errorf("hover: %w", err)
	}
	if pos.Line >= doc.LineCount() {
		return nil, nil
	}
	lineStart, err := doc.Offset(protocol.Position{Line: pos.Line})
	if err != nil {
		return nil, fmt.Errorf("hover: %w", err)
	}
	toks := opts.Tokens
	if toks == nil {
		toks = lexer.Tokenize(doc.Text)
	}
	idx := tokenAt(toks, pos.Line, off-lineStart)
	if idx < 0 {
		return nil, nil
	}
	r := renderer{doc: doc, toks: toks, macro: opts.Macro, lineNo: opts.LineNumber}
	if r.lineNo == nil {
		r.lineNo = func(line int) int { return line }
	}
	body := r.render(idx)
	if body == "" {
		return nil, nil
	}
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: body},
		Range:    tokenRange(doc, toks[idx]),
	}, nil
}

// tokenAt finds the token on line covering byte column col. A cursor sitting
// exactly between two tokens ("%ax|," or "movw|") belongs to the token that
// starts there, as editors place the caret before the character it hovers;
// the one exception is punctuation, which never has a hover, so a word that
// ends at the cursor wins over a comma that starts there. That keeps hovering
// at the end of a word working, which is where the caret sits after typing.
func tokenAt(toks []lexer.Token, line, col int) int {
	ending := -1
	for i, t := range toks {
		if t.Line < line {
			continue
		}
		if t.Line > line {
			break
		}
		if t.Col <= col && col < t.Col+t.Len {
			if t.Kind == lexer.Punct && ending >= 0 {
				return ending
			}
			return i
		}
		if t.Col+t.Len == col && t.Kind != lexer.Punct {
			ending = i
		}
	}
	return ending
}

// tokenRange converts a token's byte span to the UTF-16 range LSP wants. The
// prefix of the line is measured, not assumed ASCII, because a comment or
// string earlier on the line may hold multi-byte characters.
func tokenRange(doc *docstore.Document, t lexer.Token) *protocol.Range {
	text, _ := doc.Line(t.Line)
	end := min(t.Col+t.Len, len(text))
	start := min(t.Col, end)
	sc := docstore.UTF16Len(text[:start])
	return &protocol.Range{
		Start: protocol.Position{Line: t.Line, Character: sc},
		End:   protocol.Position{Line: t.Line, Character: sc + docstore.UTF16Len(text[start:end])},
	}
}

type renderer struct {
	doc    *docstore.Document
	toks   []lexer.Token
	macro  func(string) (Macro, bool)
	lineNo func(int) int
}

func (r *renderer) render(idx int) string {
	doc, toks := r.doc, r.toks
	t := toks[idx]
	switch t.Kind {
	case lexer.Mnemonic:
		if s := mnemonicHover(t.Text); s != "" {
			return s
		}
		if isa.IsPrefix(t.Text) {
			return prefixHover(t.Text)
		}
		if s := r.cppMacroHover(t.Text); s != "" {
			return s
		}
		return macroHover(doc, toks, t.Text, r.lineNo)
	case lexer.Prefix:
		return prefixHover(t.Text)
	case lexer.Register:
		return registerHover(t.Text)
	case lexer.Directive:
		return directiveHover(t.Text)
	case lexer.Immediate:
		atom := strings.TrimPrefix(t.Text, "$")
		if v, ok := parseNumber(atom); ok {
			return numberHover(t.Text, v)
		}
		if isSymbol(atom) {
			if s := r.cppMacroHover(atom); s != "" {
				return s
			}
			return labelHover(doc, toks, atom, r.lineNo)
		}
	case lexer.Number:
		if v, ok := parseNumber(t.Text); ok {
			return numberHover(t.Text, v)
		}
	case lexer.LocalLabelRef:
		return localLabelHover(doc, toks, idx, r.lineNo)
	case lexer.LabelRef:
		if s := r.cppMacroHover(t.Text); s != "" {
			return s
		}
		return labelHover(doc, toks, t.Text, r.lineNo)
	case lexer.LabelDef:
		return labelHover(doc, toks, t.Text, r.lineNo)
	case lexer.Identifier:
		return r.cppMacroHover(t.Text)
	}
	return ""
}

// cppMacroHover shows a macro's definition and, for an object-like macro
// whose body is a constant expression, its value: the number the assembler
// will actually see, which is the point of hovering a selector or a port.
func (r *renderer) cppMacroHover(name string) string {
	if r.macro == nil {
		return ""
	}
	m, ok := r.macro(name)
	if !ok {
		return ""
	}
	def := "#define " + name
	if m.Function {
		def += "(" + m.Params + ")"
	}
	if m.Value != "" {
		def += " " + m.Value
	}
	lines := []string{bold(name) + " - macro", code(def)}
	if !m.Function && m.Value != "" {
		lookup := func(n string) (string, bool) {
			mm, ok := r.macro(n)
			if !ok || mm.Function {
				return "", false
			}
			return mm.Value, true
		}
		if v, err := constexpr.Eval(m.Value, lookup); err == nil {
			lines = append(lines, "Value: "+formatValue(v))
		}
	}
	if m.Def != nil {
		lines = append(lines, fmt.Sprintf("Defined in %s line %d", code(path.Base(m.Def.URI)), m.Def.Range.Start.Line+1))
	}
	return join(lines)
}

// formatValue shows an evaluated macro in hex and decimal. A negative value
// is shown signed in both, since its 64-bit two's complement hex would say
// less than "-0x1".
func formatValue(v int64) string {
	if v < 0 {
		return fmt.Sprintf("hex `-0x%X` - dec `%d`", uint64(-v), v)
	}
	return fmt.Sprintf("hex `0x%X` - dec `%d`", v, v)
}

// suffixBits maps an AT&T operand-size suffix to its width.
var suffixBits = map[string]int{"b": 8, "w": 16, "l": 32, "q": 64}

// maxForms bounds the operand forms listed, so an instruction like mov with
// dozens of encodings still fits the few-line budget.
const maxForms = 4

func mnemonicHover(word string) string {
	inst, suffix, ok := isa.Lookup(word)
	if !ok {
		return ""
	}
	var lines []string

	title := bold(word)
	lw := strings.ToLower(word)
	// Aliases that are just the name plus a size letter (inb, outl, iretw)
	// read better as a suffix than as an unrelated spelling.
	if suffix == "" && len(lw) == len(inst.Name)+1 && strings.HasPrefix(lw, inst.Name) {
		if _, ok := suffixBits[lw[len(lw)-1:]]; ok {
			suffix = lw[len(lw)-1:]
		}
	}
	switch {
	case suffix != "":
		title += fmt.Sprintf(" = %s + %s (%d-bit)", escape(inst.Name), suffix, suffixBits[suffix])
	case lw != inst.Name:
		title += " (alias of " + escape(inst.Name) + ")"
	}
	if inst.Summary != "" {
		title += " - " + escape(inst.Summary)
	}
	lines = append(lines, title)

	if forms := formsLine(inst, suffix); forms != "" {
		lines = append(lines, forms)
	}
	if meta := metaLine(inst); meta != "" {
		lines = append(lines, meta)
	}
	lines = append(lines, flagsLine(inst))
	if inst.Notes != "" {
		lines = append(lines, escape(inst.Notes))
	}
	return join(lines)
}

// formsLine lists the distinct operand forms, lowest x86-64 level first so
// the encoding usable on the most machines leads (vpermd shows its AVX2 form
// before the AVX-512 ones). With an AT&T size suffix only the forms of that
// width are listed, since movw has no use for the r/m8 and r/m64 encodings;
// when nothing matches (string forms carry no width) every form is kept.
func formsLine(inst *isa.Instruction, suffix string) string {
	forms := append([]isa.Form(nil), inst.Forms...)
	sort.SliceStable(forms, func(i, j int) bool {
		return levelRank(forms[i].Level) < levelRank(forms[j].Level)
	})
	if bits, ok := suffixBits[suffix]; ok {
		var kept []isa.Form
		for _, f := range forms {
			if match, sized := formWidth(f.Operands, bits); match || !sized {
				kept = append(kept, f)
			}
		}
		if len(kept) > 0 {
			forms = kept
		}
	}
	seen := map[string]bool{}
	var distinct []string
	for _, f := range forms {
		ops := strings.TrimSpace(f.Operands)
		if ops == "" || seen[ops] {
			continue
		}
		seen[ops] = true
		distinct = append(distinct, ops)
	}
	if len(distinct) == 0 {
		return ""
	}
	n := min(len(distinct), maxForms)
	parts := make([]string, 0, n+1)
	for _, ops := range distinct[:n] {
		parts = append(parts, code(inst.Name+" "+ops))
	}
	if more := len(distinct) - n; more > 0 {
		parts = append(parts, fmt.Sprintf("(+%d more)", more))
	}
	return strings.Join(parts, " ")
}

// levelRank orders forms by level with Level 0 (outside the scheme: AES,
// VMX ...) last, because it says nothing about how widely the form runs.
func levelRank(l isa.Level) int {
	if l == 0 {
		return 5
	}
	return int(l)
}

// widthRegs are the fixed registers forms name directly (in/out, string and
// accumulator encodings), by width.
var widthRegs = map[string]int{
	"al": 8, "ax": 16, "eax": 32, "rax": 64,
}

// formWidth reports whether an operand of the form is a register or memory
// operand of the given width (match), and whether it has any sized operand at
// all (sized). Immediates do not count (mov imm32, r/m64 is a q form), and
// %dx is skipped because in/out use it as the port whatever the data width.
func formWidth(ops string, bits int) (match, sized bool) {
	for _, op := range strings.Split(ops, ",") {
		op = strings.TrimPrefix(strings.TrimSpace(op), "%")
		if op == "" || op == "dx" || strings.HasPrefix(op, "imm") || strings.HasPrefix(op, "$") {
			continue
		}
		if w, ok := widthRegs[op]; ok {
			sized = true
			if w == bits {
				return true, true
			}
			continue
		}
		for _, p := range []string{"r/m", "r", "m"} {
			if rest, ok := strings.CutPrefix(op, p); ok {
				if w, err := strconv.Atoi(rest); err == nil {
					sized = true
					if w == bits {
						return true, true
					}
				}
			}
		}
	}
	return false, sized
}

func metaLine(inst *isa.Instruction) string {
	var parts []string
	if inst.Category != "" {
		parts = append(parts, escape(inst.Category))
	}
	if inst.CPL == 0 {
		parts = append(parts, "CPL 0")
	}
	if m := modesText(inst.Modes); m != "" {
		parts = append(parts, m)
	}
	if l := levelText(inst.Forms); l != "" {
		parts = append(parts, l)
	}
	return strings.Join(parts, " - ")
}

// modesText turns the Modes field ("16,32", "64", "") into words. Only
// restrictions are worth a line; "" means valid everywhere.
func modesText(modes string) string {
	m := strings.ReplaceAll(modes, " ", "")
	switch m {
	case "", "16,32,64":
		return ""
	case "64":
		return "64-bit only"
	case "16,32":
		return "not in 64-bit mode"
	}
	return escape("modes " + m)
}

// levelText reports the lowest x86-64 level any form needs: if one form is
// baseline, the mnemonic is usable at v1 and the newer forms are refinements,
// so warning about the highest would cry wolf. The ISA set named is the one
// of that lowest form. Forms outside the level scheme (AES, VMX, RDTSCP ...)
// only matter when no form is inside it; then the set name is all there is
// to report.
func levelText(forms []isa.Form) string {
	var best *isa.Form
	var outside string
	for i := range forms {
		f := &forms[i]
		if f.Level == 0 {
			if outside == "" {
				outside = f.ISASet
			}
			continue
		}
		if best == nil || f.Level < best.Level {
			best = f
		}
	}
	switch {
	case best != nil && best.Level > 1:
		return fmt.Sprintf("x86-64-v%d (%s)", best.Level, escape(best.ISASet))
	case best == nil && outside != "":
		return "ISA " + escape(outside)
	}
	return ""
}

func flagsLine(inst *isa.Instruction) string {
	var parts []string
	add := func(verb, flags string) {
		if f := strings.Join(strings.Fields(flags), " "); f != "" {
			parts = append(parts, verb+" "+escape(f))
		}
	}
	add("writes", inst.FlagsWritten)
	add("reads", inst.FlagsRead)
	add("undefined", inst.FlagsUndefined)
	if len(parts) == 0 {
		return "Flags: none"
	}
	return "Flags: " + strings.Join(parts, "; ")
}

// prefixInfo describes the prefixes GAS accepts as separate words. The isa
// package only knows that they are prefixes; what each does is hover's
// concern.
var prefixInfo = map[string]string{
	"rep":      "Repeat the string instruction %ecx times (F3)",
	"repe":     "Repeat the string compare while equal and %ecx != 0 (F3)",
	"repz":     "Repeat the string compare while ZF=1 and %ecx != 0 (F3)",
	"repne":    "Repeat the string compare while not equal and %ecx != 0 (F2)",
	"repnz":    "Repeat the string compare while ZF=0 and %ecx != 0 (F2)",
	"lock":     "Make the read-modify-write memory access atomic (F0)",
	"data16":   "Operand-size override to 16 bits (66)",
	"data32":   "Operand-size override to 32 bits (66 in 16-bit code)",
	"addr16":   "Address-size override to 16 bits (67)",
	"addr32":   "Address-size override to 32 bits (67)",
	"rex":      "Emit an empty REX prefix (64-bit mode only)",
	"rex64":    "Emit REX.W: 64-bit operand size (64-bit mode only)",
	"notrack":  "CET: indirect branch not tracked by IBT (3E)",
	"xacquire": "TSX HLE: start a lock-elided region (F2)",
	"xrelease": "TSX HLE: end a lock-elided region (F3)",
	"bnd":      "MPX: branch keeps the bounds registers (F2)",
	"cs":       "Segment override: use %cs for the memory operand (2E)",
	"ds":       "Segment override: use %ds for the memory operand (3E)",
	"es":       "Segment override: use %es for the memory operand (26)",
	"fs":       "Segment override: use %fs for the memory operand (64)",
	"gs":       "Segment override: use %gs for the memory operand (65)",
	"ss":       "Segment override: use %ss for the memory operand (36)",
	"{vex}":    "Pseudo prefix: encode with a VEX prefix",
	"{vex2}":   "Pseudo prefix: encode with a 2-byte VEX prefix",
	"{vex3}":   "Pseudo prefix: encode with a 3-byte VEX prefix",
	"{evex}":   "Pseudo prefix: encode with an EVEX prefix",
	"{rex}":    "Pseudo prefix: emit a REX prefix even if not needed",
	"{rex2}":   "Pseudo prefix: encode with a REX2 prefix (APX)",
	"{load}":   "Pseudo prefix: pick the load form of a reg-reg move",
	"{store}":  "Pseudo prefix: pick the store form of a reg-reg move",
	"{disp8}":  "Pseudo prefix: prefer an 8-bit displacement",
	"{disp16}": "Pseudo prefix: prefer a 16-bit displacement",
	"{disp32}": "Pseudo prefix: prefer a 32-bit displacement",

	"{nooptimize}": "Pseudo prefix: do not optimize this instruction's encoding",
}

func prefixHover(word string) string {
	w := strings.ToLower(word)
	desc, ok := prefixInfo[w]
	if !ok {
		if !isa.IsPrefix(w) {
			return ""
		}
		// rex.w, rex.wrxb ...: the bits are spelled out in the name.
		desc = "Explicit REX prefix with the named W/R/X/B bits set (64-bit mode only)"
	}
	return bold(word) + " - prefix - " + escape(desc)
}

func registerHover(text string) string {
	reg, ok := isa.LookupRegister(text)
	if !ok {
		return ""
	}
	title := bold(text)
	if reg.Summary != "" {
		title += " - " + escape(reg.Summary)
	}
	info := fmt.Sprintf("%s, %d-bit", escape(reg.Class), reg.Width)
	if reg.Modes == "64" {
		info += ", 64-bit mode only"
	} else if reg.Modes != "" {
		info += ", " + escape("modes "+reg.Modes)
	}
	lines := []string{title, info}
	if reg.Bits != "" {
		lines = append(lines, "Bits: "+code(reg.Bits))
	}
	return join(lines)
}

func directiveHover(text string) string {
	d, ok := isa.LookupDirective(text)
	if !ok {
		return ""
	}
	title := bold(text)
	if d.Summary != "" {
		title += " - " + escape(d.Summary)
	}
	lines := []string{title}
	if d.Syntax != "" {
		lines = append(lines, code(d.Syntax))
	}
	return join(lines)
}

func numberHover(text string, v uint64) string {
	return join([]string{bold(text), formatNumber(v)})
}

// formatNumber shows a value the three ways system code reads it. Binary is
// left out above 32 bits, where a 64-digit string is no longer readable at a
// glance and the hex says the same thing.
func formatNumber(v uint64) string {
	s := fmt.Sprintf("hex `0x%X` - dec `%d`", v, v)
	if v <= 0xFFFFFFFF {
		s += fmt.Sprintf(" - bin `0b%b`", v)
	}
	return s
}

// localLabelHover resolves 1f/1b with symbols.ResolveLocal, the same rule
// go-to-definition uses, so hover and navigation never disagree.
func localLabelHover(doc *docstore.Document, toks []lexer.Token, idx int, lineNo func(int) int) string {
	ref := toks[idx].Text
	name, dir := ref[:len(ref)-1], ref[len(ref)-1]
	way := "forward"
	if dir == 'b' || dir == 'B' {
		way = "backward"
	}
	target, ok := symbols.ResolveLocal(toks, idx)
	if !ok {
		return join([]string{bold(ref), fmt.Sprintf("Local label %s, %s: no definition found", name, way)})
	}
	line := toks[target].Line
	lines := []string{
		bold(ref),
		fmt.Sprintf("Local label %s, %s -> line %d", name, way, lineNo(line)+1),
	}
	if text, ok := doc.Line(line); ok && strings.TrimSpace(text) != "" {
		lines = append(lines, code(strings.TrimSpace(text)))
	}
	return join(lines)
}

// labelHover reports where a symbol is defined in this document and whether
// it is exported. Cross-file resolution is phase 2, so a symbol defined
// elsewhere (an .extern, a cpp constant) has no hover rather than a guess.
func labelHover(doc *docstore.Document, toks []lexer.Token, name string, lineNo func(int) int) string {
	def := -1
	global := false
	for i, t := range toks {
		if def < 0 && t.Kind == lexer.LabelDef && t.Text == name {
			def = i
		}
		if t.Kind == lexer.Directive && i+1 < len(toks) && toks[i+1].Line == t.Line &&
			toks[i+1].Text == name && isGlobalDirective(t.Text) {
			global = true
		}
	}
	if def < 0 {
		return ""
	}
	line := toks[def].Line
	lines := []string{bold(name) + " - label", fmt.Sprintf("Label defined at line %d", lineNo(line)+1)}
	if text, ok := doc.Line(line); ok && strings.TrimSpace(text) != name+":" {
		lines = append(lines, code(strings.TrimSpace(text)))
	}
	if global {
		lines = append(lines, "Exported (.globl)")
	}
	return join(lines)
}

func isGlobalDirective(d string) bool {
	d = strings.ToLower(d)
	return d == ".globl" || d == ".global"
}

// macroHover covers a mnemonic-position word that is not an instruction:
// usually a .macro defined in the same file, whose definition line is the
// most useful thing to show.
func macroHover(doc *docstore.Document, toks []lexer.Token, name string, lineNo func(int) int) string {
	for i, t := range toks {
		if t.Kind != lexer.Directive || !strings.EqualFold(t.Text, ".macro") || i+1 >= len(toks) {
			continue
		}
		n := toks[i+1]
		if n.Line != t.Line || n.Text != name {
			continue
		}
		lines := []string{bold(name) + " - macro", fmt.Sprintf("Macro defined at line %d", lineNo(t.Line)+1)}
		if text, ok := doc.Line(t.Line); ok {
			lines = append(lines, code(strings.TrimSpace(text)))
		}
		return join(lines)
	}
	return ""
}

// isSymbol reports whether an immediate's atom is a symbol name, so $label
// can show the label's definition.
func isSymbol(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c == '.' || c == '$' || (c >= '0' && c <= '9') || (c|0x20) >= 'a' && (c|0x20) <= 'z') {
			return false
		}
	}
	return true
}

// join separates lines with a blank line, because a single newline in
// markdown is a soft break and clients would run the lines together.
func join(lines []string) string {
	return strings.Join(lines, "\n\n")
}
