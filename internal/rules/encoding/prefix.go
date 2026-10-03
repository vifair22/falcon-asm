package encoding

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

// lockable are the read-modify-write instructions LOCK applies to; on any
// other instruction it raises #UD, and GAS rejects it.
var lockable = map[string]bool{
	"add": true, "adc": true, "and": true, "btc": true, "btr": true, "bts": true,
	"cmpxchg": true, "cmpxchg8b": true, "cmpxchg16b": true, "dec": true, "inc": true,
	"neg": true, "not": true, "or": true, "sbb": true, "sub": true, "xor": true,
	"xadd": true, "xchg": true,
}

// repOK are the non-string instructions GAS accepts after rep: rep ret
// (the AMD branch-predictor idiom), rep nop (pause) and rep bsf/bsr (the
// tzcnt/lzcnt encodings, which older CPUs run as bsf/bsr).
var repOK = map[string]bool{"ret": true, "nop": true, "bsf": true, "bsr": true, "pause": true}

// repChecked are the categories a rep prefix is reported on: plain
// integer and vector instructions, where GAS rejects it. Anything else
// (system, padlock, string, newer extensions with their own F3 meanings)
// is left alone.
var repChecked = map[string]bool{
	"BINARY": true, "LOGICAL": true, "DATAXFER": true, "SHIFT": true, "ROTATE": true,
	"BITBYTE": true, "PUSH": true, "POP": true, "SETCC": true, "CMOV": true,
	"SEMAPHORE": true, "CONVERT": true, "DECIMAL": true, "FLAGOP": true,
	"SSE": true, "AVX": true, "AVX2": true, "AVX512": true, "MMX": true,
	"LOGICAL_FP": true, "BROADCAST": true, "BLEND": true, "CALL": true, "UNCOND_BR": true,
	"COND_BR": true, "INTERRUPT": true, "IO": true, "SEGOP": true,
}

// prefixGroup classifies a prefix word for conflict checks.
func prefixGroup(w string) string {
	switch w {
	case "lock":
		return "lock"
	case "rep", "repe", "repz", "repne", "repnz":
		return "rep"
	case "cs", "ds", "es", "fs", "gs", "ss":
		return "seg"
	case "data16", "data32":
		return "data"
	case "addr16", "addr32":
		return "addr"
	}
	return ""
}

// checkPrefixes reports prefixes that conflict, repeat, are redundant in
// the mode, or apply to an instruction that cannot take them.
func (st *stmtCtx) checkPrefixes() {
	if len(st.prefixes) == 0 {
		return
	}
	seen := map[string]*prefixTok{}
	var lock, rep *prefixTok
	conflict := false
	for k := range st.prefixes {
		p := &st.prefixes[k]
		g := prefixGroup(p.word)
		if g == "" {
			continue
		}
		// LOCK and the REP family share prefix group 1.
		key := g
		if g == "lock" {
			key = "rep"
		}
		if prev := seen[key]; prev != nil {
			conflict = true
			if prev.word == p.word {
				st.prefixIssue(p, fmt.Sprintf("%s is repeated; GAS rejects the same prefix twice", p.word))
			} else {
				st.prefixIssue(p, fmt.Sprintf("%s conflicts with %s: only one prefix of this group applies", p.word, prev.word))
			}
			continue
		}
		seen[key] = p
		switch g {
		case "lock":
			lock = p
		case "rep":
			rep = p
		}
		st.redundant(p)
	}
	if conflict {
		return
	}
	if seg := seen["seg"]; seg != nil {
		for j := range st.s.Ops {
			op := &st.s.Ops[j]
			if op.Kind == asm.OpMem && op.Seg != "" && op.Seg != seg.word {
				st.prefixIssue(seg, fmt.Sprintf("the %s prefix and the %%%s: override in %s conflict; GAS keeps the prefix", seg.word, op.Seg, op.Text))
				break
			}
		}
	}
	if lock != nil {
		st.checkLock(lock)
	}
	if rep != nil {
		st.checkRep(rep)
	}
}

func (st *stmtCtx) prefixIssue(p *prefixTok, msg string) {
	st.explained = true
	st.a.add(CodePrefix, p.start, p.end, msg)
}

// redundant reports a size prefix that selects what the mode already uses;
// GAS rejects it ("redundant addr32 prefix"). data16 on an explicitly
// 16-bit (w) instruction outside 16-bit code is the same mistake.
func (st *stmtCtx) redundant(p *prefixTok) {
	if !st.modeKnown() {
		return
	}
	mode := st.mode()
	switch {
	case p.word == "addr16" && mode == asm.Mode16, p.word == "data16" && mode == asm.Mode16,
		p.word == "addr32" && mode == asm.Mode32, p.word == "data32" && mode == asm.Mode32:
		st.prefixIssue(p, fmt.Sprintf("%s is redundant in %d-bit code, which already uses that size; GAS rejects it", p.word, mode))
	case p.word == "data16" && mode != asm.Mode16 && st.suffix == "w" && !isX87(st.inst):
		st.prefixIssue(p, fmt.Sprintf("data16 is redundant: the w suffix of %s already selects 16-bit operands; GAS rejects the pair", st.mnemonic()))
	case p.word == "data32" && mode == asm.Mode16 && st.suffix == "l" && !isX87(st.inst):
		st.prefixIssue(p, fmt.Sprintf("data32 is redundant: the l suffix of %s already selects 32-bit operands; GAS rejects the pair", st.mnemonic()))
	}
}

func (st *stmtCtx) checkLock(p *prefixTok) {
	if !lockable[st.name] {
		st.prefixIssue(p, fmt.Sprintf("lock applies only to read-modify-write instructions on memory (add, xchg, cmpxchg, ...), not %s", st.name))
		return
	}
	ops := st.s.Ops
	if len(ops) == 0 {
		return
	}
	for j := range ops {
		if ops[j].Kind == asm.OpExpr || (ops[j].Kind == asm.OpReg && ops[j].Reg == nil) || strings.Contains(ops[j].Disp, "%") {
			return // a macro argument or inline operand: not known
		}
	}
	dst := ops[len(ops)-1].Kind == asm.OpMem
	if st.name == "xchg" {
		for j := range ops {
			dst = dst || ops[j].Kind == asm.OpMem
		}
	}
	if !dst {
		st.prefixIssue(p, fmt.Sprintf("lock needs a memory destination; %s on registers is not lockable", st.mnemonic()))
	}
}

func (st *stmtCtx) checkRep(p *prefixTok) {
	if repOK[st.name] || !repChecked[st.inst.Category] {
		return
	}
	// With no operands, movsd and cmpsd are the string instructions.
	if len(st.s.Ops) == 0 && (st.name == "movsd" || st.name == "cmpsd") {
		return
	}
	st.prefixIssue(p, fmt.Sprintf("%s repeats only string instructions (movs, stos, lods, scas, cmps, ins, outs), not %s", p.word, st.name))
}
