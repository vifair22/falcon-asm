package symbols

import (
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

const callA = `	.globl _start
_start:
	call setup
	call puts
	jmp main_loop
setup:
	call puts
1:	jmp 1b
	ret
puts:
	lodsb
	jmp puts
	ret
tail:
	call *%eax
	ljmp $0x08, $pm_entry
`

const callB = `	.globl main_loop
main_loop:
	call helper
	call external_c
	jmp main_loop
helper:
	ret
	.globl pm_entry
pm_entry:
	call helper
puts:
	ret
`

func callIndex(t *testing.T) *Index {
	t.Helper()
	ix := NewIndex(nil)
	ix.Update("file:///w/a.S", callA, nil)
	ix.Update("file:///w/b.S", callB, nil)
	return ix
}

func itemNames(items []protocol.CallHierarchyItem) string {
	var s []string
	for _, it := range items {
		s = append(s, it.Name+"@"+filepath.Base(uriKey(it.URI))+":"+itoa(it.SelectionRange.Start.Line))
	}
	return strings.Join(s, " ")
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n/10%10)) + string(rune('0'+n%10)))
}

func TestPrepareCallHierarchy(t *testing.T) {
	ix := callIndex(t)
	// On a call target.
	got := ix.PrepareCallHierarchy("file:///w/a.S", at(t, callA, "setup", 0, 1))
	if itemNames(got) != "setup@a.S:05" {
		t.Fatalf("on reference: %s", itemNames(got))
	}
	if got[0].Range.End.Line != 8 || got[0].Detail != "a.S" {
		t.Fatalf("range/detail = %+v", got[0])
	}
	// Inside a body, not on a symbol: the enclosing function.
	got = ix.PrepareCallHierarchy("file:///w/a.S", at(t, callA, "lodsb", 0, 1))
	if itemNames(got) != "puts@a.S:09" {
		t.Fatalf("enclosing: %s", itemNames(got))
	}
	// A global defined in another file.
	got = ix.PrepareCallHierarchy("file:///w/a.S", at(t, callA, "main_loop", 0, 1))
	if itemNames(got) != "main_loop@b.S:01" || !strings.Contains(got[0].Detail, ".globl") {
		t.Fatalf("cross-file: %+v", got)
	}
	// Before any function there is nothing; unknown documents too.
	if got := ix.PrepareCallHierarchy("file:///w/a.S", protocol.Position{Line: 0, Character: 1}); got != nil {
		t.Fatalf("top: %+v", got)
	}
	if got := ix.PrepareCallHierarchy("file:///w/none.S", protocol.Position{}); got != nil {
		t.Fatalf("unknown doc: %+v", got)
	}
	if got := ix.PrepareCallHierarchy("file:///w/a.S", protocol.Position{Line: 99}); got != nil {
		t.Fatalf("out of range: %+v", got)
	}
}

func TestIncomingCalls(t *testing.T) {
	ix := callIndex(t)
	puts := ix.PrepareCallHierarchy("file:///w/a.S", at(t, callA, "puts:", 0, 1))
	in := ix.IncomingCalls(puts[0])
	var got []string
	for _, c := range in {
		got = append(got, c.From.Name+"/"+itoa(len(c.FromRanges))+c.From.Detail)
	}
	// b.S's private puts is a different symbol, and puts' jump to itself is
	// a loop, not a call.
	if strings.Join(got, " ") != "_start/01a.S .globl setup/01a.S" {
		t.Fatalf("incoming puts = %v", got)
	}
	helper := ix.PrepareCallHierarchy("file:///w/b.S", at(t, callB, "helper:", 0, 1))
	in = ix.IncomingCalls(helper[0])
	if len(in) != 2 || in[0].From.Name != "main_loop" || in[1].From.Name != "pm_entry" {
		t.Fatalf("incoming helper = %+v", in)
	}
	// Tail calls are marked; a far jump counts, attributed to the enclosing
	// function (tail: is neither called nor global, so it is not one).
	ml := ix.PrepareCallHierarchy("file:///w/b.S", at(t, callB, "main_loop:", 0, 1))
	in = ix.IncomingCalls(ml[0])
	if len(in) != 1 || in[0].From.Name != "_start" || !strings.HasSuffix(in[0].From.Detail, "tail call (jmp)") {
		t.Fatalf("incoming main_loop = %+v", in)
	}
	pm := ix.PrepareCallHierarchy("file:///w/b.S", at(t, callB, "pm_entry:", 0, 1))
	in = ix.IncomingCalls(pm[0])
	if len(in) != 1 || in[0].From.Name != "puts" || in[0].From.URI != "file:///w/a.S" {
		t.Fatalf("incoming pm_entry = %+v", in)
	}
}

func TestIncomingTopLevelAndMixed(t *testing.T) {
	ix := NewIndex(nil)
	src := "\tcall f\n\t.globl f\nf:\n\tcall g\n\tjmp g\ng:\n\tret\n"
	ix.Update("file:///w/c.S", src, nil)
	f := ix.PrepareCallHierarchy("file:///w/c.S", at(t, src, "f:", 0, 0))
	in := ix.IncomingCalls(f[0])
	if len(in) != 1 || in[0].From.Kind != protocol.SymbolKindFile || in[0].From.Name != "c.S" {
		t.Fatalf("top-level caller = %+v", in)
	}
	out := ix.OutgoingCalls(f[0])
	if len(out) != 1 || out[0].To.Name != "g" || len(out[0].FromRanges) != 2 ||
		!strings.HasSuffix(out[0].To.Detail, "call and tail call (jmp)") {
		t.Fatalf("outgoing f = %+v", out)
	}
}

func TestOutgoingCalls(t *testing.T) {
	ix := callIndex(t)
	start := ix.PrepareCallHierarchy("file:///w/a.S", at(t, callA, "_start:", 0, 1))
	out := ix.OutgoingCalls(start[0])
	var got []string
	for _, c := range out {
		got = append(got, c.To.Name+"@"+filepath.Base(uriKey(c.To.URI))+c.To.Detail)
	}
	if strings.Join(got, " ") != "setup@a.Sa.S puts@a.Sa.S main_loop@b.Sb.S .globl - tail call (jmp)" {
		t.Fatalf("outgoing _start = %v", got)
	}
	// Unresolvable callees (a C function) are left out.
	ml := ix.PrepareCallHierarchy("file:///w/b.S", at(t, callB, "main_loop:", 0, 1))
	out = ix.OutgoingCalls(ml[0])
	if len(out) != 1 || out[0].To.Name != "helper" {
		t.Fatalf("outgoing main_loop = %+v", out)
	}
	if got := ix.OutgoingCalls(protocol.CallHierarchyItem{URI: "file:///w/none.S"}); got != nil {
		t.Fatalf("unknown = %+v", got)
	}
}

func TestWorkspaceSymbols(t *testing.T) {
	ix := NewIndex(nil)
	ix.Update("file:///w/a.S", "#define STAGE2_LBA 1\n\t.globl stage2_load\nstage2_load:\nstage2:\n1:\n\tSECT = 4\n\tSECT = 5\n\t.macro load_sect\n\t.endm\nunrelated:\n", nil)
	ix.Update("file:///w/b.S", "stage2_loader:\n", nil)
	name := func(q string, limit int) string {
		var s []string
		for _, si := range ix.WorkspaceSymbols(q, limit) {
			s = append(s, si.Name)
		}
		return strings.Join(s, " ")
	}
	if got := name("stage2", 0); got != "stage2 stage2_load STAGE2_LBA stage2_loader" {
		t.Fatalf("stage2 = %q", got)
	}
	if got := name("s2ld", 0); got != "stage2_load stage2_loader" {
		t.Fatalf("subsequence = %q", got)
	}
	if got := name("sect", 0); got != "SECT load_sect" {
		t.Fatalf("equ once, macro = %q", got)
	}
	if got := name("", 2); len(strings.Fields(got)) != 2 {
		t.Fatalf("limit = %q", got)
	}
	if got := name("zzz", 0); got != "" {
		t.Fatalf("no match = %q", got)
	}
	syms := ix.WorkspaceSymbols("stage2_load", 1)
	if syms[0].Kind != protocol.SymbolKindFunction || syms[0].ContainerName != "a.S" || syms[0].Location.Range.Start.Line != 2 {
		t.Fatalf("symbol = %+v", syms[0])
	}
	kinds := map[string]protocol.SymbolKind{}
	for _, s := range ix.WorkspaceSymbols("", 0) {
		kinds[s.Name] = s.Kind
	}
	if kinds["STAGE2_LBA"] != protocol.SymbolKindConstant || kinds["load_sect"] != protocol.SymbolKindNamespace ||
		kinds["SECT"] != protocol.SymbolKindVariable || kinds["unrelated"] != protocol.SymbolKindVariable {
		t.Fatalf("kinds = %v", kinds)
	}
}
