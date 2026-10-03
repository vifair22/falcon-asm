package hw

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
)

// RegHint is the tracked register state shown before an interesting
// statement (roadmap item 84): the inputs a BIOS service reads, the port
// in DX, the MSR in ECX, the CPUID leaf, the value a control register
// receives, CR0 at a far jump.
type RegHint struct {
	Stmt    int
	Label   string // compact: "ah=42h dl=80h"
	Tooltip string
}

// maxHintRegs bounds a hint's length: a line of code is the main thing on
// screen, not its register file.
const maxHintRegs = 5

// RegisterHints computes the hints of every interesting statement, in
// statement order. It needs a flow result (NewIndexFlow); without one it
// returns nothing, since the block-local values are a subset hover
// already shows.
func (ix *Index) RegisterHints(db *hwdb.DB) []RegHint {
	if ix.fl == nil {
		return nil
	}
	var out []RegHint
	for i := range ix.Prog.Stmts {
		s := &ix.Prog.Stmts[i]
		if s.Kind != asm.Instr || s.Inst == nil || s.InMacroDef || !ix.fl.Reachable(i) {
			continue
		}
		var parts []string
		switch name := s.Inst.Name; {
		case name == "int":
			parts = ix.intHint(db, i, s)
		case name == "in" || name == "out":
			parts = ix.portHint(i, s, name == "out")
		case name == "rdmsr":
			parts = ix.msrParts(db, i)
		case name == "wrmsr":
			parts = append(ix.msrParts(db, i), ix.regParts(i, "edx", "eax")...)
		case name == "cpuid":
			parts = ix.regParts(i, "eax", "ecx")
		case name == "xsetbv":
			parts = ix.regParts(i, "ecx", "edx", "eax")
		case name == "lgdt" || name == "lidt":
			if s.Mode == asm.Mode16 {
				parts = ix.regParts(i, "ds")
			}
		case name == "mov" && len(s.Ops) == 2 && s.Ops[1].Kind == asm.OpReg && s.Ops[1].Reg != nil &&
			s.Ops[1].Reg.Class == "control" && s.Ops[0].Kind == asm.OpReg:
			parts = ix.regParts(i, s.Ops[0].RegName)
		case s.IsFar() && (s.IsUncondJump() || s.IsCall()):
			parts = ix.cr0Part(i)
		}
		if len(parts) == 0 {
			continue
		}
		if len(parts) > maxHintRegs {
			parts = parts[:maxHintRegs]
		}
		out = append(out, RegHint{Stmt: i, Label: strings.Join(parts, " "), Tooltip: ix.hintTooltip(db, i)})
	}
	return out
}

// hexH is the compact hint number: 42h, C0000080h.
func hexH(v uint64) string { return fmt.Sprintf("%Xh", v) }

// regPart is "reg=value" when every bit of reg is known before i, or the
// values per path ("ah=2h|42h"); "" otherwise. ECX gets the MSR name when
// the statement is an rdmsr/wrmsr.
func (ix *Index) regPart(i int, reg string) string {
	vals, ok := Candidates(ix.fl, i, reg, 3)
	if !ok {
		return ""
	}
	var xs []string
	for _, v := range vals {
		xs = append(xs, hexH(v))
	}
	return reg + "=" + strings.Join(xs, "|")
}

func (ix *Index) regParts(i int, regs ...string) []string {
	var out []string
	for _, r := range regs {
		if p := ix.regPart(i, r); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// msrParts is ECX with the MSR's short name: "ecx=C0000080h (EFER)".
func (ix *Index) msrParts(db *hwdb.DB, i int) []string {
	p := ix.regPart(i, "ecx")
	if p == "" {
		return nil
	}
	if idx, ok := ix.fl.KnownAt(i, "ecx"); ok {
		if m, ok := db.LookupMSR(idx); ok {
			name := m.Alias
			if name == "" {
				name = m.NameFor(idx)
			}
			p += " (" + name + ")"
		}
	}
	return []string{p}
}

// intHint shows AH (and AL) and then the registers the service reads.
func (ix *Index) intHint(db *hwdb.DB, i int, s *asm.Stmt) []string {
	if len(s.Ops) != 1 || !s.Ops[0].ImmVal.Known {
		return nil
	}
	vec := uint64(s.Ops[0].ImmVal.V)
	parts := ix.regParts(i, "ah")
	ah, ahOK := ix.fl.KnownAt(i, "ah")
	if !ahOK {
		return parts
	}
	al, alOK := ix.fl.KnownAt(i, "al")
	svc, ok := db.LookupBIOS(vec, ah, al, alOK)
	if !ok {
		return parts
	}
	seen := map[string]bool{"ah": true}
	for _, in := range svc.In {
		for _, r := range strings.Split(strings.ToLower(in[0]), ":") {
			r = strings.TrimSpace(r)
			if seen[r] {
				continue
			}
			seen[r] = true
			if _, _, _, ok := flow.RegOf(r); !ok {
				continue
			}
			if p := ix.regPart(i, r); p != "" {
				parts = append(parts, p)
			}
		}
	}
	return parts
}

// portHint shows DX for a port given in it, and the byte written by out.
func (ix *Index) portHint(i int, s *asm.Stmt, write bool) []string {
	if len(s.Ops) != 2 {
		return nil
	}
	portOp, dataOp := &s.Ops[0], &s.Ops[1]
	if write {
		portOp, dataOp = &s.Ops[1], &s.Ops[0]
	}
	var parts []string
	if portOp.Kind == asm.OpReg && portOp.RegName == "dx" {
		parts = ix.regParts(i, "dx")
	}
	if write && dataOp.Kind == asm.OpReg {
		parts = append(parts, ix.regParts(i, dataOp.RegName)...)
	}
	return parts
}

// cr0Part names the mode bits CR0 is known to hold at a far jump: the
// mode switch it completes.
func (ix *Index) cr0Part(i int) []string {
	v, m, ok := ix.fl.ValueAt(i, "cr0")
	if !ok {
		return nil
	}
	var out []string
	for _, b := range []struct {
		bit  uint64
		name string
	}{{1, "PE"}, {1 << 31, "PG"}} {
		if m&b.bit != 0 {
			out = append(out, fmt.Sprintf("%s=%d", b.name, v&b.bit/b.bit))
		}
	}
	return out
}

// hintTooltip is the hover's register lines for the statement, without
// markdown, so the tooltip says what the values mean.
func (ix *Index) hintTooltip(db *hwdb.DB, i int) string {
	for _, k := range ix.evAt[i] {
		e := ix.trk.events[k]
		switch e.kind {
		case evInt:
			if e.a.known&0xFF00 == 0xFF00 {
				if name, _, ok := db.BIOS(uint64(e.n), e.a.val>>8&0xFF); ok {
					return name
				}
			}
		case evPort:
			if !e.imm && e.d.full(16) {
				if h, ok := db.PortFor(e.d.val&0xFFFF, e.write); ok {
					return fmt.Sprintf("port %#x: %s", e.d.val&0xFFFF, h.Name())
				}
			}
		case evMSR:
			if e.c.full(32) {
				if name, _, ok := db.MSR(e.c.val & 0xFFFFFFFF); ok {
					return fmt.Sprintf("MSR %#x: %s", e.c.val&0xFFFFFFFF, name)
				}
			}
		case evCR:
			if md := crWriteHover(e, ix.Prog.Stmts[e.stmt].Mode); md != "" {
				md = strings.ReplaceAll(md, "**", "")
				md = strings.ReplaceAll(md, "`", "")
				first, _, _ := strings.Cut(md, "\n")
				return first
			}
		}
	}
	return "register values tracked by falcon-asm's dataflow analysis"
}
