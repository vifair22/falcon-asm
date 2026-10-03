package encoding

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

// checkSreg reports segment register uses the hardware or GAS rejects, or
// that assemble into a fault: mov into %cs (#UD), pop %cs (0x0F is the
// two-byte escape), the far-pointer loads with a register source, push and
// pop of %es/%cs/%ss/%ds in 64-bit code, and mov forms that do not exist.
func (st *stmtCtx) checkSreg() {
	ops := st.m.ops
	isSreg := func(o *operand) bool { return o.kind == asm.OpReg && o.reg.Class == "segment" }
	switch st.name {
	case "mov":
		if len(ops) != 2 {
			return
		}
		src, dst := &ops[0], &ops[1]
		if !isSreg(src) && !isSreg(dst) {
			return
		}
		switch {
		case isSreg(dst) && dst.reg.Name == "cs":
			st.reportOp(CodeSreg, 1, "mov to %cs raises #UD; %cs is loaded by a far jmp, call or ret")
		case isSreg(src) && isSreg(dst):
			st.reportMnem(CodeSreg, "there is no segment-to-segment mov; copy through a general register")
		case src.kind == asm.OpImm:
			st.reportOp(CodeSreg, 0, "a segment register cannot be loaded with an immediate; load a general "+
				"register first (movw $X, %ax; movw %ax, "+regText(dst.reg)+")")
		case src.kind == asm.OpReg && src.reg.Class == "gpr" && src.reg.Width == 8,
			dst.kind == asm.OpReg && dst.reg.Class == "gpr" && dst.reg.Width == 8:
			j := 0
			if dst.reg.Class == "gpr" {
				j = 1
			}
			st.reportOp(CodeSreg, j, "segment registers move to and from 16-, 32- or 64-bit general registers, not byte registers")
		case (src.kind == asm.OpMem || dst.kind == asm.OpMem) && (st.m.suffix == 32 || st.m.suffix == 64):
			is := st.reportMnem(CodeSreg, fmt.Sprintf("a segment register moves to and from memory as 16 bits; %s does not exist", st.mnemonic()))
			is.fixTitle = "Use the w suffix"
			is.edits = []edit{st.suffixEdit("w")}
		}
	case "pop", "push":
		if len(ops) != 1 || !isSreg(&ops[0]) {
			return
		}
		name := ops[0].reg.Name
		switch {
		case st.name == "pop" && name == "cs":
			st.reportOp(CodeSreg, 0, "pop %cs does not exist (its opcode, 0x0F, is the two-byte escape); load %cs with a far jmp or ret")
		case st.modeKnown() && st.mode() == asm.Mode64 && name != "fs" && name != "gs":
			st.reportOp(CodeSreg, 0, fmt.Sprintf("%s %s is not encodable in 64-bit code; only %%fs and %%gs can be pushed and popped", st.name, regText(ops[0].reg)))
		}
	case "lds", "les", "lss", "lfs", "lgs":
		if len(ops) == 2 && ops[0].kind == asm.OpReg {
			st.reportOp(CodeSreg, 0, fmt.Sprintf("%s loads a far pointer from memory (m16:16 or m16:32); a register source does not exist", st.name))
		}
	}
}

var shiftNames = map[string]bool{
	"shl": true, "sal": true, "shr": true, "sar": true, "rol": true, "ror": true, "rcl": true, "rcr": true,
}

// checkFixed reports operands that must be one particular register: the
// data register of in and out (%al, %ax or %eax), their port (an imm8 or
// %dx), and a shift or rotate count (an imm8 or %cl).
func (st *stmtCtx) checkFixed() {
	ops := st.m.ops
	switch {
	case st.name == "in" && len(ops) == 2:
		st.port(0)
		st.data(1)
	case st.name == "out" && len(ops) == 2:
		st.data(0)
		st.port(1)
	case (st.name == "in" || st.name == "out") && len(ops) == 1:
		st.port(0)
	case shiftNames[st.name] && len(ops) == 2, (st.name == "shld" || st.name == "shrd") && len(ops) == 3:
		st.count(0)
	}
}

func (st *stmtCtx) data(j int) {
	o := &st.m.ops[j]
	if o.kind == asm.OpReg && o.reg.Class == "gpr" && family(o.reg.Name) == "a" && o.reg.Width <= 32 && o.reg.Name != "ah" {
		return
	}
	st.reportOp(CodeFixedReg, j, fmt.Sprintf("%s moves data through %%al, %%ax or %%eax only, not %s", st.name, o.op.Text))
}

func (st *stmtCtx) port(j int) {
	o := &st.m.ops[j]
	switch {
	case o.kind == asm.OpImm, o.dxMem:
		return
	case o.kind == asm.OpReg && o.reg.Name == "dx":
		return
	}
	msg := fmt.Sprintf("the %s port is an immediate (0-0xff) or %%dx, not %s", st.name, o.op.Text)
	if o.kind == asm.OpReg && o.reg.Class == "gpr" && family(o.reg.Name) == "d" {
		if e, ok := st.nameEdit(j, "dx"); ok {
			st.explained = true
			st.a.addFix(CodeFixedReg, o.op.Start, o.op.End, msg, "Use %dx", e)
			return
		}
	}
	st.reportOp(CodeFixedReg, j, msg)
}

func (st *stmtCtx) count(j int) {
	o := &st.m.ops[j]
	switch {
	case o.kind == asm.OpImm:
		return
	case o.kind == asm.OpReg && o.reg.Name == "cl":
		return
	}
	msg := fmt.Sprintf("a %s count is an immediate or %%cl, not %s", st.name, o.op.Text)
	if o.kind == asm.OpReg && o.reg.Class == "gpr" && family(o.reg.Name) == "c" {
		if e, ok := st.nameEdit(j, "cl"); ok {
			st.explained = true
			st.a.addFix(CodeFixedReg, o.op.Start, o.op.End, msg, "Use %cl", e)
			return
		}
	}
	st.reportOp(CodeFixedReg, j, msg)
}

// checkREX reports %ah, %bh, %ch or %dh in an instruction that needs a REX
// prefix: with a REX prefix those encodings name %spl, %bpl, %sil and %dil
// instead, so the high bytes cannot be encoded at all.
func (st *stmtCtx) checkREX() {
	if !st.modeKnown() || st.mode() != asm.Mode64 {
		return
	}
	high := -1
	var why string
	for j := range st.m.ops {
		o := &st.m.ops[j]
		switch o.kind {
		case asm.OpReg:
			switch {
			case isHighByte(o.reg.Name):
				if high < 0 {
					high = j
				}
			case needsREX(o.reg):
				why = regText(o.reg)
			case o.reg.Class == "gpr" && o.reg.Width == 64:
				why = regText(o.reg) + " (a 64-bit operand needs REX.W)"
			}
		case asm.OpMem:
			for _, n := range []string{o.op.Base, o.op.Index} {
				if f := family(n); strings.HasPrefix(f, "r") && len(f) > 1 && f[1] >= '0' && f[1] <= '9' {
					why = "%" + n + " in the address"
				}
			}
		}
	}
	if high < 0 {
		return
	}
	if why == "" && st.m.suffix == 64 {
		why = "the q suffix (REX.W)"
	}
	if why == "" {
		return
	}
	st.reportOp(CodeREXHighByte, high, fmt.Sprintf("%s cannot be encoded in an instruction that needs a REX prefix (%s); "+
		"with REX the high-byte encodings mean %%spl/%%bpl/%%sil/%%dil", st.m.ops[high].op.Text, why))
}

// checkDeco reports AVX-512 decorations that are wrong whatever the
// instruction: %k0 as a write mask, {z} without a mask or on memory, and a
// broadcast on anything but memory.
func (st *stmtCtx) checkDeco() {
	for j := range st.m.ops {
		op := st.m.ops[j].op
		switch {
		case op.Mask == "k0":
			st.reportOp(CodeAVXDeco, j, "%k0 cannot be a write mask: its encoding means \"no masking\"; use %k1-%k7")
		case op.Zeroing && op.Mask == "":
			st.reportOp(CodeAVXDeco, j, "{z} (zeroing-masking) needs a write mask such as {%k1}")
		case op.Zeroing && op.Kind == asm.OpMem:
			msg := "zeroing-masking cannot apply to a memory destination; only merging-masking stores exist"
			if e, ok := st.decoEdit(j, "{z}", ""); ok {
				st.explained = true
				st.a.addFix(CodeAVXDeco, op.Start, op.End, msg, "Remove {z}", e)
				continue
			}
			st.reportOp(CodeAVXDeco, j, msg)
		case op.Broadcast != "" && op.Kind != asm.OpMem:
			st.reportOp(CodeAVXDeco, j, fmt.Sprintf("broadcast {%s} applies to a memory operand, not %s", op.Broadcast, strings.TrimSpace(op.Text)))
		}
	}
}

// decoEdit replaces the decoration text (e.g. "{z}" or "1to8") inside
// operand j.
func (st *stmtCtx) decoEdit(j int, old, repl string) (edit, bool) {
	op := st.m.ops[j].op
	src := st.a.prog.Doc.Text
	if op.Start < 0 || op.End > len(src) {
		return edit{}, false
	}
	at := strings.LastIndex(strings.ToLower(src[op.Start:op.End]), old)
	if at < 0 {
		return edit{}, false
	}
	return edit{start: op.Start + at, end: op.Start + at + len(old), text: repl}, true
}
