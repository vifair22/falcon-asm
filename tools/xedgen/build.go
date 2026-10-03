package main

import (
	"fmt"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// Form and Instruction are internal/isa's own types, so the generated file
// is written with exactly the JSON encoding the loader reads (tags, and CPL
// spelled out even when 0) and cannot drift from it.
type (
	Form        = isa.Form
	Instruction = isa.Instruction
)

// iclassRename maps XED iclasses that are XED-internal splits of one
// assembler mnemonic onto that mnemonic. XED separates encodings an
// assembler treats as one instruction (near/far, the SSE vs string MOVSD,
// MOV to control registers, REX.W variants); the hover wants them together.
var iclassRename = map[string]string{
	"CALL_NEAR": "CALL", "CALL_FAR": "CALL",
	"JMP_FAR":  "JMP",
	"RET_NEAR": "RET", "RET_FAR": "RET",
	"MOV_CR": "MOV", "MOV_DR": "MOV",
	"MOVSD_XMM": "MOVSD", "CMPSD_XMM": "CMPSD",
	// The dword string instructions collide with the SSE2 MOVSD/CMPSD in
	// Intel syntax. GAS spells the string forms movsl/cmpsl, so that is the
	// only name that can identify them.
	"MOVSD": "MOVSL", "CMPSD": "CMPSL",
	"PEXTRW_SSE4": "PEXTRW", "VPEXTRW_C5": "VPEXTRW",
	"BEXTR_XOP":          "BEXTR",
	"PREFETCH_EXCLUSIVE": "PREFETCH",
	"SYSCALL_AMD":        "SYSCALL", "SYSRET_AMD": "SYSRET", "SYSRET64": "SYSRET",
	"PCMPESTRI64": "PCMPESTRI", "PCMPESTRM64": "PCMPESTRM", "PCMPISTRI64": "PCMPISTRI",
	"VPCMPESTRI64": "VPCMPESTRI", "VPCMPESTRM64": "VPCMPESTRM", "VPCMPISTRI64": "VPCMPISTRI",
}

// summaryKey redirects the summary lookup for names that are not Intel
// mnemonics.
var summaryKey = map[string]string{"MOVSL": "MOVS", "CMPSL": "CMPS"}

// undocumentedKeep lists iclasses XED marks UNDOCUMENTED that GAS
// nevertheless assembles by name and that real code uses.
var undocumentedKeep = map[string]bool{"FFREEP": true}

// canonicalName returns the upper-case mnemonic a record is filed under, or
// ok=false for records the generator leaves out: LOCK-prefixed and
// REP-prefixed duplicates (the prefix is lexed separately), reserved-NOP
// encodings and undocumented aliases.
func canonicalName(r *xedRecord) (string, bool) {
	ic := r.IClass
	if ic == "" || strings.HasSuffix(ic, "_LOCK") || ic == "PREFETCH_RESERVED" {
		return "", false
	}
	if r.hasAttr("UNDOCUMENTED") && !undocumentedKeep[ic] {
		return "", false
	}
	for _, p := range []string{"REP_", "REPE_", "REPNE_"} {
		if rest, ok := strings.CutPrefix(ic, p); ok {
			// VIA PadLock instructions only exist with REP; for them the
			// prefix is part of the mnemonic GAS accepts.
			if !strings.HasPrefix(r.ISASet, "VIA_") {
				return "", false
			}
			ic = rest
		}
	}
	if n, ok := iclassRename[ic]; ok {
		ic = n
	}
	return ic, true
}

// manualAliases are genuine GAS spellings that are not base+size-suffix of
// the canonical name and that asm-lsp's data does not carry.
var manualAliases = map[string][]string{
	"iretd": {"iretl"}, "pushad": {"pushal"}, "popad": {"popal"},
	"pushfd": {"pushfl"}, "popfd": {"popfl"},
	"lodsd": {"lodsl"}, "stosd": {"stosl"}, "scasd": {"scasl"},
	"insd": {"insl"}, "outsd": {"outsl"},
	"jmp": {"ljmp"}, "call": {"lcall"}, "ret": {"lret"},
	"shl": {"sal"}, "fwait": {"wait"}, "int1": {"icebp"},
	"loope": {"loopz"}, "loopne": {"loopnz"},
	"mov": {"movabs"},
}

// ccAliases are the alternative condition-code spellings, keyed by the one
// XED uses in iclass names.
var ccAliases = map[string][]string{
	"b": {"c", "nae"}, "nb": {"nc", "ae"}, "be": {"na"}, "nbe": {"a"},
	"l": {"nge"}, "nl": {"ge"}, "le": {"ng"}, "nle": {"g"},
	"p": {"pe"}, "np": {"po"}, "z": {"e"}, "nz": {"ne"},
}

// skipGasAliases are Intel mnemonics whose GAS spellings are handled by the
// hand-written system.json (movzbl, movslq, ...) or by manualAliases.
var skipGasAliases = map[string]bool{"MOVSX": true, "MOVZX": true, "MOVSXD": true, "MOV": true}

// pform is a form in the middle of being built: Intel-order operands plus
// what is needed to merge register and memory variants.
type pform struct {
	key   string // encoding identity; only forms with the same key merge
	isa   string
	att   bool // false when AT&T keeps Intel operand order
	ops   []rendered
	dead  bool
	fused bool
}

// encDeny lists pattern-token prefixes that differ between the register and
// memory variants of one encoding (or carry no encoding identity). What is
// left identifies the opcode, map, prefixes, vector length and W bit.
var encDeny = []string{
	"MOD", "RM", "BCRC", "NELEM_", "ESIZE_", "ZEROING", "SIB", "VMODRM",
	"UISA_", "VSIB", "eanot16", "eamode", "NOEVSR", "NOVSR", "EVEXRR",
	"UBIT", "lock", "nolock", "DISP", "BRDISP", "SIMM", "UIMM", "IMM",
	"REG[rrr]", "REG[nnn]",
}

func encKey(r *xedRecord) string {
	var b strings.Builder
	b.WriteString(r.IClass)
	for _, t := range r.Pattern {
		deny := false
		for _, p := range encDeny {
			if strings.HasPrefix(t, p) {
				deny = true
				break
			}
		}
		if !deny {
			b.WriteByte(' ')
			b.WriteString(t)
		}
	}
	return b.String()
}

// buildForms renders every record of one instruction into deduplicated
// AT&T forms. Register and memory variants of the same encoding are fused
// into Intel's notation ("r/m32", "xmm/m128"), and an AVX-512 broadcast
// variant is folded in after that ("zmm/m512/m32bcst").
func buildForms(recs []*xedRecord, widths map[string]width) []Form {
	var pfs []*pform
	for _, r := range recs {
		k := encKey(r)
		for _, osz := range operandSizes(r, widths) {
			ops, ok := renderRecord(r, widths, osz)
			if !ok {
				continue
			}
			pfs = append(pfs, &pform{
				key: fmt.Sprintf("%s|%d", k, osz),
				isa: r.ISASet,
				att: !r.hasAttr("ATT_OPERAND_ORDER_EXCEPTION"),
				ops: ops,
			})
		}
	}
	regMem := func(a, b rendered) bool { return a.Class == clsReg && b.Class == clsMem }
	fuse(pfs, true, regMem)
	// A second, keyless pass picks up register and memory variants that
	// XED writes as separate encodings with different mode or REX.W
	// constraints (MOVZX, MOVSXD). Running it after the keyed pass means
	// instructions with two genuine encodings of the same shape (MOV
	// 88/8A) have already paired off correctly.
	resetFused(pfs)
	fuse(pfs, false, regMem)
	resetFused(pfs)
	fuse(pfs, true, func(a, b rendered) bool {
		return a.Class == clsFused && b.Class == clsBcst
	})

	subsume(pfs)

	var out []Form
	seen := map[string]bool{}
	for _, p := range pfs {
		if p.dead {
			continue
		}
		f := Form{Operands: formatOps(p), ISASet: p.isa, Level: isa.LevelForISASet(p.isa)}
		k := f.Operands + "|" + f.ISASet
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// fuse merges pairs of forms that share an ISA set (and, when keyed, an
// encoding key) and
// differ in exactly one operand position, where match(a, b) accepts the
// two operands at that position (in either order). Each form fuses at most
// once per pass, so a reg,reg form that pairs with a mem form on one side
// does not also swallow the other direction's memory form.
func fuse(pfs []*pform, keyed bool, match func(a, b rendered) bool) {
	for i, a := range pfs {
		if a.dead || a.fused {
			continue
		}
		for _, b := range pfs[i+1:] {
			if b.dead || b.fused || (keyed && a.key != b.key) || a.isa != b.isa || len(a.ops) != len(b.ops) {
				continue
			}
			pos := diffPos(a.ops, b.ops)
			if pos < 0 {
				continue
			}
			x, y := a.ops[pos], b.ops[pos]
			switch {
			case match(x, y):
				a.ops[pos] = fuseOp(x, y)
			case match(y, x):
				a.ops[pos] = fuseOp(y, x)
			default:
				continue
			}
			a.fused = true
			b.dead = true
			break
		}
	}
}

// subsume drops forms that another form of the same ISA set already
// covers, e.g. "imm8, xmm, r32" next to "imm8, xmm, r32/m16". These arise
// when XED defines a second encoding with only the register (or only the
// memory) variant, which then has nothing to fuse with.
func subsume(pfs []*pform) {
	for _, a := range pfs {
		if a.dead {
			continue
		}
		for _, b := range pfs {
			if b == a || b.dead || a.isa != b.isa || len(a.ops) != len(b.ops) {
				continue
			}
			pos := diffPos(a.ops, b.ops)
			if pos < 0 {
				continue
			}
			ca, cb := components(a.ops[pos].Text), components(b.ops[pos].Text)
			if len(ca) < len(cb) && subset(ca, cb) {
				a.dead = true
				break
			}
		}
	}
}

// components splits a fused operand into the alternatives it stands for:
// "r/m8" is {r8, m8}, "zmm/m512/m32bcst" is {zmm, m512, m32bcst}.
func components(t string) map[string]bool {
	parts := strings.Split(t, "/")
	out := map[string]bool{}
	for i, p := range parts {
		if p == "r" && i+1 < len(parts) && strings.HasPrefix(parts[i+1], "m") {
			out["r"+parts[i+1][1:]] = true
			continue
		}
		out[p] = true
	}
	return out
}

func subset(a, b map[string]bool) bool {
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func resetFused(pfs []*pform) {
	for _, p := range pfs {
		p.fused = false
	}
}

// diffPos returns the single index at which two operand lists differ
// (ignoring write-mask decorations), or -1 if they differ in zero or
// several places.
func diffPos(a, b []rendered) int {
	pos := -1
	for i := range a {
		if a[i].Text != b[i].Text {
			if pos >= 0 {
				return -1
			}
			pos = i
		}
	}
	return pos
}

// fuseOp joins a register (or already fused) operand with a memory one.
// The register side's decoration wins: the SDM writes the store form of a
// masked move as "zmm2/m512 {k1}{z}".
func fuseOp(reg, mem rendered) rendered {
	text := reg.Text + "/" + mem.Text
	if strings.HasPrefix(reg.Text, "r") && len(reg.Text) > 1 && reg.Text[1] >= '0' && reg.Text[1] <= '9' &&
		strings.TrimPrefix(reg.Text, "r") == strings.TrimPrefix(mem.Text, "m") {
		text = "r/m" + strings.TrimPrefix(reg.Text, "r")
	}
	deco := reg.Deco
	if deco == "" {
		deco = mem.Deco
	}
	return rendered{Text: text, Class: clsFused, Deco: deco}
}

func formatOps(p *pform) string {
	out := make([]string, len(p.ops))
	n := len(p.ops)
	for i, o := range p.ops {
		j := i
		if p.att {
			j = n - 1 - i
		}
		out[j] = o.Text + o.Deco
	}
	return strings.Join(out, ", ")
}

// flagOrder is the EFLAGS bit order, then the x87 condition codes, so flag
// lists read the way the SDM tables do.
var flagOrder = []string{"CF", "PF", "AF", "ZF", "SF", "TF", "IF", "DF", "OF", "IOPL", "NT", "RF", "VM", "AC", "VIF", "VIP", "ID", "C0", "C1", "C2", "C3"}

// parseFlags reads an XED FLAGS value such as
// "MUST [ of-mod sf-mod cf-tst ]" or, for shifts,
// "IMM1 MUST [ ... ], IMMx MUST [ ... ]", adding each flag to the read,
// written or undefined set. Every bracketed group is taken: the groups
// describe alternative cases of one instruction and the hover summarises
// them all.
func parseFlags(s string, read, written, undef map[string]bool) {
	for {
		open := strings.IndexByte(s, '[')
		if open < 0 {
			return
		}
		closeIdx := strings.IndexByte(s[open:], ']')
		if closeIdx < 0 {
			return
		}
		for _, tok := range strings.Fields(s[open+1 : open+closeIdx]) {
			flag, act, ok := strings.Cut(strings.ToUpper(tok), "-")
			if !ok {
				continue
			}
			if len(flag) == 3 && strings.HasPrefix(flag, "FC") {
				flag = "C" + flag[2:]
			}
			switch act {
			case "TST":
				read[flag] = true
			case "U":
				undef[flag] = true
			default: // MOD, 0, 1, POP, AH
				written[flag] = true
			}
		}
		s = s[open+closeIdx+1:]
	}
}

func joinFlags(set map[string]bool) string {
	var out []string
	for _, f := range flagOrder {
		if set[f] {
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}

func modesString(bits int) string {
	switch bits {
	case 7, 0:
		return ""
	}
	var out []string
	for i, m := range []string{"16", "32", "64"} {
		if bits&(1<<i) != 0 {
			out = append(out, m)
		}
	}
	return strings.Join(out, ",")
}

// buildStats is reported by the generator so fallbacks can be audited.
type buildStats struct {
	Fallback []string // names whose summary came from XED, not asm-lsp
}

// build joins XED records and asm-lsp docs into the sorted instruction list.
func build(db *xedDB, docs *asmDocs) ([]Instruction, buildStats) {
	groups := map[string][]*xedRecord{}
	var order []string
	for _, r := range db.Records {
		name, ok := canonicalName(r)
		if !ok {
			continue
		}
		if _, have := groups[name]; !have {
			order = append(order, name)
		}
		groups[name] = append(groups[name], r)
	}

	var st buildStats
	var out []Instruction
	names := map[string]bool{}
	for _, n := range order {
		names[strings.ToLower(n)] = true
	}
	for _, upper := range order {
		recs := dropAPXPromotions(groups[upper])
		if upper == "NOP" {
			recs = canonicalNOP(recs)
		}
		inst, fellBack := buildInstruction(upper, recs, db.Widths, docs, names)
		if fellBack {
			st.Fallback = append(st.Fallback, inst.Name)
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(st.Fallback)
	return out, st
}

// dropAPXPromotions removes APX (EVEX map 4) re-encodings of legacy
// instructions. They are real, but listing "add r64, r64, r/m64" style NDD
// forms first-class beside every legacy ALU form would bury the forms
// people write; instructions that exist only under APX keep their forms.
func dropAPXPromotions(recs []*xedRecord) []*xedRecord {
	var legacy []*xedRecord
	for _, r := range recs {
		if !strings.HasPrefix(r.ISASet, "APX_") {
			legacy = append(legacy, r)
		}
	}
	if len(legacy) == 0 {
		return recs
	}
	return legacy
}

// canonicalNOP keeps the two documented NOP encodings: 0x90 (which XED
// spells as the bit pattern 0b1001_0 with SRM=0) and the multi-byte
// 0F 1F /0. XED also files the whole reserved-NOP hint space (0F 18..1E,
// reused over time for prefetch, MPX, CET, CLDEMOTE) under NOP; those are
// not encodings anyone writes as nop.
func canonicalNOP(recs []*xedRecord) []*xedRecord {
	var out []*xedRecord
	for _, r := range recs {
		p := r.Pattern
		if (len(p) > 0 && p[0] == "0b1001_0") || (len(p) > 1 && p[0] == "0x0F" && p[1] == "0x1F") {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return recs
	}
	return out
}

func buildInstruction(upper string, recs []*xedRecord, widths map[string]width, docs *asmDocs, names map[string]bool) (Instruction, bool) {
	name := strings.ToLower(upper)
	inst := Instruction{Name: name, CPL: 0}
	primary := recs[0]
	for _, r := range recs {
		if r.IClass == upper {
			primary = r
			break
		}
	}
	inst.Category = primary.Category

	read, written, undef := map[string]bool{}, map[string]bool{}, map[string]bool{}
	modes := 0
	for _, r := range recs {
		if r.CPL == 3 {
			inst.CPL = 3
		}
		modes |= recordModes(r)
		parseFlags(r.Flags, read, written, undef)
	}
	// A flag written in one case and undefined in another (OF after a
	// multi-bit shift) is reported as written; "undefined" is kept for
	// flags the instruction never defines.
	for f := range written {
		delete(undef, f)
	}
	inst.Modes = modesString(modes)
	inst.FlagsRead = joinFlags(read)
	inst.FlagsWritten = joinFlags(written)
	inst.FlagsUndefined = joinFlags(undef)
	inst.Forms = buildForms(recs, widths)
	inst.Aliases = aliasesFor(name, upper, docs, names)

	for _, key := range summaryCandidates(upper) {
		if s, ok := docs.lookup(key); ok {
			inst.Summary = s
			return inst, false
		}
	}
	if s, ok := manualSummary[upper]; ok {
		inst.Summary = s
		return inst, false
	}
	for _, a := range inst.Aliases {
		if s, ok := docs.lookup(strings.ToUpper(a)); ok {
			inst.Summary = s
			return inst, false
		}
	}
	inst.Summary = fallbackSummary(primary)
	return inst, true
}

// stringOps are the string instruction stems; asm-lsp's index files the
// sized forms (SCASQ, LODSQ, ...) under the stem.
var stringOps = []string{"MOVS", "CMPS", "SCAS", "LODS", "STOS", "INS", "OUTS"}

// summaryCandidates lists the asm-lsp keys to try for an instruction, most
// specific first. The extra keys cover places where XED and the SDM index
// file one page under a different name.
func summaryCandidates(upper string) []string {
	out := []string{upper}
	if k, ok := summaryKey[upper]; ok {
		out = []string{k}
	}
	if base, ok := strings.CutSuffix(upper, "64"); ok && base != "" {
		out = append(out, base)
	}
	switch upper {
	case "LOOPE", "LOOPNE":
		out = append(out, "LOOPCC")
	case "JCXZ":
		out = append(out, "JCC")
	case "UD0", "UD1", "UDB":
		out = append(out, "UD")
	}
	if strings.HasPrefix(upper, "FCMOV") {
		out = append(out, "FCMOVCC")
	}
	for _, s := range stringOps {
		if rest, ok := strings.CutPrefix(upper, s); ok && len(rest) == 1 && strings.Contains("BWDQ", rest) {
			out = append(out, s)
		}
	}
	return out
}

// manualSummary covers well-known instructions that neither asm-lsp file
// describes (AMD SVM, FRED, MSR list and a few legacy opcodes). Titles
// follow the vendor manuals. Anything not here and not in asm-lsp gets a
// fallbackSummary and is reported.
var manualSummary = map[string]string{
	"FFREEP":    "Free Floating-Point Register and Pop",
	"SALC":      "Set AL From Carry Flag",
	"GETSEC":    "Safer Mode Extensions Leaf Function Dispatch",
	"CLGI":      "Clear Global Interrupt Flag",
	"STGI":      "Set Global Interrupt Flag",
	"SKINIT":    "Secure Init and Jump with Attestation",
	"VMRUN":     "Run Virtual Machine",
	"VMLOAD":    "Load State from VMCB",
	"VMSAVE":    "Save State to VMCB",
	"VMMCALL":   "Call VMM",
	"INVLPGA":   "Invalidate TLB Entry in a Specified ASID",
	"INVLPGB":   "Broadcast TLB Invalidation",
	"TLBSYNC":   "Synchronize TLB Invalidations",
	"WRMSRNS":   "Non-Serializing Write to Model Specific Register",
	"RDMSRLIST": "Read List of Model Specific Registers",
	"WRMSRLIST": "Write List of Model Specific Registers",
	"LKGS":      "Load Kernel GS Base",
	"ERETS":     "Event Return to Supervisor",
	"ERETU":     "Event Return to User",
	"PUSH2":     "Push Two 64-bit Registers",
	"POP2":      "Pop Two 64-bit Registers",
	"JMPABS":    "Jump Absolute",
}

// fallbackSummary is used when asm-lsp has no entry. Its fixed shape
// ("NAME instruction (ISA_SET)") makes these easy to find and replace.
func fallbackSummary(r *xedRecord) string {
	name, _ := canonicalName(r)
	return fmt.Sprintf("%s instruction (%s)", name, r.ISASet)
}

// aliasesFor collects the GAS spellings of an instruction that Lookup's
// suffix stripping would not find on its own.
func aliasesFor(name, upper string, docs *asmDocs, names map[string]bool) []string {
	var cand []string
	cand = append(cand, manualAliases[name]...)
	for _, fam := range []string{"cmov", "set", "j"} {
		if strings.HasPrefix(name, "fcmov") {
			break
		}
		if cc, ok := strings.CutPrefix(name, fam); ok {
			for _, a := range ccAliases[cc] {
				cand = append(cand, fam+a)
			}
			break
		}
	}
	if !skipGasAliases[upper] {
		for _, g := range docs.GasNames[upper] {
			if isSuffixed(g, name) {
				continue
			}
			cand = append(cand, g)
		}
	}
	var out []string
	for _, a := range cand {
		if a == name || names[a] || contains(out, a) || suffixOfName(a, names) {
			continue
		}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// suffixOfName reports whether a is another instruction's name plus one
// size suffix (pushfl is pushf+l, iretl is iret+l). Lookup already splits
// those, and an alias would stop the semantic-token pass from painting the
// suffix separately.
func suffixOfName(a string, names map[string]bool) bool {
	n := len(a)
	return n > 1 && strings.IndexByte("bwlq", a[n-1]) >= 0 && names[a[:n-1]]
}

// isSuffixed reports whether g is name itself or name plus one AT&T size
// suffix, both of which Lookup already resolves.
func isSuffixed(g, name string) bool {
	if g == name {
		return true
	}
	rest, ok := strings.CutPrefix(g, name)
	return ok && len(rest) == 1 && strings.Contains("bwlq", rest)
}
