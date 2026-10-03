package isa

import "sort"

// Enumeration exists for completion, which has to offer every name rather
// than resolve one. Each call returns a fresh slice so a caller can sort or
// filter it freely; the pointed-to values are shared with the lookup tables
// and must be treated as read-only.

// Instructions returns every instruction Lookup can reach by its own Name,
// sorted by Name so the result does not depend on data-file order. A
// generated entry whose Name is shadowed by a hand-written alias (xed's
// xsaves64 against the system table's xsaves) is left out: Lookup would
// never return it, so offering it would show data hover cannot.
func Instructions() []*Instruction {
	ix := load()
	out := make([]*Instruction, 0, len(ix.all))
	for _, inst := range ix.all {
		if ix.byName[inst.Name] == inst {
			out = append(out, inst)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Registers returns every register, sorted by Name.
func Registers() []*Register {
	out := make([]*Register, 0, len(registers))
	for _, r := range registers {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Directives returns every directive in table order, which groups related
// directives together (code size, sections, symbols ...).
func Directives() []*Directive {
	out := make([]*Directive, len(directiveList))
	for i := range directiveList {
		out[i] = &directiveList[i]
	}
	return out
}

// Prefixes returns the fixed prefix words, sorted. The open-ended rex.wrxb
// spellings IsPrefix also accepts are not listed: there is no finite set to
// offer, and nobody types them often enough to want them suggested.
func Prefixes() []string {
	out := make([]string, 0, len(prefixes))
	for p := range prefixes {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
