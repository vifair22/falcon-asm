// Package cpu holds the CPU profile rule of roadmap section 2 (item 16):
// instructions the target processor does not implement. The x86-64 level
// rules in internal/rules/target cover the psABI levels; this one covers a
// named processor (target.cpu in .falcon-asm.json, or a -march naming one),
// which is what code for old machines needs: cpuid on a 386, cmov before the
// Pentium Pro, rdtsc on a 486.
package cpu

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Code is the rule's code.
const Code = "cpu-unsupported"

func init() {
	lint.Register(&lint.Rule{
		Code:    Code,
		Section: 2,
		Default: lint.Warning,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "Instruction the target CPU profile does not implement (cmov on a Pentium, cpuid on a 386).",
			Why: "A CPU target (`target.cpu` in .falcon-asm.json, or a -march that names a processor such as " +
				"i586 or pentium3) promises the code runs on that processor. An instruction from a later " +
				"generation assembles without complaint and raises #UD on the old machine, typically the one " +
				"nobody tests on. The message names the ISA extension and the first profile that has it. " +
				"Profiles list only what every part of a generation implements, so the rule errs toward " +
				"reporting; an instruction guarded by a CPUID check can be silenced on its line. With only an " +
				"x86-64 level target the level rules apply instead.",
			Bad:  "# .falcon-asm.json: {\"target\": {\"cpu\": \"pentium\"}}\n\tcmovel %ebx, %eax",
			Good: "# .falcon-asm.json: {\"target\": {\"cpu\": \"pentiumpro\"}}\n\tcmovel %ebx, %eax",
		},
		Check: check,
	})
}

// Profile resolves a target's CPU: a profile name or a gcc -march alias.
func Profile(cpu string) (*hwdb.Profile, bool) {
	if cpu == "" {
		return nil, false
	}
	if p, ok := hwdb.CPUProfile(cpu); ok {
		return p, true
	}
	if name, ok := hwdb.ProfileForMarch(cpu); ok {
		return hwdb.CPUProfile(name)
	}
	return nil, false
}

func check(c *lint.Context) {
	p, ok := Profile(c.Target.CPU)
	// A level profile named as the CPU says what the level rules already
	// check when a level is set too; reporting it twice helps nobody.
	if !ok || c.Prog == nil || p.Level > 0 && c.Target.Level > 0 {
		return
	}
	src := c.Target.Sources["cpu"]
	if src == "" {
		src = "target"
	}
	for i := range c.Prog.Stmts {
		s := &c.Prog.Stmts[i]
		if s.Kind != asm.Instr || s.Inst == nil || s.InMacroDef || len(s.Inst.Forms) == 0 {
			continue
		}
		set, unsupported := missing(p, s)
		if !unsupported {
			continue
		}
		first := ""
		set, first = earliest(p, s, set)
		msg := fmt.Sprintf("%s is %s, which the %s profile (%s) does not implement", s.Mnemonic, set, p.Name, src)
		if first != "" {
			msg += "; first in " + first
		}
		c.ReportRange(s.MnemStart, s.MnemStart+len(s.Mnemonic), msg)
	}
}

// missing reports whether no form of the instruction is in the profile,
// and names the ISA set of the first form for the message.
func missing(p *hwdb.Profile, s *asm.Stmt) (string, bool) {
	for _, f := range s.Inst.Forms {
		if f.ISASet == "" || p.Supports(f.ISASet) {
			return "", false
		}
	}
	if amdSyscall(p, s) {
		return "", false
	}
	return s.Inst.Forms[0].ISASet, true
}

// amdSyscall covers what the instruction tables cannot say: syscall and
// sysret are classified LONGMODE, which is right for Intel (they only exist
// in 64-bit mode there), but AMD introduced them for 32-bit code on the
// K6-2, and the K7 has them too. In 16- or 32-bit code on a profile
// descended from those, they are supported.
func amdSyscall(p *hwdb.Profile, s *asm.Stmt) bool {
	if s.Inst.Name != "syscall" && s.Inst.Name != "sysret" || s.Mode == asm.Mode64 {
		return false
	}
	for q := p; q != nil; {
		switch q.Name {
		case "k6-2", "athlon":
			return true
		}
		if q.Base == "" {
			return false
		}
		next, ok := hwdb.CPUProfile(q.Base)
		if !ok {
			return false
		}
		q = next
	}
	return false
}

// earliest picks, among the ISA sets of the instruction's forms, the one
// the first profile to have any of them implements: `movd` is MMX on a
// Pentium MMX before it is SSE2, and the message should say so.
func earliest(p *hwdb.Profile, s *asm.Stmt, fallback string) (string, string) {
	order := map[string]int{}
	for i, n := range hwdb.Profiles() {
		order[n] = i
	}
	best, bestAt, bestRank, found := fallback, "", 0, false
	for _, f := range s.Inst.Forms {
		at := firstWith(p, f.ISASet)
		if at == "" {
			continue
		}
		rank := order[at]
		if descends(profileNamed(at), p.Name) {
			rank -= len(order) // the target's own line first
		}
		if !found || rank < bestRank {
			best, bestAt, bestRank, found = f.ISASet, at, rank, true
		}
	}
	return best, bestAt
}

func profileNamed(name string) *hwdb.Profile {
	q, _ := hwdb.CPUProfile(name)
	return q
}

// firstWith names the earliest profile that implements set, preferring the
// target's own line of descent (a Pentium's successor rather than an AMD
// part) and falling back to the first in table order.
func firstWith(target *hwdb.Profile, set string) string {
	var any string
	for _, name := range hwdb.Profiles() {
		q, ok := hwdb.CPUProfile(name)
		if !ok || !q.Supports(set) {
			continue
		}
		if descends(q, target.Name) {
			return name
		}
		if any == "" && !strings.HasPrefix(name, "x86-64") {
			any = name
		}
	}
	return any
}

func descends(q *hwdb.Profile, ancestor string) bool {
	seen := map[string]bool{}
	for q != nil && !seen[q.Name] {
		if q.Name == ancestor {
			return true
		}
		seen[q.Name] = true
		if q.Base == "" {
			return false
		}
		q, _ = hwdb.CPUProfile(q.Base)
	}
	return false
}
