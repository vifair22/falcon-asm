package hw

import (
	"encoding/json"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

var gdtRules = []string{"far-jump-mode", "selector-type", "descriptor-invalid", "gdt-limit"}

// The real files are clean: no rule of this package reports on them.
func TestAiriesFixturesClean(t *testing.T) {
	all := append(append([]string{}, gdtRules...), "pte-reserved", "sipi-trampoline", "sipi-unproven", "bios-struct",
		"msr-reserved-write", "cr-reserved-write")
	for _, f := range []string{"pmode.S", "rthunk.S", "idt.S", "boot.S"} {
		if got := run(t, fixture(t, f), opts{}, all...); len(got) != 0 {
			t.Errorf("%s: %v", f, got)
		}
	}
}

func TestPmodeIndex(t *testing.T) {
	ix := NewIndex(prog(fixture(t, "pmode.S"), asm.Mode16))
	g := ix.GDT()
	if g == nil || g.Label != "gdt" || !g.LimitKnown || g.Limit != 39 || len(g.Entries) != 5 {
		t.Fatalf("gdt = %+v", g)
	}
	if len(ix.fjs) != 1 || !ix.fjs[0].protected || ix.fjs[0].sel != 8 || ix.fjs[0].target < 0 {
		t.Fatalf("far jumps = %+v", ix.fjs)
	}
	sregs := map[string]bool{}
	for _, u := range ix.uses {
		sregs[u.sreg] = true
	}
	if len(sregs) != 6 {
		t.Fatalf("selector uses = %+v", ix.uses)
	}
}

// Mutations of the pmode fixture, one bug each.
func TestGDTRules(t *testing.T) {
	base := fixture(t, "pmode.S")
	cases := []struct {
		name, from, to, code, msg string
	}{
		{"32-bit selector, 16-bit target", "\t.code32\npmode_32:", "\t.code16\npmode_32:", "far-jump-mode", "assembled as .code16"},
		{"16-bit selector, 32-bit target", "ljmpl   $SEL_CODE32", "ljmpl   $0x18", "far-jump-mode", "16-bit code"},
		{"data selector into CS", "ljmpl   $SEL_CODE32", "ljmpl   $0x10", "selector-type", "needs a code descriptor"},
		{"code selector into SS", "movw    $SEL_DATA32, %ax", "movw    $SEL_CODE32, %ax", "selector-type", "SS needs a writable data"},
		{"beyond the limit", "ljmpl   $SEL_CODE32", "ljmpl   $0x30", "selector-type", "beyond the GDT limit"},
		{"limit is the size", "gdt_end - gdt - 1", "gdt_end - gdt", "gdt-limit", "should be 0x27"},
		{"odd limit", "gdt_end - gdt - 1", "gdt_end - gdt - 3", "gdt-limit", "not a multiple of 8"},
		{"L and D", "0x008F9A000000FFFF", "0x00EF9A000000FFFF", "descriptor-invalid", "L and D both set"},
		{"not present but used", "0x00CF92000000FFFF", "0x00CF12000000FFFF", "descriptor-invalid", "not present"},
		{"execute-only code into DS", "0x008F9A000000FFFF   /* code16 */", "0x00CF98000000FFFF", "", ""},
	}
	for _, c := range cases {
		text := strings.Replace(base, c.from, c.to, 1)
		if text == base {
			t.Fatalf("%s: mutation did not apply", c.name)
		}
		got := run(t, text, opts{}, gdtRules...)
		if c.code == "" {
			if len(got) != 0 {
				t.Errorf("%s: %v", c.name, got)
			}
			continue
		}
		f := mustHave(t, got, c.code, c.msg)
		if c.code == "far-jump-mode" && len(f.Related) != 1 {
			t.Errorf("%s: related = %+v", c.name, f.Related)
		}
	}
	// An execute-only code segment loaded into DS faults.
	text := strings.Replace(base, "movw    $SEL_DATA32, %ax", "movw    $0x08, %ax", 1)
	text = strings.Replace(text, "0x00CF9A000000FFFF", "0x00CF98000000FFFF", 1)
	mustHave(t, run(t, text, opts{}, gdtRules...), "selector-type", "%ds needs a data or readable code")
	// A zero entry.
	text = strings.Replace(base, "0x008F92000000FFFF   /* data16 */", "0", 1)
	text = strings.Replace(text, "movw    $SEL_DATA32, %ax", "movw    $0x20, %ax", 1)
	mustHave(t, run(t, text, opts{}, gdtRules...), "selector-type", "all zero")
}

// A real-mode far jump names a segment: never read as a selector.
func TestRealModeFarJump(t *testing.T) {
	text := fixture(t, "pmode.S") + "\t.code16\nreal:\n\tljmp $0x07C0, $real\n\tljmp $0xFFFF, $0\n" +
		"\tmovl %cr0, %eax\n\tandl $0xFFFFFFFE, %eax\n\tmovl %eax, %cr0\n\tljmp $0x1000, $real\n" +
		"\t.code32\nback:\n\tmovl %cr0, %eax\n\tandl $0xFFFFFFFE, %eax\n\tmovl %eax, %cr0\n\tljmp $0x1000, $real\n"
	if got := run(t, text, opts{}, gdtRules...); len(got) != 0 {
		t.Fatal(got)
	}
}

// Two different GDTs loaded: selectors are not resolved.
func TestTwoGDTs(t *testing.T) {
	text := strings.Replace(fixture(t, "pmode.S"), "ljmpl   $SEL_CODE32", "ljmpl   $0x10", 1) +
		"\tlgdt d2\ng2:\t.quad 0\ng2e:\nd2:\t.word g2e - g2 - 1\n\t.long g2\n"
	for _, f := range run(t, text, opts{}, gdtRules...) {
		if f.Code != "gdt-limit" {
			t.Fatalf("ambiguous GDT checked: %v", f)
		}
	}
}

func TestRthunkTableByShape(t *testing.T) {
	ix := NewIndex(prog(fixture(t, "rthunk.S"), asm.Mode16))
	if ix.GDT() != nil {
		t.Fatal("an installed GDT has no table to resolve against")
	}
	var shaped *DescTable
	for _, tb := range ix.tables {
		if !tb.Loaded {
			shaped = tb
		}
	}
	if shaped == nil || len(shaped.Entries) != 5 {
		t.Fatalf("tables = %+v", ix.tables)
	}
	text := fixture(t, "rthunk.S")
	off := strings.Index(text, "0x00CF92000000FFFF")
	md, _, _ := ix.Hover(hwdb.Default(), off)
	if !strings.Contains(md, "GDT entry 2") || !strings.Contains(md, "data32") {
		t.Fatalf("hover = %q", md)
	}
	// The limit of the installed pseudo-descriptor is still checked.
	bad := strings.Replace(text, "5 * 8 - 1", "5 * 8", 1)
	if got := run(t, bad, opts{}, "gdt-limit"); len(got) != 0 {
		t.Fatalf("no lgdt names it by label: %v", got)
	}
}

func TestIDT(t *testing.T) {
	text := fixture(t, "idt.S")
	ix := NewIndex(prog(text, asm.Mode32))
	md, _, _ := ix.Hover(hwdb.Default(), strings.Index(text, "0x00008E0000081000"))
	if !strings.Contains(md, "IDT vector 0") || !strings.Contains(strings.ToLower(md), "interrupt gate") {
		t.Fatalf("gate hover = %q", md)
	}
	bad := strings.Replace(text, "32 * 8 - 1", "32 * 8", 1)
	mustHave(t, run(t, bad, opts{mode: asm.Mode32}, "gdt-limit"), "gdt-limit", "IDT limit")
	// A null IDT (limit 0) is deliberate.
	null := strings.Replace(text, "32 * 8 - 1", "0", 1)
	if got := run(t, null, opts{mode: asm.Mode32}, "gdt-limit"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestGDTHover(t *testing.T) {
	text := fixture(t, "pmode.S")
	ix := NewIndex(prog(text, asm.Mode16))
	db := hwdb.Default()
	cases := []struct{ at, want string }{
		{"0x00CF9A000000FFFF", "GDT entry 1"},
		{"0x00CF9A000000FFFF", "code32"},
		{"$SEL_CODE32", "Selector 0x8"},
		{"$SEL_DATA32", "data32"},
		{"$((1 << 9)", "OSFXSR"},
		{"$1, %eax             /* CR0.PE", "PE"},
		{"$1, %eax\n\tcpuid", "CPUID"},
	}
	for _, c := range cases {
		off := strings.Index(text, c.at)
		if off < 0 {
			t.Fatalf("%q not in fixture", c.at)
		}
		md, _, _ := ix.Hover(db, off+1)
		if !strings.Contains(md, c.want) {
			t.Errorf("hover at %q = %q, want %q", c.at, md, c.want)
		}
	}
	if md, _, _ := ix.Hover(db, strings.Index(text, "cli")); md != "" {
		t.Errorf("cli hover = %q", md)
	}
	if md, _, _ := ix.Hover(db, len(text)+5); md != "" {
		t.Error("past the end")
	}
}

func TestPortBIOSMSRHover(t *testing.T) {
	text := "\t.code16\n" +
		"\tmovb $0x0E, %ah\n\tmovw $7, %bx\n\tint $0x10\n" + // 1-3
		"\tinb $0x92, %al\n" + // 4
		"\tmovw $0x3F8, %dx\n\toutb %al, %dx\n" + // 5-6
		"\tmovb $0x0A, %al\n\toutb %al, $0x70\n" + // 7-8
		"\tmovl $0xC0000080, %ecx\n\trdmsr\n\torl $0x100, %eax\n\twrmsr\n" + // 9-12
		"\tmovl $7, %eax\n\txorl %ecx, %ecx\n\tcpuid\n" + // 13-15
		"\tint $0x12\n" + // 16
		"\tmovl $0xC0000080, %ecx\n" + // 17 (no consumer)
		"\tmovw $0x3F8, %dx\n" + // 18 (no consumer)
		"\tmovw $0x80, %dx\n\tint $0x13\n" + // 19-20
		"\tmovl $0, %eax\n\tmovl %eax, %cr4\n" + // 21-22
		"\tmovl %cr0, %eax\n\tandl $0x9FFFFFFF, %eax\n\tmovl %eax, %cr0\n" // 23-25
	ix := NewIndex(prog(text, asm.Mode16))
	db := hwdb.Default()
	at := func(line int, sub string) string {
		ls := strings.Split(text, "\n")
		off := 0
		for i := 0; i < line; i++ {
			off += len(ls[i]) + 1
		}
		k := strings.Index(ls[line], sub)
		if k < 0 {
			t.Fatalf("%q not on line %d", sub, line)
		}
		md, _, _ := ix.Hover(db, off+k)
		return md
	}
	checks := []struct {
		line      int
		sub, want string
	}{
		{3, "int", "INT 10h AH=0Eh"},
		{1, "$0x0E", "INT 10h AH=0Eh"},
		{4, "$0x92", "0x92"},
		{5, "$0x3F8", "COM1"},
		{8, "$0x70", "CMOS"},
		{9, "$0xC0000080", "EFER"},
		{13, "$7", "07h"},
		{16, "int", "INT 12h"},
		{17, "$0xC0000080", "EFER"},
		{18, "$0x3F8", "COM1"},
		{19, "$0x80", ""},
		{21, "$0", "CR4"},
		{24, "$0x9FFFFFFF", "clears"},
	}
	for _, c := range checks {
		md := at(c.line, c.sub)
		if c.want == "" {
			if md != "" && strings.Contains(md, "port") {
				t.Errorf("line %d: unexpected %q", c.line, md)
			}
			continue
		}
		if !strings.Contains(md, c.want) {
			t.Errorf("line %d %q: hover = %q, want %q", c.line, c.sub, md, c.want)
		}
	}
}

func TestMSRAndCRRules(t *testing.T) {
	codes := []string{"msr-reserved-write", "cr-reserved-write"}
	cases := []struct {
		text, code, msg string
	}{
		// EFER bit 0 is SCE; bit 1 is reserved.
		{"\tmovl $0xC0000080, %ecx\n\trdmsr\n\torl $2, %eax\n\twrmsr\n", "cr-reserved-write", "EFER"},
		{"\tmovl $0x277, %ecx\n\tmovl $0x02020202, %eax\n\tmovl $0x02020202, %edx\n\twrmsr\n", "msr-reserved-write", "MSR 0x277"},
		{"\tmovl $0x80000000, %eax\n\tmovl %eax, %cr0\n", "cr-reserved-write", "PG set without PE"},
		{"\tmovl $0, %ecx\n\tmovl $0, %edx\n\tmovl $2, %eax\n\txsetbv\n", "cr-reserved-write", "XCR0"},
		// Values across a label come from the dataflow.
		{"\tmovl $0xC0000080, %ecx\n1:\torl $2, %eax\n\twrmsr\n", "cr-reserved-write", "EFER"},
		{"\tmovl $0, %ecx\n\tmovl $0, %edx\n1:\tmovl $2, %eax\n\txsetbv\n", "cr-reserved-write", "XCR0"},
		{"\tmovl $0x277, %ecx\n\tmovl $0x02020202, %eax\n2:\tmovl $0x02020202, %edx\n\twrmsr\n", "msr-reserved-write", "MSR 0x277"},
	}
	for _, c := range cases {
		mustHave(t, run(t, c.text, opts{mode: asm.Mode32}, codes...), c.code, c.msg)
	}
	clean := []string{
		"\tmovl $0xC0000080, %ecx\n\trdmsr\n\torl $0x100, %eax\n\twrmsr\n",
		"\tmovl $0x1B, %ecx\n\trdmsr\n\torl $0x800, %eax\n\twrmsr\n",
		"\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n",
		// Two paths disagree about ECX: nothing is known at the wrmsr.
		"\ttestl %ebx, %ebx\n\tjz 1f\n\tmovl $0xC0000080, %ecx\n\tjmp 2f\n1:\tmovl $0x1B, %ecx\n2:\torl $2, %eax\n\twrmsr\n",
		"\tmovl $0x80000001, %eax\n\tmovl %eax, %cr0\n",
	}
	for _, c := range clean {
		if got := run(t, c, opts{mode: asm.Mode32}, codes...); len(got) != 0 {
			t.Errorf("%q: %v", c, got)
		}
	}
	// Without an MSR-checking table the MSR rule is quiet.
	if got := run(t, cases[1].text, opts{mode: asm.Mode32, tables: fakeTables{}}, codes...); len(got) != 0 {
		t.Fatal(got)
	}
}

type fakeTables struct{}

func (fakeTables) Port(uint64) (string, string, bool)         { return "", "", false }
func (fakeTables) MSR(uint64) (string, string, bool)          { return "", "", false }
func (fakeTables) BIOS(uint64, uint64) (string, string, bool) { return "", "", false }

func TestPageTables(t *testing.T) {
	text := "\t.code32\n\tmovl $pml4, %eax\n\tmovl %eax, %cr3\n\t.code64\n\tnop\n\t.balign 4096\n" +
		"pml4:\t.quad pdpt + 3\n\t.fill 511, 8, 0\n\t.balign 4096\n" +
		"pdpt:\t.quad pd + 3\n\t.balign 4096\n" +
		"pd:\t.quad 0x83\n\t.quad 0x200083 | (1 << 13)\n"
	got := run(t, text, opts{mode: asm.Mode32}, "pte-reserved")
	if codesOf(got) != "pte-reserved@"+itoaLine(lineOf(text, "0x200083")) {
		t.Fatalf("findings = %v", got)
	}
	ix := NewIndex(prog(text, asm.Mode32))
	md, _, _ := ix.Hover(hwdb.Default(), strings.Index(text, "pdpt + 3"))
	if !strings.Contains(md, "pml4e") || !strings.Contains(md, "pdpt") {
		t.Fatalf("pml4e hover = %q", md)
	}
	md, _, _ = ix.Hover(hwdb.Default(), strings.Index(text, "0x83"))
	if !strings.Contains(md, "pde") || !strings.Contains(md, "2 MiB") {
		t.Fatalf("pde hover = %q", md)
	}
	// No 64-bit code: PAE or 4-level. The third level is a PDE under
	// 4-level paging but a PTE under PAE, where bit 13 is address, and the
	// PDPTE's RW is reserved only under PAE: only bits reserved under both
	// count, so nothing is reported, and the hover offers both readings.
	pae := strings.Replace(text, "\t.code64\n", "", 1)
	if got := run(t, pae, opts{mode: asm.Mode32}, "pte-reserved"); len(got) != 0 {
		t.Fatalf("PAE ambiguity: %v", got)
	}
	ix = NewIndex(prog(pae, asm.Mode32))
	md, _, _ = ix.Hover(hwdb.Default(), strings.Index(pae, "pd + 3"))
	if !strings.Contains(md, "PAE") {
		t.Fatalf("ambiguous hover = %q", md)
	}
	// 32-bit paging with .long entries.
	p32 := "\tmovl $pdir, %eax\n\tmovl %eax, %cr3\npdir:\t.long ptab + 3\nptab:\t.long 0x1003\n"
	ix = NewIndex(prog(p32, asm.Mode32))
	md, _, _ = ix.Hover(hwdb.Default(), strings.Index(p32, "0x1003"))
	if !strings.Contains(md, "pte32") {
		t.Fatalf("pte32 hover = %q", md)
	}
}

func TestSIPI(t *testing.T) {
	icr := "\t.code32\n\tmovl $(0x4600 | (ap_start >> 12)), 0xFEE00300\n"
	bad := icr + "\t.code32\nap_start:\n\tcli\n"
	mustHave(t, run(t, bad, opts{mode: asm.Mode32}, "sipi-trampoline"), "sipi-trampoline", ".code32")
	good := icr + "\t.balign 4096\n\t.code16\nap_start:\n\tcli\n"
	if got := run(t, good, opts{mode: asm.Mode32}, "sipi-trampoline", "sipi-unproven"); len(got) != 0 {
		t.Fatal(got)
	}
	// Delivery mode INIT (101b) is not a SIPI.
	initIPI := "\t.code32\n\tmovl $(0x4500 | (x >> 12)), 0xFEE00300\nx:\n\tcli\n"
	if got := run(t, initIPI, opts{mode: asm.Mode32}, "sipi-trampoline"); len(got) != 0 {
		t.Fatal(got)
	}
	// Listing: a trampoline at offset 0x120 of its section.
	text := "\t.code16\n\tnop\n\t# falcon-asm: trampoline\nap_start:\n\tcli\n"
	ls := &toolchain.AsmResult{Listing: []toolchain.ListingLine{
		{Line: 1, Offset: 0, HasOffset: true, Section: ".text", Bytes: []byte{0x90}},
		{Line: 4, Offset: 0x120, HasOffset: true, Section: ".text", Bytes: []byte{0xFA}},
	}}
	mustHave(t, run(t, text, opts{listing: ls}, "sipi-alignment"), "sipi-alignment", "0x120")
	ls.Listing[1].Offset = 0x1000
	if got := run(t, text, opts{listing: ls}, "sipi-alignment"); len(got) != 0 {
		t.Fatal(got)
	}
}

// listingFor builds a listing from per-line sizes, as gas would lay out a
// single section.
func listingFor(text string, sizes map[int]int, sigLine int, sig []byte) *toolchain.AsmResult {
	res := &toolchain.AsmResult{}
	off := uint64(0)
	n := strings.Count(text, "\n")
	for l := 0; l < n; l++ {
		sz, ok := sizes[l]
		if !ok {
			continue
		}
		b := make([]byte, sz)
		if l == sigLine {
			b = sig
		}
		res.Listing = append(res.Listing, toolchain.ListingLine{Line: l, Offset: off, HasOffset: sz > 0, Section: ".text", Bytes: b})
		off += uint64(sz)
	}
	return res
}

func TestBootSignature(t *testing.T) {
	good := "_start:\n\tcli\n\t. = _start + 510\n\t.byte 0x55, 0xAA\n"
	ls := listingFor(good, map[int]int{1: 1, 2: 509, 3: 2}, 3, []byte{0x55, 0xAA})
	if got := run(t, good, opts{listing: ls}, "boot-signature"); len(got) != 0 {
		t.Fatal(got)
	}
	swapped := strings.Replace(good, ".byte 0x55, 0xAA", ".word 0x55AA", 1)
	mustHave(t, run(t, swapped, opts{listing: ls}, "boot-signature"), "boot-signature", "little-endian")
	other := strings.Replace(good, ".byte 0x55, 0xAA", ".byte 0x12, 0x34", 1)
	mustHave(t, run(t, other, opts{listing: ls}, "boot-signature"), "boot-signature", "12 34")
	none := "_start:\n\tcli\n\t.org 510\n"
	mustHave(t, run(t, none, opts{listing: ls}, "boot-signature"), "boot-signature", "no signature")
	// A macro the parse cannot evaluate: the listing has the bytes.
	macro := strings.Replace(good, ".byte 0x55, 0xAA", ".word BOOT_SIG", 1)
	if got := run(t, macro, opts{listing: ls}, "boot-signature"); len(got) != 0 {
		t.Fatal(got)
	}
	ls2 := listingFor(macro, map[int]int{1: 1, 2: 509, 3: 2}, 3, []byte{0xAA, 0x55})
	mustHave(t, run(t, macro, opts{listing: ls2}, "boot-signature"), "boot-signature", "little-endian")
	// Not a boot sector: nothing to say.
	if got := run(t, "\tnop\n\t.org 511\n\t.byte 0\n", opts{listing: ls}, "boot-signature"); len(got) != 0 {
		t.Fatal(got)
	}
	// Signature not at 510 (start label is not at the section start).
	off := "\tnop\n_start:\n\tcli\n\t.org 510\n\t.byte 0x55, 0xAA\n"
	ls3 := listingFor(off, map[int]int{0: 1, 2: 1, 3: 508, 4: 2}, 4, []byte{0x55, 0xAA})
	if got := run(t, off, opts{listing: ls3}, "boot-signature"); len(got) != 0 {
		t.Fatalf(".org is section-relative: %v", got)
	}
	sym := "\tnop\n_start:\n\tcli\n\t.org _start + 510\n\t.byte 0x55, 0xAA\n"
	ls4 := listingFor(sym, map[int]int{0: 1, 2: 1, 3: 508, 4: 2}, 4, []byte{0x55, 0xAA})
	mustHave(t, run(t, sym, opts{listing: ls4}, "boot-signature"), "boot-signature", "offset 509")
}

func TestMBROverlap(t *testing.T) {
	r, _ := lint.Lookup("mbr-partition-overlap")
	bad := r.Doc.Bad + "\n"
	ls := listingFor(bad, map[int]int{2: 450, 3: 60, 4: 2}, 4, []byte{0x55, 0xAA})
	f := mustHave(t, run(t, bad, opts{listing: ls}, "mbr-partition-overlap"), "mbr-partition-overlap", "446-449")
	if f.Range.Start.Line != 2 {
		t.Fatalf("range %+v", f.Range)
	}
	good := r.Doc.Good + "\n"
	ls = listingFor(good, map[int]int{2: 400, 3: 110, 4: 2}, 4, []byte{0x55, 0xAA})
	if got := run(t, good, opts{listing: ls}, "mbr-partition-overlap"); len(got) != 0 {
		t.Fatal(got)
	}
	// boot.S: 440 marker, then the move to 510.
	text := fixture(t, "boot.S")
	sizes := map[int]int{}
	for l := lineOf(text, "cli"); l < lineOf(text, ".org    _start + 440"); l++ {
		sizes[l] = 4
	}
	sizes[lineOf(text, ". = _start")] = 0
	ls = listingFor(text, sizes, -1, nil)
	// Pad the move: offsets from the listing rows.
	end := ls.Listing[len(ls.Listing)-1].Offset + 4
	ls.Listing = append(ls.Listing, toolchain.ListingLine{Line: lineOf(text, ". = _start"), Offset: end, HasOffset: true, Section: ".text.stage1"},
		toolchain.ListingLine{Line: lineOf(text, "0x55, 0xAA"), Offset: 510, HasOffset: true, Section: ".text.stage1", Bytes: []byte{0x55, 0xAA}})
	for k := range ls.Listing {
		ls.Listing[k].Section = ".text.stage1"
	}
	if got := run(t, text, opts{listing: ls}, "mbr-partition-overlap", "boot-signature"); len(got) != 0 {
		t.Fatal(got)
	}
	// Data placed after an explicit move to 446 is the partition table.
	pt := "_start:\n\tcli\n\t.org _start + 446\n\t.fill 64, 1, 0\n\t. = _start + 510\n\t.byte 0x55, 0xAA\n"
	ls = listingFor(pt, map[int]int{1: 1, 2: 445, 3: 64, 4: 0, 5: 2}, 5, []byte{0x55, 0xAA})
	if got := run(t, pt, opts{listing: ls}, "mbr-partition-overlap"); len(got) != 0 {
		t.Fatal(got)
	}
	// Not an MBR (no 440/446 marker, no comment): a VBR may use the area.
	vbr := "_start:\n\t.fill 450, 1, 0x90\n\t. = _start + 510\n\t.byte 0x55, 0xAA\n"
	ls = listingFor(vbr, map[int]int{1: 450, 2: 60, 3: 2}, 3, []byte{0x55, 0xAA})
	if got := run(t, vbr, opts{listing: ls}, "mbr-partition-overlap"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestBIOSStruct(t *testing.T) {
	text := fixture(t, "boot.S")
	if got := run(t, text, opts{}, "bios-struct"); len(got) != 0 {
		t.Fatal(got)
	}
	bad := strings.Replace(text, ".byte   16 ", ".byte   20 ", 1)
	mustHave(t, run(t, bad, opts{}, "bios-struct"), "bios-struct", "size (offset 0) is 0x14")
	res := strings.Replace(text, ".byte   0\n", ".byte   1\n", 1)
	mustHave(t, run(t, res, opts{}, "bios-struct"), "bios-struct", "reserved (offset 1)")
	// A packet cut short by the next label.
	short := "\tmovw $dap, %si\n\tmovb $0x42, %ah\n\tint $0x13\ndap:\t.byte 0x10, 0\n\t.word 1\nnext:\t.word 0\n"
	mustHave(t, run(t, short, opts{}, "bios-struct"), "bios-struct", "only 4 are laid out")
	// E820 buffer, through movl $0xE820, %eax and ES:DI.
	e820 := "1:\tmovl $0xE820, %eax\n\tmovl $24, %ecx\n\tmovw $ent, %di\n\tmovl $0, ent + 16\n\tint $0x15\nent:\t.space 16\n"
	mustHave(t, run(t, e820, opts{}, "bios-struct"), "bios-struct", "16 bytes laid out")
	ok := strings.Replace(e820, ".space 16", ".space 24", 1)
	if got := run(t, ok, opts{}, "bios-struct"); len(got) != 0 {
		t.Fatal(got)
	}
	// Marked by a comment.
	marked := "\t# falcon-asm: struct=dap\npkt:\t.byte 0x11, 0\n\t.word 1, 0, 0\n\t.quad 0\n"
	mustHave(t, run(t, marked, opts{}, "bios-struct"), "bios-struct", "0x11")
	// An unknown structure name, and tables that are not hwdb's.
	if got := run(t, strings.Replace(marked, "dap", "nosuch", 1), opts{}, "bios-struct"); len(got) != 0 {
		t.Fatal(got)
	}
	if got := run(t, marked, opts{tables: fakeTables{}}, "bios-struct"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestLayoutEval(t *testing.T) {
	text := "#define N 4\n\t.set K, 2\na:\t.byte 1, 2\n\t.word a2 - a\n\t.ascii \"x\\n\\x41\\101\"\n\t.asciz \"y\"\n" +
		"\t.space N\n\t.fill 2, 2, 0x1234\n\t.zero 1\na2:\t.long a2 - a + K\n\t.quad (b + 3)\n\t.quad 4 + b\n\t.quad b | 0x80\n" +
		"\t.long b - 1\nb:\n\t.balign 4\nc:\t.byte 'q'\n"
	p := prog(text, asm.Mode32)
	p.Macros = map[string]string{"N": "4"}
	l := newLayout(p)
	es := l.elemsFrom("a", 100)
	var vals []string
	for _, e := range es {
		if e.Known {
			vals = append(vals, itoaLine(int(e.Val)))
		} else {
			vals = append(vals, e.Sym+"+"+itoaLine(int(e.Addend)))
		}
	}
	got := strings.Join(vals, ",")
	want := "1,2,19,120,10,65,65,121,0,0,0,0,0,4660,4660,0,21,b+3,b+4,b+128,b+-"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("elements = %s", got)
	}
	if _, ok := l.eval("b"); ok {
		t.Fatal("a lone label is not a constant")
	}
	if v, ok := l.eval("c - a"); ok {
		t.Fatalf("labels in different runs: %d", v)
	}
	if _, ok := unquote("\"\\q\""); ok {
		t.Fatal("bad escape accepted")
	}
	if b, ok := unquote("\"\\t\\r\\b\\f\\\\\\\"\""); !ok || string(b) != "\t\r\b\f\\\"" {
		t.Fatalf("escapes = %q", b)
	}
}

func TestTablesLoader(t *testing.T) {
	tb, err := loadTables(json.RawMessage(`{"ports":[{"port":"0x510","name":"fw_cfg selector","widths":[16]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if name, _, ok := tb.Port(0x510); !ok || !strings.Contains(name, "fw_cfg") {
		t.Fatalf("override port = %q", name)
	}
	if DB(tb) == hwdb.Default() || DB(nil) != hwdb.Default() || DB(fakeTables{}) != hwdb.Default() {
		t.Fatal("DB")
	}
	if tb, err := loadTables(json.RawMessage(`{"ports":[{"port":"x"}]}`)); err == nil || tb != nil {
		t.Fatalf("bad override: %v %v", tb, err)
	}
	// The registered loader is this one.
	got, err := lint.LoadTables(json.RawMessage(`{"helpers":[{"name":"port_out8","kind":"port","write":true,"value":1}]}`))
	if err != nil || DB(got) == hwdb.Default() {
		t.Fatalf("registered loader: %v %v", got, err)
	}
}
