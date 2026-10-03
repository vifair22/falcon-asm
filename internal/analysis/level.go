package analysis

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// family is the encoding family an instruction's operands commit it to.
// Most vector mnemonics have SSE/VEX/EVEX forms at different levels, and
// only the operands say which one the assembler will pick, so the level of
// "vpermd" alone is meaningless: with ymm it is AVX2 (v3), with zmm it is
// AVX512F (v4).
type family int

const (
	famPlain  family = iota // no ymm and nothing EVEX-only
	famYMM                  // a ymm0-15 operand: a VEX.256 (or EVEX.256) form
	famAVX512               // zmm, opmask, xmm/ymm16-31 or an EVEX decoration
)

// operandFamily inspects everything after the mnemonic, plus any {evex}
// pseudo prefix before it, and returns the family and a short phrase for
// the message.
func operandFamily(stmt []lexer.Token, mi int) (family, string) {
	fam := famPlain
	zmm := false
	for _, t := range stmt[:mi] {
		if t.Kind == lexer.Prefix && strings.EqualFold(t.Text, "{evex}") {
			fam = famAVX512
		}
	}
	for _, t := range stmt[mi+1:] {
		switch t.Kind {
		case lexer.Punct:
			// Braces only appear in operands as EVEX decorations: {%k1},
			// {z}, {1to16}, {sae}, {rn-sae}.
			if t.Text == "{" {
				fam = famAVX512
			}
		case lexer.Register:
			r, ok := isa.LookupRegister(t.Text)
			if !ok {
				continue
			}
			switch {
			case r.Class == "zmm":
				fam, zmm = famAVX512, true
			case r.Class == "mask", (r.Class == "xmm" || r.Class == "ymm") && regNumber(r.Name) >= 16:
				fam = famAVX512
			case r.Class == "ymm" && fam == famPlain:
				fam = famYMM
			}
		}
	}
	switch {
	case zmm:
		return fam, " with zmm operands"
	case fam == famAVX512:
		return fam, " with AVX-512 operands"
	case fam == famYMM:
		return fam, " with ymm operands"
	}
	return fam, ""
}

// regNumber returns the trailing decimal number of a register name such as
// "xmm17", or -1 when there is none.
func regNumber(name string) int {
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	n, err := strconv.Atoi(name[i:])
	if err != nil {
		return -1
	}
	return n
}

// formInFamily reports whether a form belongs to the operand family.
//
// It is a predicate rather than a filtered copy: levelCheck runs for every
// instruction of a document, and copying the forms of mov and friends was
// most of what an analysis allocated.
func formInFamily(f *isa.Form, fam family) bool {
	ops := strings.ToLower(f.Operands)
	set := strings.ToUpper(f.ISASet)
	switch fam {
	case famAVX512:
		return strings.HasPrefix(set, "AVX512")
	case famYMM:
		return strings.Contains(ops, "ymm")
	default:
		return !strings.Contains(ops, "zmm") && !strings.Contains(ops, "{k}") && !strings.HasPrefix(set, "AVX512")
	}
}

// selectForms narrows inst's forms to the family and returns the
// lowest-level form with a known level (nil if none) and the first selected
// form, which names the ISA set when no level is known. When nothing
// matches (the data has no form of that family, or the operands are wrong
// for the mnemonic), every form is used: the assembler will report a
// mismatch better than a guess here could.
func selectForms(forms []isa.Form, fam family) (best, first *isa.Form) {
	for pass := 0; pass < 2 && first == nil; pass++ {
		for i := range forms {
			f := &forms[i]
			if pass == 0 && !formInFamily(f, fam) {
				continue
			}
			if first == nil {
				first = f
			}
			if f.Level > 0 && (best == nil || f.Level < best.Level) {
				best = f
			}
		}
	}
	return best, first
}

// familyForms caches selectForms per instruction and family: instructions
// are immutable once the tables load, and classifying every form of mov on
// every mov of a document was most of an analysis' time. Indexes are -1
// when there is no such form.
type familyForms struct {
	best, first [famAVX512 + 1]int
}

var formCache sync.Map // *isa.Instruction -> *familyForms

func cachedForms(inst *isa.Instruction, fam family) (best, first *isa.Form) {
	v, ok := formCache.Load(inst)
	if !ok {
		ff := &familyForms{}
		for f := famPlain; f <= famAVX512; f++ {
			b, fi := selectForms(inst.Forms, f)
			ff.best[f], ff.first[f] = formIndex(inst.Forms, b), formIndex(inst.Forms, fi)
		}
		v, _ = formCache.LoadOrStore(inst, ff)
	}
	ff := v.(*familyForms)
	if i := ff.best[fam]; i >= 0 {
		best = &inst.Forms[i]
	}
	if i := ff.first[fam]; i >= 0 {
		first = &inst.Forms[i]
	}
	return best, first
}

// formIndex finds f's index in forms, which it points into.
func formIndex(forms []isa.Form, f *isa.Form) int {
	for i := range forms {
		if &forms[i] == f {
			return i
		}
	}
	return -1
}

// levelCheck compares the cheapest form the operands allow with the target.
// The minimum is the right comparison because the assembler picks the
// shortest encoding that fits, which is the lowest-level one.
func (a *analyzer) levelCheck(stmt []lexer.Token, mi int, inst *isa.Instruction, word string) {
	if len(inst.Forms) == 0 {
		return
	}
	fam, phrase := operandFamily(stmt, mi)
	best, first := cachedForms(inst, fam)
	m := stmt[mi]
	if best == nil {
		if set := first.ISASet; set != "" {
			a.report(m, protocol.SeverityInformation, CodeLevelOutside, fmt.Sprintf(
				"%s%s is outside the x86-64 level scheme: requires %s", word, phrase, setName(set, 0)))
		}
		return
	}
	if best.Level <= a.cfg.Level {
		return
	}
	target := fmt.Sprintf("v%d", a.cfg.Level)
	if a.cfg.LevelSource != "" {
		target += " (from " + a.cfg.LevelSource + ")"
	}
	a.report(m, protocol.SeverityWarning, CodeLevel, fmt.Sprintf(
		"%s%s needs x86-64-v%d (%s); target is %s", word, phrase, best.Level, setName(best.ISASet, best.Level), target))
}

// setName shortens an XED ISA set for a message. The vector-length split
// (AVX512F_512, AVX512F_256 ...) is an XED bookkeeping detail; the CPUID
// feature a reader would check is the family, AVX512F.
func setName(set string, lvl isa.Level) string {
	s := strings.ToUpper(set)
	for _, suf := range []string{"_128", "_256", "_512", "_SCALAR"} {
		s = strings.TrimSuffix(s, suf)
	}
	if lvl == 4 {
		if i := strings.IndexByte(s, '_'); i > 0 {
			s = s[:i]
		}
	}
	return s
}

// LevelOverride finds the in-file target comment "falcon-asm: level=vN"
// (N 1-4) in any comment style. "x86-64-vN" and "x86-64" (= v1) are accepted
// as values too, matching how the level is spelled in -march, and
// "level=none" returns (0, true) so a file can opt out of a project-wide
// target. The first valid comment wins.
func LevelOverride(src string) (isa.Level, bool) {
	l, _, ok := levelOverride(lexer.Tokenize(src))
	return l, ok
}

func levelOverride(toks []lexer.Token) (isa.Level, int, bool) {
	for _, t := range toks {
		if t.Kind != lexer.Comment {
			continue
		}
		if l, ok := parseOverride(t.Text); ok {
			return l, t.Line, true
		}
	}
	return 0, 0, false
}

const overrideTag = "falcon-asm:"

func parseOverride(comment string) (isa.Level, bool) {
	// Every comment of a document comes through here; lowering each one to
	// look for a tag almost none contain allocated per comment.
	if !containsFold(comment, overrideTag) {
		return 0, false
	}
	s := strings.ToLower(comment)
	for {
		i := strings.Index(s, overrideTag)
		if i < 0 {
			return 0, false
		}
		s = s[i+len(overrideTag):]
		rest := strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(rest, "level") {
			continue
		}
		rest = strings.TrimLeft(rest[len("level"):], " \t")
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		rest = strings.TrimLeft(rest[1:], " \t")
		end := 0
		for end < len(rest) && (rest[end] == '-' || rest[end] == '_' ||
			(rest[end] >= '0' && rest[end] <= '9') || (rest[end] >= 'a' && rest[end] <= 'z')) {
			end++
		}
		if l, ok := levelValue(rest[:end]); ok {
			return l, true
		}
	}
}

// containsFold reports whether s contains the lowercase ASCII tag, ignoring
// ASCII case, without allocating.
func containsFold(s, tag string) bool {
	for i := 0; i+len(tag) <= len(s); i++ {
		if s[i]|0x20 == tag[0] && strings.EqualFold(s[i:i+len(tag)], tag) {
			return true
		}
	}
	return false
}

func levelValue(v string) (isa.Level, bool) {
	switch v {
	case "none":
		return 0, true
	case "x86-64":
		return 1, true
	}
	v = strings.TrimPrefix(v, "x86-64-")
	if len(v) == 2 && v[0] == 'v' && v[1] >= '1' && v[1] <= '4' {
		return isa.Level(v[1] - '0'), true
	}
	return 0, false
}

// LevelOverrideTokens is LevelOverride over a token stream the caller
// already has, also returning the zero-based lexer line of the comment, so
// a caller resolving a document's target does not tokenize it again.
func LevelOverrideTokens(toks []lexer.Token) (isa.Level, int, bool) {
	return levelOverride(toks)
}
