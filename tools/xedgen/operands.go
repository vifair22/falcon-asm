package main

import (
	"regexp"
	"strconv"
	"strings"
)

// xedOperand is one parsed token from an OPERANDS line, e.g.
// "REG0=GPRv_R():w:v" or "MEM0:r:vv:f32:TXT=BCASTSTR".
type xedOperand struct {
	Name   string // REG0, MEM0, IMM0, RELBR, ...
	Value  string // the part after '=', e.g. "GPRv_R()" or "XED_REG_AL"
	Action string // r, w, rw, cw, crw ...
	Width  string // operand width code, e.g. "v", "b", "zd"
	Elem   string // element type, e.g. "f32", "u64"
	Vis    string // EXPL, IMPL or SUPP
	Txt    string // TXT= decoration hint: ZEROSTR, BCASTSTR, ROUNDC, SAESTR
}

var actionRE = regexp.MustCompile(`^[rwc]+$`)

// parseOperand splits one operand token. Fields after the action are
// positional in XED's grammar only loosely (width and visibility may come in
// either order), so each is classified by what it looks like: visibility
// keywords, TXT= hints and known width codes, with anything left over taken
// as the element type.
func parseOperand(tok string, widths map[string]width) xedOperand {
	fields := strings.Split(tok, ":")
	op := xedOperand{}
	head := fields[0]
	if n, v, ok := strings.Cut(head, "="); ok {
		op.Name, op.Value = n, v
	} else {
		op.Name = head
	}
	rest := fields[1:]
	if len(rest) > 0 && actionRE.MatchString(rest[0]) {
		op.Action = rest[0]
		rest = rest[1:]
	}
	for _, f := range rest {
		switch {
		case f == "EXPL" || f == "IMPL" || f == "SUPP" || f == "ECOND":
			op.Vis = f
		case strings.HasPrefix(f, "TXT="):
			op.Txt = strings.TrimPrefix(f, "TXT=")
		case op.Width == "" && isWidth(f, widths):
			op.Width = f
		case op.Elem == "":
			op.Elem = f
		}
	}
	return op
}

func isWidth(f string, widths map[string]width) bool {
	_, ok := widths[f]
	return ok
}

// opClass is the coarse role of a rendered operand, used when merging a
// register form and a memory form into Intel's "r/m" notation.
type opClass int

const (
	clsOther opClass = iota
	clsReg
	clsMem
	clsBcst
	clsFused // already a "reg/mem" alternative; never fused again with a register
)

// rendered is one visible operand in Intel order. Deco carries an AVX-512
// write-mask decoration ("{k}" or "{k}{z}") that belongs to the operand
// rather than being an operand of its own.
type rendered struct {
	Text  string
	Class opClass
	Deco  string
}

// renderCtx holds the per-record facts operand rendering depends on.
type renderCtx struct {
	rec    *xedRecord
	widths map[string]width
	osz    int // effective operand size: 16, 32 or 64
}

// renderRecord renders the visible operands of rec for effective operand
// size osz, in Intel (destination first) order. ok is false for records the
// generator deliberately does not list: AVX-512 embedded-rounding and SAE
// variants, which are the same instruction as the plain register form with
// a decoration GAS spells as an extra "{rn-sae}" style operand.
func renderRecord(rec *xedRecord, widths map[string]width, osz int) (ops []rendered, ok bool) {
	ctx := renderCtx{rec: rec, widths: widths, osz: osz}
	// A register-form EVEX record with BCRC=1 is embedded rounding or SAE
	// even when no operand carries the TXT hint.
	if (rec.hasToken("MOD=3") || rec.hasToken("MOD[0b11]")) && rec.hasToken("BCRC=1") {
		return nil, false
	}
	parsed := make([]xedOperand, 0, len(rec.Operands))
	hasPtr, hasMem := false, false
	for _, tok := range rec.Operands {
		op := parseOperand(tok, widths)
		if op.Txt == "ROUNDC" || op.Txt == "SAESTR" {
			return nil, false
		}
		hasPtr = hasPtr || op.Name == "PTR"
		hasMem = hasMem || (strings.HasPrefix(op.Name, "MEM") && op.Vis != "SUPP")
		parsed = append(parsed, op)
	}
	for _, op := range parsed {
		if op.Vis == "SUPP" {
			continue
		}
		// XED splits a far pointer into PTR (offset) and IMM0 (selector);
		// the SDM and GAS treat them as the one ptr16:16/ptr16:32 operand.
		if hasPtr && strings.HasPrefix(op.Name, "IMM") {
			continue
		}
		// x87 memory forms name ST(0) as an implicit operand. The SDM and
		// GAS write just the memory operand ("fadd m32fp").
		if hasMem && op.Vis == "IMPL" && op.Value == "XED_REG_ST0" {
			continue
		}
		r, keep := ctx.render(op)
		if !keep {
			continue
		}
		if r.Deco != "" && r.Text == "" {
			// Write-mask operand: decorate the preceding operand.
			if len(ops) > 0 {
				ops[len(ops)-1].Deco += r.Deco
			}
			continue
		}
		ops = append(ops, r)
	}
	return ops, true
}

func (c renderCtx) render(op xedOperand) (rendered, bool) {
	switch {
	case strings.HasPrefix(op.Name, "REG"):
		return c.renderReg(op)
	case op.Name == "MEM0" || op.Name == "MEM1":
		return c.renderMem(op), true
	case op.Name == "AGEN":
		return rendered{Text: "m", Class: clsMem}, true
	case strings.HasPrefix(op.Name, "IMM"):
		if op.Vis == "IMPL" {
			// Shift-by-one forms: the immediate is a fixed 1.
			return rendered{Text: "1"}, true
		}
		return rendered{Text: "imm" + strconv.Itoa(c.bits(op.Width, 8))}, true
	case op.Name == "RELBR":
		return rendered{Text: "rel" + strconv.Itoa(c.bits(op.Width, 32))}, true
	case op.Name == "ABSBR":
		return rendered{Text: "imm64"}, true
	case op.Name == "PTR":
		if c.bits(op.Width, 32) <= 32 {
			return rendered{Text: "ptr16:16"}, true
		}
		return rendered{Text: "ptr16:32"}, true
	}
	return rendered{}, false
}

// bits resolves a width code for the current operand size, with a default
// for operands that do not name one.
func (c renderCtx) bits(code string, def int) int {
	if w, ok := c.widths[code]; ok {
		if b := w.bitsFor(c.osz); b > 0 {
			return b
		}
	}
	return def
}

func (c renderCtx) gprBits(nt string) int {
	switch {
	case strings.HasPrefix(nt, "GPR8"):
		return 8
	case strings.HasPrefix(nt, "GPR16"):
		return 16
	case strings.HasPrefix(nt, "GPR32"), strings.HasPrefix(nt, "VGPR32"):
		return 32
	case strings.HasPrefix(nt, "GPR64"), strings.HasPrefix(nt, "VGPR64"):
		return 64
	case strings.HasPrefix(nt, "GPRz"):
		return c.bits("z", 32)
	case strings.HasPrefix(nt, "GPRy"), strings.HasPrefix(nt, "VGPRy"):
		return c.bits("y", 32)
	case strings.HasPrefix(nt, "A_GPR"):
		return c.bits("asz", 32)
	}
	return c.bits("v", 32)
}

func (c renderCtx) renderReg(op xedOperand) (rendered, bool) {
	v := op.Value
	if fixed, ok := strings.CutPrefix(v, "XED_REG_"); ok {
		switch {
		case fixed == "INVALID", strings.HasPrefix(fixed, "BSR"):
			return rendered{}, false
		case fixed == "ST0":
			return rendered{Text: "st(0)", Class: clsReg}, true
		}
		return rendered{Text: strings.ToLower(fixed), Class: clsOther}, true
	}
	nt := strings.TrimSuffix(v, "()")
	switch {
	case nt == "MASK1" || nt == "MASKNOT0":
		d := "{k}"
		if op.Txt == "ZEROSTR" {
			d += "{z}"
		}
		return rendered{Deco: d}, true
	case strings.HasPrefix(nt, "MASK"):
		return rendered{Text: "k", Class: clsReg}, true
	case strings.HasPrefix(nt, "XMM"):
		return rendered{Text: "xmm", Class: clsReg}, true
	case strings.HasPrefix(nt, "YMM"):
		return rendered{Text: "ymm", Class: clsReg}, true
	case strings.HasPrefix(nt, "ZMM"):
		return rendered{Text: "zmm", Class: clsReg}, true
	case strings.HasPrefix(nt, "MMX"):
		return rendered{Text: "mm", Class: clsReg}, true
	case strings.HasPrefix(nt, "TMM"):
		return rendered{Text: "tmm", Class: clsReg}, true
	case strings.HasPrefix(nt, "X87"):
		return rendered{Text: "st(i)", Class: clsReg}, true
	case strings.HasPrefix(nt, "BND"):
		return rendered{Text: "bnd", Class: clsReg}, true
	case strings.HasPrefix(nt, "CR_"):
		return rendered{Text: "cr", Class: clsReg}, true
	case strings.HasPrefix(nt, "DR_"):
		return rendered{Text: "dr", Class: clsReg}, true
	case strings.HasPrefix(nt, "SEG"):
		return rendered{Text: "sreg", Class: clsReg}, true
	case nt == "OrAX" || nt == "OrDX" || nt == "OrBX" || nt == "OrCX":
		return rendered{Text: sizedReg(nt[2:], c.bits("v", 32))}, true
	case nt == "OeAX":
		return rendered{Text: sizedReg("AX", c.bits("z", 32))}, true
	case nt == "ArAX":
		return rendered{Text: sizedReg("AX", c.bits("asz", 32))}, true
	case strings.Contains(nt, "GPR"):
		return rendered{Text: "r" + strconv.Itoa(c.gprBits(nt)), Class: clsReg}, true
	}
	return rendered{Text: strings.ToLower(nt), Class: clsOther}, true
}

// sizedReg spells AX/BX/CX/DX at a given width (ax, eax, rax).
func sizedReg(base string, bits int) string {
	b := strings.ToLower(base)
	switch bits {
	case 32:
		return "e" + b
	case 64:
		return "r" + b
	}
	return b
}

// specialMem gives the Intel SDM spelling for memory widths that are not a
// plain power-of-two size. The x87 names (m32fp, m16int, m80bcd) follow the
// SDM so hover text reads like the manual.
var specialMem = map[string]string{
	"s":         "m16&32",
	"s64":       "m16&64",
	"a16":       "m16&16",
	"a32":       "m32&32",
	"mem32real": "m32fp",
	"m64real":   "m64fp",
	"mem80real": "m80fp",
	"mem16int":  "m16int",
	"mem32int":  "m32int",
	"m64int":    "m64int",
	"mem80dec":  "m80bcd",
	"mem16":     "m2byte",
	"mem14":     "m14byte",
	"mem28":     "m28byte",
	"mem94":     "m94byte",
	"mem108":    "m108byte",
	"mfpxenv":   "m512byte",
	"mxsave":    "mem",
	"mprefetch": "m8",
}

var vsibRE = regexp.MustCompile(`(?:VMODRM|VSIB)_([XYZ])MM`)

func (c renderCtx) renderMem(op xedOperand) rendered {
	rec := c.rec
	// Gathers and scatters address memory through a vector index (VSIB);
	// the SDM writes these as vm32x, vm64y and so on.
	for _, t := range rec.Pattern {
		if m := vsibRE.FindStringSubmatch(t); m != nil {
			idx := "32"
			if rec.hasAttr("QWORD_INDICES") {
				idx = "64"
			}
			return rendered{Text: "vm" + idx + strings.ToLower(m[1]), Class: clsMem}
		}
	}
	if rec.hasToken("BCRC=1") {
		return rendered{Text: "m" + strconv.Itoa(esize(rec, op)) + "bcst", Class: clsBcst}
	}
	r := c.renderPlainMem(op)
	if op.Txt == "BCASTSTR" {
		// One record covers both the full-width load and the embedded
		// broadcast (EVEX.b is not constrained by the pattern).
		r.Text += "/m" + strconv.Itoa(esize(rec, op)) + "bcst"
	}
	return r
}

func (c renderCtx) renderPlainMem(op xedOperand) rendered {
	rec := c.rec
	switch op.Width {
	case "p":
		if c.osz == 16 {
			return rendered{Text: "m16:16", Class: clsMem}
		}
		return rendered{Text: "m16:32", Class: clsMem}
	case "p2":
		switch c.osz {
		case 16:
			return rendered{Text: "m16:16", Class: clsMem}
		case 32:
			return rendered{Text: "m16:32", Class: clsMem}
		}
		return rendered{Text: "m16:64", Class: clsMem}
	}
	if s, ok := specialMem[op.Width]; ok {
		return rendered{Text: s, Class: clsMem}
	}
	w, ok := c.widths[op.Width]
	if !ok {
		return rendered{Text: "m", Class: clsMem}
	}
	if w.XType == "struct" {
		return rendered{Text: "m" + strconv.Itoa(w.bitsFor(c.osz)/8) + "byte", Class: clsMem}
	}
	b := w.bitsFor(c.osz)
	if b == 0 {
		b = vectorMemBits(rec, op)
	}
	if b == 0 {
		return rendered{Text: "m", Class: clsMem}
	}
	return rendered{Text: "m" + strconv.Itoa(b), Class: clsMem}
}

// vectorLength is the EVEX/VEX vector length a record is defined for.
func vectorLength(rec *xedRecord) int {
	switch {
	case rec.hasToken("VL512"):
		return 512
	case rec.hasToken("VL256"):
		return 256
	}
	return 128
}

var esizeRE = regexp.MustCompile(`^ESIZE_(\d+)_BITS\(\)$`)
var elemBitsRE = regexp.MustCompile(`(\d+)$`)

// esize is the element size in bits, from the ESIZE_n_BITS() pattern
// nonterminal or failing that the operand's element type (f32, u64, ...).
func esize(rec *xedRecord, op xedOperand) int {
	for _, t := range rec.Pattern {
		if m := esizeRE.FindStringSubmatch(t); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	if m := elemBitsRE.FindStringSubmatch(op.Elem); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 32
}

// vectorMemBits sizes an AVX-512 "vv" memory operand, whose width XED
// derives from the element count (NELEM_*) times the element size.
func vectorMemBits(rec *xedRecord, op xedOperand) int {
	vl := vectorLength(rec)
	es := esize(rec, op)
	for _, t := range rec.Pattern {
		switch t {
		case "NELEM_FULLMEM()", "NELEM_FULL()":
			return vl
		case "NELEM_HALFMEM()", "NELEM_HALF()":
			return vl / 2
		case "NELEM_QUARTERMEM()", "NELEM_QUARTER()":
			return vl / 4
		case "NELEM_EIGHTHMEM()":
			return vl / 8
		case "NELEM_MEM128()", "NELEM_TUPLE1_4X()":
			return 128
		case "NELEM_ONE()", "NELEM_SCALAR()", "NELEM_TUPLE1()", "NELEM_GSCAT()":
			return es
		case "NELEM_TUPLE2()":
			return 2 * es
		case "NELEM_TUPLE4()":
			return 4 * es
		case "NELEM_TUPLE8()":
			return 8 * es
		case "NELEM_MOVDDUP()":
			if vl == 128 {
				return 64
			}
			return vl
		}
	}
	return 0
}

// operandSizes lists the effective operand sizes a record should be
// rendered at. Most records have no size-dependent operand and render once
// (returned as {32}); for the rest the pattern's mode and REX.W/VEX.W
// constraints prune the 16/32/64 set the way the SDM lists separate forms.
func operandSizes(rec *xedRecord, widths map[string]width) []int {
	base, _ := renderRecord(rec, widths, 32)
	varies := false
	for _, osz := range []int{16, 64} {
		o, _ := renderRecord(rec, widths, osz)
		if joinIntel(o) != joinIntel(base) {
			varies = true
		}
	}
	if !varies {
		return []int{32}
	}
	allow := map[int]bool{16: true, 32: true, 64: true}
	if rec.hasToken("not64") || rec.hasToken("mode16") || rec.hasToken("mode32") {
		allow[64] = false
	}
	vex := rec.hasToken("VV1") || rec.hasToken("EVV") || rec.hasToken("XOPV")
	switch {
	case rec.hasToken("rexw_prefix") || rec.hasToken("REXW=1") || (vex && rec.hasToken("W1")):
		allow = map[int]bool{64: true}
	case rec.hasToken("norexw_prefix") || rec.hasToken("REXW=0") || (vex && rec.hasToken("W0")):
		allow[64] = false
	}
	var out []int
	for _, s := range []int{16, 32, 64} {
		if allow[s] {
			out = append(out, s)
		}
	}
	return out
}

// joinIntel renders operands in Intel order for comparison and debugging.
func joinIntel(ops []rendered) string {
	parts := make([]string, len(ops))
	for i, o := range ops {
		parts[i] = o.Text + o.Deco
	}
	return strings.Join(parts, ", ")
}

// attOrder converts Intel-order operands to the AT&T order GAS uses. XED
// flags the few instructions (BOUND, ENTER and friends) whose AT&T syntax
// keeps the Intel order.
func attOrder(rec *xedRecord, ops []rendered) string {
	out := make([]string, len(ops))
	n := len(ops)
	for i, o := range ops {
		j := n - 1 - i
		if rec.hasAttr("ATT_OPERAND_ORDER_EXCEPTION") {
			j = i
		}
		out[j] = o.Text + o.Deco
	}
	return strings.Join(out, ", ")
}

// recordModes returns which of 16/32/64-bit mode the record's encoding is
// valid in, as a bit set (1=16, 2=32, 4=64).
func recordModes(rec *xedRecord) int {
	switch {
	case rec.hasToken("mode64"):
		return 4
	case rec.hasToken("not64"):
		return 1 | 2
	case rec.hasToken("mode16"):
		return 1
	case rec.hasToken("mode32"):
		return 2
	}
	return 7
}
