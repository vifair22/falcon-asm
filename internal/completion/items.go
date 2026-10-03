package completion

import (
	"fmt"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// commonMnemonics are boosted above the rest of the prefix matches, in this
// order. The ISA tables hold about 2,000 names, most of them SIMD; without a
// boost, typing "c" would bury call and cmp under cbw, cdq, clac and
// clflush. The list leans toward what the airies-boot code actually uses:
// real/protected mode transitions, port I/O and string operations.
var commonMnemonics = []string{
	"mov", "jmp", "call", "ret", "push", "pop", "cmp", "test", "add", "sub",
	"lea", "xor", "and", "or", "inc", "dec", "je", "jne", "jz", "jnz",
	"jc", "jnc", "ja", "jae", "jb", "jbe", "jg", "jge", "jl", "jle", "js",
	"jns", "loop", "shl", "shr", "sar", "not", "neg", "mul", "imul", "div",
	"idiv", "adc", "sbb", "xchg", "in", "out", "cli", "sti", "cld", "std",
	"hlt", "nop", "int", "iret", "lgdt", "lidt", "ljmp", "lret", "lcall",
	"movs", "movsb", "movsw", "movsl", "stos", "stosb", "stosw", "stosl",
	"lods", "lodsb", "lodsw", "lodsl", "cmps", "cmpsb", "scas", "scasb",
	"movzbl", "movzwl", "movsbl", "movswl", "pushf", "popf", "pusha",
	"popa", "leave", "enter", "bt", "bts", "btr", "sete", "setne", "cpuid",
	"rdmsr", "wrmsr", "ltr", "invlpg", "int3", "ud2", "bswap",
}

// suffixed lists the common mnemonics whose AT&T size-suffixed spellings
// (movl, pushw ...) are offered, with the suffixes each takes. Lookup strips
// a suffix at resolve time, so these names are not in the tables; without
// them, typing "mov" could never complete to the "movl" most code uses.
var suffixed = map[string]string{
	"mov": "bwlq", "add": "bwlq", "sub": "bwlq", "cmp": "bwlq", "test": "bwlq",
	"xor": "bwlq", "and": "bwlq", "or": "bwlq", "inc": "bwlq", "dec": "bwlq",
	"shl": "bwlq", "shr": "bwlq", "sar": "bwlq", "not": "bwlq", "neg": "bwlq",
	"mul": "bwlq", "imul": "bwlq", "div": "bwlq", "idiv": "bwlq", "adc": "bwlq",
	"sbb": "bwlq", "xchg": "bwlq", "push": "wlq", "pop": "wlq", "lea": "wlq",
	"bt": "wlq", "bts": "wlq", "btr": "wlq",
}

var suffixWidth = map[byte]string{'b': "8-bit", 'w': "16-bit", 'l': "32-bit", 'q': "64-bit"}

// prefixDetails describes the prefix words; the ISA tables only know them
// as a set.
var prefixDetails = map[string]string{
	"rep":      "Prefix: repeat the string instruction while the count register is nonzero",
	"repe":     "Prefix: repeat cmps/scas while equal and the count is nonzero",
	"repz":     "Prefix: repeat cmps/scas while zero and the count is nonzero",
	"repne":    "Prefix: repeat cmps/scas while not equal and the count is nonzero",
	"repnz":    "Prefix: repeat cmps/scas while not zero and the count is nonzero",
	"lock":     "Prefix: make the read-modify-write memory access atomic",
	"data16":   "Prefix: operand-size override (0x66) to 16 bits",
	"data32":   "Prefix: operand-size override (0x66) to 32 bits",
	"addr16":   "Prefix: address-size override (0x67) to 16 bits",
	"addr32":   "Prefix: address-size override (0x67) to 32 bits",
	"rex":      "Prefix: emit a REX prefix with no bits set",
	"rex64":    "Prefix: emit REX.W (64-bit operand size)",
	"cs":       "Prefix: CS segment override",
	"ds":       "Prefix: DS segment override",
	"es":       "Prefix: ES segment override",
	"fs":       "Prefix: FS segment override",
	"gs":       "Prefix: GS segment override",
	"ss":       "Prefix: SS segment override",
	"notrack":  "Prefix: CET indirect branch without ENDBR tracking",
	"xacquire": "Prefix: HLE hint, start an elided lock region",
	"xrelease": "Prefix: HLE hint, end an elided lock region",
	"bnd":      "Prefix: MPX, keep bounds across this branch",
}

func buildMnemonics() []mnemonic {
	rank := make(map[string]int, len(commonMnemonics))
	for i, m := range commonMnemonics {
		rank[m] = i
	}
	commonRank := func(name string) int {
		if r, ok := rank[name]; ok {
			return r
		}
		return -1
	}

	var out []mnemonic
	seen := map[string]bool{}
	add := func(m mnemonic) {
		if seen[m.label] {
			return
		}
		seen[m.label] = true
		out = append(out, m)
	}
	for _, inst := range isa.Instructions() {
		names := append([]string{inst.Name}, inst.Aliases...)
		for _, n := range names {
			// An alias another table entry shadows resolves elsewhere, so
			// its detail here would contradict hover.
			if got, suffix, ok := isa.Lookup(n); !ok || got != inst || suffix != "" {
				continue
			}
			add(mnemonic{label: n, detail: inst.Summary, kind: protocol.CompletionKindFunction,
				modes: inst.Modes, common: commonRank(n)})
		}
	}
	for _, p := range isa.Prefixes() {
		if strings.HasPrefix(p, "{") {
			// Pseudo prefixes start with a brace no client treats as a
			// word character, so they could never match a typed prefix.
			continue
		}
		detail := prefixDetails[p]
		if detail == "" {
			detail = "Prefix"
		}
		modes := ""
		if strings.HasPrefix(p, "rex") {
			modes = "64"
		}
		add(mnemonic{label: p, detail: detail, kind: protocol.CompletionKindKeyword, modes: modes, common: commonRank(p)})
	}
	// Suffixed forms go last so a real table name that happens to look
	// suffixed (movsl is the dword string move, not mov+sl) keeps its own
	// meaning through the seen check.
	for _, base := range commonMnemonics {
		sfx, ok := suffixed[base]
		if !ok {
			continue
		}
		inst, _, ok := isa.Lookup(base)
		if !ok {
			continue
		}
		for i := 0; i < len(sfx); i++ {
			add(mnemonic{label: base + sfx[i:i+1], detail: inst.Summary + " (" + sfx[i:i+1] + " = " + suffixWidth[sfx[i]] + ")",
				kind: protocol.CompletionKindFunction, modes: inst.Modes, common: rank[base], base: base})
		}
	}
	return out
}

// suffixValid drops the spellings the assembler rejects in a mode: q
// outside 64-bit code, and 32-bit push/pop inside it.
func suffixValid(m *mnemonic, mode int) bool {
	s := m.label[len(m.label)-1]
	if s == 'q' && mode != 64 {
		return false
	}
	if s == 'l' && mode == 64 && (m.base == "push" || m.base == "pop") {
		return false
	}
	return true
}

// mnemonicItems returns the mnemonics and prefixes starting with partial
// (already lowercased), valid in mode, ranked:
//
//  1. an exact match of the typed word
//  2. common mnemonics, in commonMnemonics order; a suffixed form follows
//     its base (mov, movb, movl, movw ...)
//  3. everything else, shorter names first, then alphabetically
//
// Suffixed forms are only offered once the whole base has been typed, so
// "m" is not flooded with movb/movw/movl/movq/mulb...
func mnemonicItems(partial string, mode int) []protocol.CompletionItem {
	t := loadTables()
	type hit struct {
		m     *mnemonic
		exact bool
	}
	var hits []hit
	for i := range t.mnemonics {
		m := &t.mnemonics[i]
		if !strings.HasPrefix(m.label, partial) || !validInMode(m.modes, mode) {
			continue
		}
		if m.base != "" && (!strings.HasPrefix(partial, m.base) || !suffixValid(m, mode)) {
			continue
		}
		hits = append(hits, hit{m, m.label == partial})
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.exact != b.exact {
			return a.exact
		}
		ac, bc := a.m.common >= 0, b.m.common >= 0
		if ac != bc {
			return ac
		}
		if ac && a.m.common != b.m.common {
			return a.m.common < b.m.common
		}
		if ac && (a.m.base == "") != (b.m.base == "") {
			return a.m.base == ""
		}
		if len(a.m.label) != len(b.m.label) {
			return len(a.m.label) < len(b.m.label)
		}
		return a.m.label < b.m.label
	})
	items := make([]protocol.CompletionItem, len(hits))
	for i, h := range hits {
		items[i] = protocol.CompletionItem{Label: h.m.label, Kind: h.m.kind, Detail: h.m.detail}
	}
	return items
}

// classOrder puts the registers system code reaches for first ahead of the
// hundreds of vector and mask registers.
var classOrder = []string{"gpr", "segment", "ip", "flags", "control", "debug", "x87", "mmx", "xmm", "ymm", "zmm", "mask"}

func classRank(c string) int {
	for i, k := range classOrder {
		if k == c {
			return i
		}
	}
	return len(classOrder)
}

// naturalLess orders names with embedded numbers numerically, so r8 sorts
// before r10 and xmm2 before xmm10.
func naturalLess(a, b string) bool {
	return naturalKey(a) < naturalKey(b)
}

func naturalKey(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			sb.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		// Register numbers are at most two digits; three keeps the key
		// order-preserving with room to spare.
		sb.WriteString(strings.Repeat("0", max(0, 3-(j-i))))
		sb.WriteString(s[i:j])
		i = j
	}
	return sb.String()
}

// registerItems returns the registers whose name (without '%') starts with
// partial and that are valid in mode. Labels carry the '%', because the
// edit replaces the sigil too and clients filter on the label.
func registerItems(partial string, mode int) []protocol.CompletionItem {
	var items []protocol.CompletionItem
	for _, r := range loadTables().registers {
		if !strings.HasPrefix(r.Name, partial) || !validInMode(r.Modes, mode) {
			continue
		}
		items = append(items, protocol.CompletionItem{
			Label:  "%" + r.Name,
			Kind:   protocol.CompletionKindVariable,
			Detail: fmt.Sprintf("%s, %d-bit", r.Class, r.Width),
		})
	}
	return items
}

// directiveItems returns the directives starting with partial, which
// includes the leading '.'.
func directiveItems(partial string) []protocol.CompletionItem {
	var items []protocol.CompletionItem
	for _, d := range loadTables().directives {
		if strings.HasPrefix(d.Name, partial) {
			items = append(items, protocol.CompletionItem{Label: d.Name, Kind: protocol.CompletionKindKeyword, Detail: d.Summary})
		}
	}
	return items
}

// symbolItems returns labels and macros matching partial. Matching ignores
// case for convenience, though GAS symbols are case-sensitive: the exact
// spelling still wins the ranking, and the inserted text is the symbol as
// defined. A name that is both a label and a macro is offered once, as the
// macro, since cpp substitutes it before the assembler sees a label.
func symbolItems(partial string, ctx Context) []protocol.CompletionItem {
	lower := strings.ToLower(partial)
	byName := map[string]protocol.CompletionItem{}
	collect := func(src func() []string, kind protocol.CompletionItemKind, detail string) {
		if src == nil {
			return
		}
		for _, n := range src() {
			if n != "" && strings.HasPrefix(strings.ToLower(n), lower) {
				byName[n] = protocol.CompletionItem{Label: n, Kind: kind, Detail: detail}
			}
		}
	}
	collect(ctx.Labels, protocol.CompletionKindReference, "label")
	collect(ctx.Macros, protocol.CompletionKindConstant, "macro")

	items := make([]protocol.CompletionItem, 0, len(byName))
	for _, it := range byName {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].Label, items[j].Label
		if ea, eb := a == partial, b == partial; ea != eb {
			return ea
		}
		if pa, pb := strings.HasPrefix(a, partial), strings.HasPrefix(b, partial); pa != pb {
			return pa
		}
		return a < b
	})
	return items
}
