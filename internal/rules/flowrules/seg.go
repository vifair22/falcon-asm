package flowrules

import (
	"fmt"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeSegmentUnset,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Memory access through a segment register the boot entry never set (%es:(%di) stores, %ds loads in real mode).",
			Why: "In real mode every memory access is segment * 16 + offset, and the BIOS promises nothing " +
				"about DS, ES, FS or GS when it jumps to a boot sector: QEMU happens to leave zeros, many " +
				"real machines do not. A boot sector that stores through ES:DI (stos, movs, ins, or an %es: " +
				"override) or reads a variable through DS before loading the register writes or reads some " +
				"other 64 KiB of memory. The rule runs only on the function the BIOS enters (the first code " +
				"of a 16-bit file that lays out a boot sector, or _start), and reports the first use of each " +
				"segment register that no path from the entry has written. Code reached by a call or a far " +
				"jump is not checked: its caller may well have set the register.",
			Bad:  "_start:\n\tmovw $0x500, %di\n\tmovw $0, %ax\n\tstosw\n\t. = _start + 510\n\t.word 0xAA55",
			Good: "_start:\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x500, %di\n\tstosw\n\t. = _start + 510\n\t.word 0xAA55",
		},
		Check: checkSegments,
	})
}

func checkSegments(c *lint.Context) {
	f := factsOf(c)
	if f == nil || f.bootFunc < 0 {
		return
	}
	p, g, res := f.p, f.g, f.res
	done := map[flow.Reg]bool{}
	for _, b := range g.Funcs[f.bootFunc].Blocks {
		stmts(g, b, func(i int) bool {
			st := &p.Stmts[i]
			if st.Kind != asm.Instr || st.Inst == nil || st.Mode != asm.Mode16 || !res.Reachable(i) {
				return true
			}
			for _, u := range segUses(st) {
				if done[u.seg] || res.In[i].Get(u.seg).Def != flow.DefEntry {
					continue
				}
				if u.seg == flow.DS && u.si && res.In[i].Get(flow.RSI).Def == flow.DefEntry {
					// DS:SI as the chain loader handed it over: the
					// partition entry an MBR passes to the boot sector.
					continue
				}
				done[u.seg] = true
				name := u.seg.Name(16)
				msg := fmt.Sprintf("%s %s through %%%s, which nothing has loaded since the BIOS entered this boot sector: its value is whatever the BIOS left", st.Mnemonic, u.what, name)
				if u.op >= 0 {
					c.ReportOp(i, u.op, msg)
				} else {
					c.ReportStmt(i, msg)
				}
			}
			return true
		})
	}
}

// segUse is one segment register a statement addresses memory through.
type segUse struct {
	seg  flow.Reg
	op   int    // operand index, -1 for the implicit operand of a string instruction
	what string // "stores", "reads"
	si   bool   // the offset is SI (string source or (%si))
}

// segUses lists the segment registers an instruction's memory accesses go
// through: explicit overrides, DS for an operand with no override whose
// base is not BP or SP (those default to SS), and the string
// instructions' DS:SI source and ES:DI destination.
func segUses(st *asm.Stmt) []segUse {
	name := st.Inst.Name
	if name == "lea" || name == "nop" || isPrefetch(name) {
		return nil
	}
	var out []segUse
	n := len(st.Ops)
	srcOverride := ""
	for k := range st.Ops {
		op := &st.Ops[k]
		if op.Kind != asm.OpMem {
			continue
		}
		what := "reads"
		if k == n-1 && !readOnly[name] {
			what = "stores"
		}
		if isStringOp(st) {
			// Only the source of a string instruction can be overridden;
			// the ES:DI destination is fixed.
			if op.Seg != "" && (op.Base == "si" || op.Base == "esi") {
				srcOverride = op.Seg
			}
			continue
		}
		seg := op.Seg
		if seg == "" {
			switch op.Base {
			case "bp", "ebp", "sp", "esp":
				continue
			}
			seg = "ds"
		}
		if r, _, _, ok := flow.RegOf(seg); ok && (r == flow.DS || r == flow.ES || r == flow.FS || r == flow.GS) {
			out = append(out, segUse{seg: r, op: k, what: what, si: op.Base == "si" || op.Base == "esi"})
		}
	}
	if !isStringOp(st) {
		return out
	}
	src := flow.DS
	if srcOverride != "" {
		r, _, _, ok := flow.RegOf(srcOverride)
		if !ok || r != flow.DS && r != flow.ES && r != flow.FS && r != flow.GS {
			src = flow.NoReg
		} else {
			src = r
		}
	}
	switch name {
	case "movs":
		out = append(out, segUse{seg: flow.ES, op: -1, what: "stores"})
		if src != flow.NoReg {
			out = append(out, segUse{seg: src, op: -1, what: "reads", si: true})
		}
	case "stos", "ins":
		out = append(out, segUse{seg: flow.ES, op: -1, what: "stores"})
	case "scas":
		out = append(out, segUse{seg: flow.ES, op: -1, what: "reads"})
	case "cmps":
		out = append(out, segUse{seg: flow.ES, op: -1, what: "reads"})
		if src != flow.NoReg {
			out = append(out, segUse{seg: src, op: -1, what: "reads", si: true})
		}
	case "lods", "outs":
		if src != flow.NoReg {
			out = append(out, segUse{seg: src, op: -1, what: "reads", si: true})
		}
	}
	return out
}

func isPrefetch(name string) bool {
	return len(name) >= 8 && name[:8] == "prefetch"
}
