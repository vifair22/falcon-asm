package analysis

import (
	"fmt"
	"strconv"
	"strings"

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

// selectForms narrows inst's forms to the family. When nothing matches (the
// data has no form of that family, or the operands are wrong for the
// mnemonic), every form is used: the assembler will report a mismatch
// better than a guess here could.
func selectForms(forms []isa.Form, fam family) []isa.Form {
	var out []isa.Form
	for _, f := range forms {
		ops := strings.ToLower(f.Operands)
		set := strings.ToUpper(f.ISASet)
		var keep bool
		switch fam {
		case famAVX512:
			keep = strings.HasPrefix(set, "AVX512")
		case famYMM:
			keep = strings.Contains(ops, "ymm")
		default:
			keep = !strings.Contains(ops, "zmm") && !strings.Contains(ops, "{k}") && !strings.HasPrefix(set, "AVX512")
		}
		if keep {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return forms
	}
	return out
}

// levelCheck compares the cheapest form the operands allow with the target.
// The minimum is the right comparison because the assembler picks the
// shortest encoding that fits, which is the lowest-level one.
func (a *analyzer) levelCheck(stmt []lexer.Token, mi int, inst *isa.Instruction, word string) {
	if len(inst.Forms) == 0 {
		return
	}
	fam, phrase := operandFamily(stmt, mi)
	forms := selectForms(inst.Forms, fam)
	var best *isa.Form
	for i := range forms {
		if forms[i].Level > 0 && (best == nil || forms[i].Level < best.Level) {
			best = &forms[i]
		}
	}
	m := stmt[mi]
	if best == nil {
		if set := forms[0].ISASet; set != "" {
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
