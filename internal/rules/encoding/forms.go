package encoding

import (
	"strconv"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// The form matcher answers one question: does some form of the instruction
// accept the operands as written? The isa tables describe forms as AT&T
// pattern strings ("imm8, r/m8", "xmm/m128/m32bcst, xmm, xmm{k}{z}"); they
// are compiled once per instruction into slots of alternatives and cached,
// so matching a statement is a few comparisons per operand.
//
// Matching can be relaxed along three axes (general register widths and the
// suffix, vector register widths, AVX-512 decorations). A statement that
// matches only once an axis is relaxed has a more specific problem than "no
// form", and the rule for that axis reports it instead.

// altKind is what one alternative of a slot accepts.
type altKind uint8

const (
	akGPR   altKind = iota // general register of width
	akMem                  // memory; width 0 = any size
	akImm                  // immediate, any value (values are imm-range's business)
	akFixed                // one register by name (al, cl, dx, es ...)
	akSreg                 // any segment register
	akCR                   // control register
	akDR                   // debug register
	akVec                  // xmm/ymm/zmm of width
	akMM                   // MMX register
	akK                    // AVX-512 mask register
	akTMM                  // AMX tile register
	akBND                  // MPX bound register
	akSTi                  // any x87 stack register
	akVSIB                 // memory with a vector index of width
)

// alt is one alternative of a slot ("r/m32" is two: r32 and m32).
type alt struct {
	kind  altKind
	width int16 // register or memory width in bits; vector index width for akVSIB
	// sized marks a memory width the AT&T suffix governs (r/m32, m16 in
	// an integer instruction); special memory (m16&32, m16:32, m80fp,
	// m512byte) and vector memory (xmm/m128) are not.
	sized bool
	bcst  int16 // element bits of an mNbcst alternative; 0 otherwise
	plain bool  // an mN written alone as the whole slot
	// bcstN is the {1toN} count an mNbcst alternative takes: the vector
	// width of its slot over the element size; 0 when the slot names no
	// vector register to tell.
	bcstN int16
	name  string // akFixed: canonical register name
	// opSize marks an alternative whose width is the instruction's
	// operand size, the one a suffix names. Fixed %cl (a count) and %dx
	// (a port) are not.
	opSize bool
}

// slot is one operand position of a form.
type slot struct {
	alts    []alt
	mask    bool // {k}: an opmask decoration is allowed
	zero    bool // {z}: zeroing is allowed
	maskReq bool // the EVEX gather/scatter form needs a mask
}

// form is one compiled operand pattern.
type form struct {
	slots []slot
	text  string // the pattern as the tables write it, for messages
}

// compiled is every form of one instruction.
type compiled struct {
	forms []form
	// unknown counts forms with a pattern the compiler does not
	// understand. When no known form matches and some form is unknown,
	// the statement is given the benefit of the doubt.
	unknown int
	// intOps marks an integer instruction (some form has a general
	// register slot), where the suffix governs plain mN memory too.
	intOps bool
	// noOps marks an instruction whose every form takes no operands.
	noOps bool
}

// formKey selects a cache entry: the instruction, and whether the APX
// forms of 64-bit code are included.
type formKey struct {
	inst *isa.Instruction
	apx  bool
}

var formCache sync.Map // formKey -> *compiled

// formsOf returns the compiled forms of inst, compiling them on first use.
// apx adds the forms GAS accepts in 64-bit code for Intel APX (see
// withAPX).
func formsOf(inst *isa.Instruction, apx bool) *compiled {
	k := formKey{inst, apx}
	if v, ok := formCache.Load(k); ok {
		return v.(*compiled)
	}
	c := compile(inst)
	if apx {
		c = withAPX(inst.Name, c)
	}
	v, _ := formCache.LoadOrStore(k, c)
	return v.(*compiled)
}

// APX (Advanced Performance Extensions) gives the legacy integer
// instructions a "new data destination" in 64-bit code: one more register
// operand that receives the result, as wide as the r/m operand
// (add %ebx, %ecx, %edx). GAS accepts these forms in any 64-bit code, so
// the matcher must too, or every NDD statement would be reported.
var (
	// apxNDD extends the forms with this many operands by one register.
	apxNDD = map[string]int{
		"add": 2, "adc": 2, "and": 2, "or": 2, "sub": 2, "sbb": 2, "xor": 2,
		"adcx": 2, "adox": 2, "imul": 2,
		"shl": 2, "sal": 2, "shr": 2, "sar": 2, "rol": 2, "ror": 2, "rcl": 2, "rcr": 2,
		"shld": 3, "shrd": 3,
		"inc": 1, "dec": 1, "neg": 1, "not": 1,
	}
	// apxExtra are further APX-era forms: SETcc writing a whole register
	// (setzu), register-to-register movbe, and the movsx spellings GAS
	// maps to movsxd.
	apxExtra = map[string][]string{
		"movbe":  {"r16, r16", "r32, r32", "r64, r64"},
		"movsx":  {"r/m32, r32", "r/m32, r64"},
		"movsxd": {"r/m32, r16"},
	}
)

func withAPX(name string, base *compiled) *compiled {
	c := *base
	c.forms = append([]form(nil), base.forms...)
	if n, ok := apxNDD[name]; ok || strings.HasPrefix(name, "cmov") {
		if !ok {
			n = 2
		}
		for _, f := range base.forms {
			if len(f.slots) != n {
				continue
			}
			for _, a := range f.slots[n-1].alts {
				if a.kind != akGPR {
					continue
				}
				slots := append(append([]slot(nil), f.slots...), slot{alts: []alt{{kind: akGPR, width: a.width, opSize: true}}})
				c.forms = append(c.forms, form{slots: slots, text: f.text + ", r" + strconv.Itoa(int(a.width))})
			}
		}
	}
	extra := apxExtra[name]
	if strings.HasPrefix(name, "set") && len(base.forms) > 0 && base.forms[0].text == "r/m8" {
		extra = []string{"r16", "r32", "r64"}
	}
	for _, p := range extra {
		if f, ok := compileForm(p, false); ok {
			c.forms = append(c.forms, f)
		}
	}
	return &c
}

func compile(inst *isa.Instruction) *compiled {
	c := &compiled{noOps: true}
	pats := make([]string, 0, len(inst.Forms)+len(extraForms[inst.Name]))
	for _, f := range inst.Forms {
		pats = append(pats, f.Operands)
	}
	pats = append(pats, extraForms[inst.Name]...)
	seen := make(map[string]bool, len(pats))
	gather := strings.HasPrefix(inst.Category, "GATHER") || strings.HasPrefix(inst.Category, "SCATTER")
	for _, p := range pats {
		if seen[p] {
			continue
		}
		seen[p] = true
		if strings.TrimSpace(p) != "" {
			c.noOps = false
		}
		f, ok := compileForm(p, gather)
		if !ok {
			c.unknown++
			continue
		}
		c.forms = append(c.forms, f)
	}
	for _, f := range c.forms {
		for _, s := range f.slots {
			for _, a := range s.alts {
				if a.kind == akGPR {
					c.intOps = true
				}
			}
		}
	}
	if c.intOps {
		// In an integer instruction a plain mN is an operand-size
		// memory operand (movbe m32, cmpxchg8b m64), so the suffix
		// governs it like the memory half of r/mN.
		for fi := range c.forms {
			for si := range c.forms[fi].slots {
				for ai := range c.forms[fi].slots[si].alts {
					a := &c.forms[fi].slots[si].alts[ai]
					if a.kind == akMem && a.plainSized() {
						a.sized, a.opSize = true, true
					}
				}
			}
		}
	}
	return c
}

// plainSized reports an mN alternative written on its own as a whole slot,
// which compile marks as suffix-governed in integer instructions. Vector
// memory (the m128 of xmm/m128) is never plain.
func (a *alt) plainSized() bool { return a.plain && a.width > 0 && a.bcst == 0 }

// compileForm compiles one pattern string; ok is false when some part of
// it is not understood.
func compileForm(p string, gather bool) (form, bool) {
	f := form{text: p}
	p = strings.TrimSpace(p)
	if p == "" {
		return f, true
	}
	for _, tok := range strings.Split(p, ",") {
		s, ok := compileSlot(strings.TrimSpace(tok), gather)
		if !ok {
			return f, false
		}
		f.slots = append(f.slots, s)
	}
	return f, true
}

func compileSlot(tok string, gather bool) (slot, bool) {
	var s slot
	if strings.HasPrefix(tok, "*") || strings.HasPrefix(tok, "$") {
		// Indirect branch targets and far pointers: branches are not
		// matched at all, so these are simply not understood.
		return s, false
	}
	switch {
	case strings.HasSuffix(tok, "{k}{z}"):
		s.mask, s.zero = true, true
		tok = strings.TrimSuffix(tok, "{k}{z}")
	case strings.HasSuffix(tok, "{k}"):
		s.mask = true
		tok = strings.TrimSuffix(tok, "{k}")
		// A gather or scatter's EVEX form has a mask on its vector
		// operand that is the completion mask, not a write mask; it is
		// required. Other {k} slots (compares into k{k}) make it
		// optional.
		s.maskReq = gather
	}
	if strings.ContainsAny(tok, "(){}") && tok != "st(i)" && tok != "st(0)" {
		return s, false // string operands such as "(%si)": not matched
	}
	tok = strings.TrimPrefix(tok, "%")
	if rest, ok := strings.CutPrefix(tok, "r/m"); ok {
		w, err := strconv.Atoi(rest)
		if err != nil || !gprWidth(w) {
			return s, false
		}
		s.alts = append(s.alts,
			alt{kind: akGPR, width: int16(w), opSize: true},
			alt{kind: akMem, width: int16(w), sized: true, opSize: true})
		return s, true
	}
	parts := strings.Split(tok, "/")
	hasGPR := false
	for _, part := range parts {
		a, ok := compileAlt(part)
		if !ok {
			return s, false
		}
		if a.kind == akGPR {
			hasGPR = true
		}
		s.alts = append(s.alts, a)
	}
	var vec int16
	for _, a := range s.alts {
		if a.kind == akVec {
			vec = a.width
		}
	}
	for i := range s.alts {
		if a := &s.alts[i]; a.bcst != 0 && vec != 0 {
			a.bcstN = vec / a.bcst
		}
	}
	if len(s.alts) == 1 && s.alts[0].kind == akMem {
		s.alts[0].plain = true
	}
	if hasGPR {
		// r32/m16, r64/m16: the memory half is an integer operand.
		for i := range s.alts {
			if s.alts[i].kind == akMem && s.alts[i].width > 0 && s.alts[i].bcst == 0 {
				s.alts[i].sized, s.alts[i].opSize = true, true
			}
		}
	}
	return s, true
}

func gprWidth(w int) bool { return w == 8 || w == 16 || w == 32 || w == 64 }

// vecClass maps the vector register class names to their widths.
var vecClass = map[string]int16{"xmm": 128, "ymm": 256, "zmm": 512}

func compileAlt(p string) (alt, bool) {
	switch p {
	case "r8", "r16", "r32", "r64":
		w, _ := strconv.Atoi(p[1:])
		return alt{kind: akGPR, width: int16(w), opSize: true}, true
	case "xmm", "ymm", "zmm":
		return alt{kind: akVec, width: vecClass[p]}, true
	case "mm":
		return alt{kind: akMM}, true
	case "k":
		return alt{kind: akK}, true
	case "tmm":
		return alt{kind: akTMM}, true
	case "bnd":
		return alt{kind: akBND}, true
	case "sreg":
		return alt{kind: akSreg}, true
	case "cr":
		return alt{kind: akCR}, true
	case "dr":
		return alt{kind: akDR}, true
	case "st(i)":
		return alt{kind: akSTi}, true
	case "st(0)", "st":
		return alt{kind: akFixed, name: "st"}, true
	case "1":
		return alt{kind: akImm, width: 8}, true
	case "m", "mem":
		return alt{kind: akMem}, true
	case "al", "ax", "eax", "rax":
		r, _ := isa.LookupRegister(p)
		return alt{kind: akFixed, name: p, width: int16(r.Width), opSize: true}, true
	case "xmm0":
		// The implicit mask of blendvps and friends, written out.
		return alt{kind: akFixed, name: p, width: 128}, true
	case "cl", "dx", "ecx", "edx", "rcx", "rdx", "es", "cs", "ss", "ds", "fs", "gs":
		// Fixed counts, ports and implicit operands: not the operand
		// size (shll %cl, outb %al, %dx).
		r, _ := isa.LookupRegister(p)
		return alt{kind: akFixed, name: p, width: int16(r.Width)}, true
	}
	if rest, ok := strings.CutPrefix(p, "imm"); ok {
		if w, err := strconv.Atoi(rest); err == nil && gprWidth(w) {
			return alt{kind: akImm, width: int16(w)}, true
		}
		return alt{}, false
	}
	if rest, ok := strings.CutPrefix(p, "vm"); ok && len(rest) == 3 {
		w, ok := map[byte]int16{'x': 128, 'y': 256, 'z': 512}[rest[2]]
		if !ok || (rest[:2] != "32" && rest[:2] != "64") {
			return alt{}, false
		}
		return alt{kind: akVSIB, width: w}, true
	}
	if rest, ok := strings.CutPrefix(p, "m"); ok {
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n == 0 {
			return alt{}, false
		}
		w, _ := strconv.Atoi(rest[:n])
		switch tail := rest[n:]; {
		case tail == "":
			return alt{kind: akMem, width: int16(w)}, true
		case tail == "bcst":
			return alt{kind: akMem, bcst: int16(w)}, true
		case tail == "fp", tail == "int", tail == "bcd", tail == "byte",
			strings.HasPrefix(tail, "&"), strings.HasPrefix(tail, ":"):
			// Special memory: a descriptor-table pointer, a far
			// pointer, an x87 format. Any memory operand, and the
			// suffix does not size it (lgdtl and lgdtw both exist).
			return alt{kind: akMem}, true
		}
	}
	return alt{}, false
}

// extraForms are operand shapes GAS accepts that the tables do not list:
// the one-operand shift by one, imul's two-operand immediate form, the
// implicit accumulator of in and out, the reversed operand orders of the
// commutative test and xchg, mov between a 32/64-bit register and a
// segment register, and movd with a 64-bit register (GAS assembles it as
// movq).
var extraForms = map[string][]string{
	"shl": shiftOne, "sal": shiftOne, "shr": shiftOne, "sar": shiftOne,
	"rol": shiftOne, "ror": shiftOne, "rcl": shiftOne, "rcr": shiftOne,
	"imul": {"imm8, r16", "imm16, r16", "imm8, r32", "imm32, r32", "imm8, r64", "imm32, r64"},
	"in":   {"imm8", "dx", "imm8, al", "imm8, ax", "imm8, eax", "dx, al", "dx, ax", "dx, eax"},
	"out":  {"imm8", "dx", "al, imm8", "ax, imm8", "eax, imm8", "al, dx", "ax, dx", "eax, dx"},
	"test": {"r/m8, r8", "r/m16, r16", "r/m32, r32", "r/m64, r64"},
	"xchg": {"r/m8, r8", "r/m16, r16", "r/m32, r32", "r/m64, r64", "r16, ax", "r32, eax", "r64, rax"},
	"mov":  {"r32, sreg", "r64, sreg"},
	"movd": {"r64, xmm", "xmm, r64", "r64, mm", "mm, r64"},
	// The selector instructions take their selector register at any
	// width GAS can encode (it uses the low 16 bits); lar and lsl want the
	// source as wide as the destination or 16 bits.
	"lar": {"r32, r32", "r64, r64"}, "lsl": {"r32, r32", "r64, r64"},
	"ltr": {"r32", "r64"}, "lldt": {"r32", "r64"}, "verr": {"r32", "r64"}, "verw": {"r32", "r64"},
	"ud0": {"r/m16, r16", "r/m64, r64"}, "ud1": {"r/m16, r16", "r/m64, r64"},
	"lkgs": {"r32", "r64"},
	// The SNP instructions take their implicit address in %rax, or %eax
	// with an address-size prefix.
	"psmash": {"eax"}, "pvalidate": {"ax", "eax"}, "rmpupdate": {"eax, rcx", "eax", "rax"}, "rmpadjust": {"eax"},
	// The SSE4.1 variable blends and sha256rnds2 read %xmm0 implicitly;
	// GAS also accepts it written as the first operand.
	"blendvps": {"xmm0, xmm/m128, xmm"}, "blendvpd": {"xmm0, xmm/m128, xmm"},
	"pblendvb": {"xmm0, xmm/m128, xmm"}, "sha256rnds2": {"xmm0, xmm/m128, xmm"},
	// The mask extracts write a 32-bit result and accept a 64-bit
	// destination register too.
	"movmskps": {"xmm, r64"}, "movmskpd": {"xmm, r64"}, "pmovmskb": {"xmm, r64", "mm, r64"},
	"vmovmskps": {"xmm, r64", "ymm, r64"}, "vmovmskpd": {"xmm, r64", "ymm, r64"}, "vpmovmskb": {"xmm, r64", "ymm, r64"},
}

var shiftOne = []string{"r/m8", "r/m16", "r/m32", "r/m64"}

// relax selects which constraints a match ignores.
type relax uint8

const (
	relaxGPR  relax = 1 << iota // general register widths and the suffix
	relaxVec                    // xmm/ymm/zmm widths
	relaxDeco                   // masks, zeroing and broadcasts
)

// operand is a statement operand reduced to what matching needs; built
// once per statement.
type operand struct {
	op    *asm.Operand
	kind  asm.OpKind
	reg   *isa.Register // OpReg
	vsib  int16         // OpMem with a vector index: its width
	dxMem bool          // "(%dx)", the port operand of in/out
}

// matchCtx is one statement prepared for matching.
type matchCtx struct {
	ops    []operand
	suffix int // operand size the suffix names, 0 when none
}

// match reports whether form f accepts the operands under r. On success
// it also returns, per operand, the alternative that matched.
func (m *matchCtx) match(f *form, r relax, picked []*alt) bool {
	if len(f.slots) != len(m.ops) {
		return false
	}
	sizedSeen, sizedOK := false, false
	for i := range m.ops {
		o := &m.ops[i]
		s := &f.slots[i]
		if !decoOK(o, s, r) {
			return false
		}
		var hit *alt
		for k := range s.alts {
			if m.altOK(o, &s.alts[k], r) {
				hit = &s.alts[k]
				break
			}
		}
		if hit == nil {
			return false
		}
		if picked != nil {
			picked[i] = hit
		}
		if hit.opSize {
			sizedSeen = true
			if w := opWidth(o, hit); w == m.suffix {
				sizedOK = true
			}
		}
	}
	// The suffix must name the width of some operand-size operand of the
	// form ("any" rather than "every", so crc32b %al, %ecx, whose suffix
	// sizes the source, is accepted). A form with no such operand takes
	// any suffix (pushl %ds, enterl).
	if m.suffix != 0 && r&relaxGPR == 0 && sizedSeen && !sizedOK {
		return false
	}
	return true
}

// opWidth is the operand-size width an operand takes in alternative a.
func opWidth(o *operand, a *alt) int {
	if o.kind == asm.OpReg && o.reg != nil {
		return o.reg.Width
	}
	return int(a.width)
}

func decoOK(o *operand, s *slot, r relax) bool {
	if r&relaxDeco != 0 {
		return true
	}
	op := o.op
	if op.Mask != "" && !s.mask {
		return false
	}
	if op.Zeroing && !s.zero {
		return false
	}
	if s.maskReq && op.Mask == "" {
		return false
	}
	return true
}

func (m *matchCtx) altOK(o *operand, a *alt, r relax) bool {
	op := o.op
	switch o.kind {
	case asm.OpReg:
		if op.Broadcast != "" && r&relaxDeco == 0 {
			return false
		}
		return regOK(o.reg, a, r)
	case asm.OpMem:
		switch a.kind {
		case akMem:
			if o.vsib != 0 {
				return false
			}
			if op.Broadcast != "" {
				if r&relaxDeco != 0 {
					return true
				}
				n := bcstCount(op.Broadcast)
				return a.bcst != 0 && n > 0 && (a.bcstN == 0 || int(a.bcstN) == n)
			}
			if a.bcst != 0 {
				return false
			}
			if a.sized && m.suffix != 0 && r&relaxGPR == 0 && int(a.width) != m.suffix {
				return false
			}
			return true
		case akVSIB:
			if o.vsib == 0 || op.Broadcast != "" {
				return false
			}
			return o.vsib == a.width || r&relaxVec != 0
		case akFixed:
			return o.dxMem && a.name == "dx"
		}
		return false
	case asm.OpImm:
		return a.kind == akImm && op.Broadcast == ""
	}
	return false
}

func regOK(reg *isa.Register, a *alt, r relax) bool {
	switch reg.Class {
	case "gpr":
		switch a.kind {
		case akGPR:
			return int(a.width) == reg.Width || r&relaxGPR != 0
		case akFixed:
			if a.name == reg.Name {
				return true
			}
			return r&relaxGPR != 0 && family(a.name) != "" && family(a.name) == family(reg.Name)
		}
	case "segment":
		return a.kind == akSreg || (a.kind == akFixed && a.name == reg.Name)
	case "control":
		return a.kind == akCR
	case "debug":
		return a.kind == akDR
	case "xmm", "ymm", "zmm":
		if a.kind == akFixed {
			return a.name == reg.Name
		}
		return a.kind == akVec && (int(a.width) == reg.Width || r&relaxVec != 0)
	case "mmx":
		return a.kind == akMM
	case "mask":
		return a.kind == akK
	case "x87":
		return a.kind == akSTi || (a.kind == akFixed && a.name == "st" && (reg.Name == "st" || reg.Name == "st(0)"))
	}
	if strings.HasPrefix(reg.Name, "tmm") {
		return a.kind == akTMM
	}
	if strings.HasPrefix(reg.Name, "bnd") {
		return a.kind == akBND
	}
	return false
}

// bcstCount is N of a {1toN} decoration, 0 when it does not parse.
func bcstCount(b string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(b, "1to"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// matches returns the forms of c that accept the statement under r.
func (m *matchCtx) matches(c *compiled, r relax) []*form {
	var out []*form
	for i := range c.forms {
		if m.match(&c.forms[i], r, nil) {
			out = append(out, &c.forms[i])
		}
	}
	return out
}

// any reports whether some form of c accepts the statement under r.
func (m *matchCtx) any(c *compiled, r relax) bool {
	for i := range c.forms {
		if m.match(&c.forms[i], r, nil) {
			return true
		}
	}
	return false
}
