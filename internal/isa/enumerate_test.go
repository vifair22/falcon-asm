package isa

import (
	"sort"
	"testing"
)

func TestInstructionsEnumeratesIndex(t *testing.T) {
	insts := Instructions()
	if len(insts) == 0 {
		t.Fatal("no instructions")
	}
	if !sort.SliceIsSorted(insts, func(i, j int) bool { return insts[i].Name < insts[j].Name }) {
		t.Error("Instructions not sorted by name")
	}
	seen := map[string]bool{}
	for _, inst := range insts {
		if seen[inst.Name] {
			t.Errorf("duplicate %q", inst.Name)
		}
		seen[inst.Name] = true
		// Every enumerated instruction must be the one Lookup returns, or
		// completion would offer a summary hover disagrees with.
		got, suffix, ok := Lookup(inst.Name)
		if !ok || suffix != "" || got != inst {
			t.Errorf("Lookup(%q) = %v %q %v, want the enumerated entry", inst.Name, got, suffix, ok)
		}
	}
	for _, want := range []string{"mov", "lgdt", "iret", "pusha", "vpaddd"} {
		if !seen[want] {
			t.Errorf("missing %q", want)
		}
	}
	// A fresh slice per call: sorting one result must not disturb the index.
	insts[0], insts[1] = insts[1], insts[0]
	if again := Instructions(); again[0] == insts[0] {
		t.Error("Instructions shares its backing array between calls")
	}
}

func TestRegistersEnumeratesTable(t *testing.T) {
	regs := Registers()
	if len(regs) != len(registers) {
		t.Fatalf("got %d registers, table has %d", len(regs), len(registers))
	}
	for i, r := range regs {
		if i > 0 && regs[i-1].Name >= r.Name {
			t.Errorf("not sorted at %q", r.Name)
		}
		if got, ok := LookupRegister(r.Name); !ok || got != r {
			t.Errorf("LookupRegister(%q) disagrees with enumeration", r.Name)
		}
	}
}

func TestDirectivesEnumeratesTable(t *testing.T) {
	ds := Directives()
	if len(ds) != len(directiveList) {
		t.Fatalf("got %d directives, table has %d", len(ds), len(directiveList))
	}
	for _, d := range ds {
		if got, ok := LookupDirective(d.Name); !ok || got != d {
			t.Errorf("LookupDirective(%q) disagrees with enumeration", d.Name)
		}
	}
}

func TestPrefixesEnumeratesSet(t *testing.T) {
	ps := Prefixes()
	if len(ps) != len(prefixes) || !sort.StringsAreSorted(ps) {
		t.Fatalf("Prefixes() = %v", ps)
	}
	for _, p := range ps {
		if !IsPrefix(p) {
			t.Errorf("IsPrefix(%q) = false", p)
		}
	}
}
