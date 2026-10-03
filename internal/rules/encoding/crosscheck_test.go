package encoding

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// The cross-check asks GAS itself. Every form of every instruction the
// matcher judges is instantiated with concrete operands, plus mutations of
// those (a register where memory was, an immediate where a register was,
// another register width, an operand dropped), and the lot is assembled
// with the local `as`. Two properties are checked:
//
//   - precision: no line GAS assembles cleanly gets a finding from the
//     operand rules, apart from the documented cases where the rule is
//     deliberately stricter than GAS (gasStricter);
//   - recall: of the lines GAS rejects, the share the rules flag is
//     logged, and must stay above a floor so a regression in the matcher
//     shows up as a test failure rather than a quietly worse linter.
//
// The test is opt-in (FALCON_GAS_CROSSCHECK=1) and skipped when no `as`
// is installed.

// gasStricter are findings reported on lines GAS accepts, on purpose:
// GAS assembles them, but not into what was written.
var gasStricter = map[string]*regexp.Regexp{
	// mov to %cs assembles and raises #UD.
	CodeSreg: regexp.MustCompile(`^mov[wlq]? .*%cs$`),
	// GAS 2.46 assembles vcvttps2dq with an xmm and a ymm operand as the
	// ymm-to-ymm form, silently; the mixed widths are a real mistake.
	CodeAVXDeco: regexp.MustCompile(`^vcvttps2dq %[xy]mm1, %[xy]mm1$`),
}

// stricterCodes are the rules whose findings on GAS-accepted lines are by
// design (ambiguity, silent truncation, #UD at run time).
var stricterCodes = map[string]bool{
	CodeSizeAmbiguous: true, CodeImmRange: true, CodeDataRange: true,
}

type genLine struct {
	text string
	inst *isa.Instruction
}

// gprOf names a register of width w that no fixed form singles out.
func gprOf(w int16, mode asm.Mode) (string, bool) {
	switch w {
	case 8:
		return "%bl", true
	case 16:
		return "%bx", true
	case 32:
		return "%ebx", true
	case 64:
		return "%rbx", mode == asm.Mode64
	}
	return "", false
}

func memOf(mode asm.Mode) string {
	if mode == asm.Mode64 {
		return "(%rax)"
	}
	return "(%eax)"
}

// renderAlt renders one alternative as operand text.
func renderAlt(a alt, mode asm.Mode) (string, bool) {
	switch a.kind {
	case akGPR:
		return gprOf(a.width, mode)
	case akMem:
		if a.bcst != 0 {
			if a.bcstN == 0 {
				return "", false
			}
			return fmt.Sprintf("%s{1to%d}", memOf(mode), a.bcstN), true
		}
		return memOf(mode), true
	case akImm:
		return "$1", true
	case akFixed:
		return "%" + a.name, true
	case akSreg:
		return "%ds", true
	case akCR:
		return "%cr0", true
	case akDR:
		return "%dr0", true
	case akVec:
		return map[int16]string{128: "%xmm1", 256: "%ymm1", 512: "%zmm1"}[a.width], true
	case akMM:
		return "%mm1", true
	case akK:
		return "%k1", true
	case akTMM:
		return "%tmm1", mode == asm.Mode64
	case akBND:
		return "%bnd1", true
	case akSTi:
		return "%st(1)", true
	case akVSIB:
		base := "%rax"
		if mode != asm.Mode64 {
			base = "%eax"
		}
		idx := map[int16]string{128: "%xmm2", 256: "%ymm2", 512: "%zmm2"}[a.width]
		return fmt.Sprintf("(%s,%s,4)", base, idx), true
	}
	return "", false
}

// generate instantiates the forms of every judged instruction: each slot
// at its first alternative, then each other alternative of one slot at a
// time, with a write mask where the form allows one.
func generate(mode asm.Mode) []genLine {
	var out []genLine
	seen := map[string]bool{}
	add := func(inst *isa.Instruction, mnem string, ops []string) {
		text := mnem
		if len(ops) > 0 {
			text += " " + strings.Join(ops, ", ")
		}
		if !seen[text] {
			seen[text] = true
			out = append(out, genLine{text: text, inst: inst})
		}
	}
	for _, inst := range isa.Instructions() {
		if skipForms(inst) || inst.Name == "movq" {
			continue
		}
		c := formsOf(inst, false)
		for fi := range c.forms {
			f := &c.forms[fi]
			if len(f.slots) == 0 {
				continue
			}
			base := make([]string, len(f.slots))
			ok := true
			for si, s := range f.slots {
				base[si], ok = renderAlt(s.alts[0], mode)
				if !ok {
					break
				}
				if s.maskReq {
					base[si] += "{%k1}"
				}
			}
			if !ok {
				continue
			}
			add(inst, inst.Name, base)
			for si, s := range f.slots {
				for _, a := range s.alts[1:] {
					txt, ok := renderAlt(a, mode)
					if !ok {
						continue
					}
					ops := append([]string(nil), base...)
					ops[si] = txt
					add(inst, inst.Name, ops)
				}
				if s.mask && !s.maskReq && si == len(f.slots)-1 {
					ops := append([]string(nil), base...)
					ops[si] += "{%k1}"
					add(inst, inst.Name, ops)
					if s.zero {
						ops = append([]string(nil), base...)
						ops[si] += "{%k1}{z}"
						add(inst, inst.Name, ops)
					}
				}
			}
		}
	}
	return out
}

// mutate derives lines that are probably invalid from a valid one.
func mutate(l genLine, mode asm.Mode) []genLine {
	mnem, rest, _ := strings.Cut(l.text, " ")
	if rest == "" {
		return []genLine{{text: mnem + " " + memOf(mode), inst: l.inst}}
	}
	ops := splitOps(rest)
	var out []genLine
	emit := func(o []string) {
		out = append(out, genLine{text: mnem + " " + strings.Join(o, ", "), inst: l.inst})
	}
	reg, _ := gprOf(32, mode)
	for i, op := range ops {
		o := append([]string(nil), ops...)
		switch {
		case strings.HasPrefix(op, "("):
			o[i] = reg
		case strings.HasPrefix(op, "$"):
			o[i] = reg
		case strings.HasPrefix(op, "%e"):
			o[i] = "%" + op[2:]
		case strings.HasPrefix(op, "%r") && len(op) == 4:
			o[i] = "%e" + op[2:]
		case strings.HasPrefix(op, "%xmm"):
			o[i] = "%ymm" + op[4:]
		default:
			o[i] = "$1"
		}
		emit(o)
	}
	emit(ops[:len(ops)-1])
	emit(append(append([]string(nil), ops...), reg))
	return out
}

func splitOps(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '{':
			depth++
		case ')', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// gasVerdict assembles the lines and returns, per line index, "" when GAS
// accepted it silently, else its first message.
func gasVerdict(t *testing.T, lines []genLine, mode asm.Mode) []string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "x.s")
	var b strings.Builder
	fmt.Fprintf(&b, ".code%d\n", mode)
	for _, l := range lines {
		b.WriteString(l.text + "\n")
	}
	if err := os.WriteFile(src, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	flag := "--64"
	if mode != asm.Mode64 {
		flag = "--32"
	}
	out, _ := exec.Command("as", flag, "-o", filepath.Join(dir, "x.o"), src).CombinedOutput()
	verdict := make([]string, len(lines))
	re := regexp.MustCompile(`^.*?:(\d+): (Error|Warning): (.*)$`)
	for _, l := range strings.Split(string(out), "\n") {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if i := n - 2; i >= 0 && i < len(verdict) && verdict[i] == "" {
			verdict[i] = m[2] + ": " + m[3]
		}
	}
	return verdict
}

// findingsByLine lints the lines as one document and groups the findings
// by line index.
func findingsByLine(t *testing.T, lines []genLine, mode asm.Mode) map[int][]lint.Finding {
	t.Helper()
	// Chunks keep each document small: positions are computed from the
	// start of the text, so one huge document would be quadratic.
	const chunk = 500
	out := map[int][]lint.Finding{}
	for lo := 0; lo < len(lines); lo += chunk {
		var b strings.Builder
		for _, l := range lines[lo:min(lo+chunk, len(lines))] {
			b.WriteString("\t" + l.text + "\n")
		}
		for _, f := range lintText(t, b.String(), mode, lint.LangAsm, nil) {
			out[lo+f.Range.Start.Line] = append(out[lo+f.Range.Start.Line], f)
		}
	}
	return out
}

func TestGasCrossCheck(t *testing.T) {
	if _, err := exec.LookPath("as"); err != nil {
		t.Skip("no GNU as installed")
	}
	// Generating, assembling and linting some 200k lines takes long under
	// -race; the default test run stays fast and this one is opt-in.
	if os.Getenv("FALCON_GAS_CROSSCHECK") == "" {
		t.Skip("set FALCON_GAS_CROSSCHECK=1 to cross-check the rules against GNU as")
	}
	for _, mode := range []asm.Mode{asm.Mode64, asm.Mode32, asm.Mode16} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) { crossCheck(t, mode) })
	}
}

func crossCheck(t *testing.T, mode asm.Mode) {
	valid := generate(mode)
	var mutants []genLine
	for _, l := range valid {
		mutants = append(mutants, mutate(l, mode)...)
	}
	lines := append(append([]genLine(nil), valid...), mutants...)
	verdict := gasVerdict(t, lines, mode)
	found := findingsByLine(t, lines, mode)

	var falsePos []string
	rejected, flagged := 0, 0
	var missed []string
	for i, l := range lines {
		fs := found[i]
		if strings.HasPrefix(verdict[i], "Error") {
			rejected++
			if len(fs) > 0 {
				flagged++
			} else {
				missed = append(missed, l.text+"\t# "+verdict[i])
			}
			continue
		}
		if verdict[i] != "" {
			continue // GAS warned: it accepted the line but not cleanly
		}
		for _, f := range fs {
			if re := gasStricter[f.Code]; stricterCodes[f.Code] || (re != nil && re.MatchString(l.text)) {
				continue
			}
			falsePos = append(falsePos, fmt.Sprintf("%s\t[gas: %q]\t%s: %s", l.text, verdict[i], f.Code, f.Message))
		}
	}
	t.Logf("mode %d: %d generated, %d mutants; gas rejected %d, the rules flagged %d of those (%.1f%%)",
		mode, len(valid), len(mutants), rejected, flagged, 100*float64(flagged)/float64(max(rejected, 1)))
	if p := os.Getenv("FALCON_ENC_FP"); p != "" {
		_ = os.WriteFile(p+fmt.Sprint(mode), []byte(strings.Join(falsePos, "\n")+"\n"), 0o600)
		_ = os.WriteFile(p+fmt.Sprint(mode)+".missed", []byte(strings.Join(missed, "\n")+"\n"), 0o600)
	}
	if len(falsePos) > 0 {

		n := min(len(falsePos), 40)
		t.Errorf("%d findings on lines GAS accepts; first %d:\n%s", len(falsePos), n, strings.Join(falsePos[:n], "\n"))
	}
}
