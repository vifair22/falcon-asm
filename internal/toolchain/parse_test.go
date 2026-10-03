package toolchain

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseLineMap(t *testing.T) {
	out := strings.Join([]string{
		`# 0 "/p/t.S"`,                           // 0
		`# 0 "<built-in>"`,                       // 1
		`#define __x86_64__ 1`,                   // 2
		`# 1 "/usr/include/stdc-predef.h" 1 3 4`, // 3
		`# 0 "<command-line>" 2`,                 // 4
		`# 1 "/p/t.S"`,                           // 5
		``,                                       // 6  t.S:0
		`#define X 5`,                            // 7  t.S:1
		`.code16`,                                // 8  t.S:2
		`# 1 "inc/a.inc" 1`,                      // 9  include from t.S:3
		` nop`,                                   // 10 a.inc:0
		`# 1 "inc/b.inc" 1`,                      // 11 include from a.inc:1
		` hlt`,                                   // 12 b.inc:0
		`# 3 "inc/a.inc" 2`,                      // 13
		` ret`,                                   // 14 a.inc:2
		`# 5 "/p/t.S" 2`,                         // 15
		` movw $5, %ax`,                          // 16 t.S:4
		`# 9 "/p/t\"q.S"`,                        // 17 escaped quote in name
		`x`,                                      // 18
		`#line 20 "/p/t.S"`,                      // 19
		`y`,                                      // 20 t.S:19
	}, "\n") + "\n"
	lm := ParseLineMap(out, "/p/t.S", "/p")
	want := map[int]Origin{
		2:  {File: "", Line: -1, Via: -1},
		6:  {File: "/p/t.S", Line: 0, Via: -1},
		7:  {File: "/p/t.S", Line: 1, Via: -1},
		8:  {File: "/p/t.S", Line: 2, Via: -1},
		10: {File: "/p/inc/a.inc", Line: 0, Via: 3},
		12: {File: "/p/inc/b.inc", Line: 0, Via: 3},
		14: {File: "/p/inc/a.inc", Line: 2, Via: 3},
		16: {File: "/p/t.S", Line: 4, Via: -1},
		18: {File: `/p/t"q.S`, Line: 8, Via: -1},
		20: {File: "/p/t.S", Line: 19, Via: -1},
	}
	for i, w := range want {
		if i >= len(lm.Lines) || lm.Lines[i] != w {
			t.Errorf("line %d = %+v, want %+v", i, lm.Lines[i], w)
		}
	}
	if !reflect.DeepEqual(lm.Includes, map[int]bool{3: true}) {
		t.Errorf("Includes = %v", lm.Includes)
	}
	present := lm.Present(out)
	for _, l := range []int{1, 2, 3, 4, 19} {
		if !present[l] {
			t.Errorf("line %d not present", l)
		}
	}
	if present[0] {
		t.Error("blank line 0 counted as present")
	}
	if v, ok := lm.ViaFor("/p/inc/b.inc", 0); !ok || v != 3 {
		t.Errorf("ViaFor = %d %v", v, ok)
	}
	if _, ok := lm.ViaFor("/p/t.S", 2); ok {
		t.Error("ViaFor on a main-file line")
	}
	if got := ParseLineMap("", "/p/t.S", "/p"); len(got.Lines) != 0 {
		t.Error("empty output produced lines")
	}
}

func TestParseMarkerRejects(t *testing.T) {
	for _, l := range []string{"# comment", "#  1 \"x\"", "# 1x \"y\"", "# 1 x", "# 1 \"open", "#define X", "# 12"} {
		if _, _, _, ok := parseMarker(l); ok {
			t.Errorf("parseMarker(%q) accepted", l)
		}
	}
}

func TestLineMapOnRealBoot(t *testing.T) {
	out := readFixture(t, "boot.pp.s")
	main := "/proj/falcon-bios/src/start/boot.S"
	lm := ParseLineMap(out, main, "/proj")
	lines := strings.Split(out, "\n")
	found := false
	for i, l := range lines {
		if strings.TrimSpace(l) == ". = _start + 510" {
			found = true
			if o := lm.Lines[i]; o.File != main || o.Line != 285 {
				t.Errorf("'. = _start + 510' maps to %+v, want boot.S line 286", o)
			}
		}
		if strings.HasPrefix(l, ".macro safe_mbr_hook") {
			if o := lm.Lines[i]; o.File != "/proj/falcon-bios/src/start/biosdefs.h" || o.Via != 38 {
				t.Errorf("macro line maps to %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("fixture lacks the boot signature move")
	}
	if !lm.Includes[38] {
		t.Errorf("include on boot.S:39 not recorded: %v", lm.Includes)
	}
}

func TestSplitFileLine(t *testing.T) {
	tests := []struct {
		in   string
		file string
		line int
		rest string
		ok   bool
	}{
		{"a.S:12: Error: x", "a.S", 12, " Error: x", true},
		{"C:/x/a.S:3: Warning: y", "C:/x/a.S", 3, " Warning: y", true},
		{"a.S:1:5: error: z", "a.S", 1, "5: error: z", true},
		{"a.S: Assembler messages:", "", 0, "", false},
		{"a.S:0: Error: x", "", 0, "", false},
		{":3: x", "", 0, "", false},
		{"a.S:12", "", 0, "", false},
	}
	for _, tt := range tests {
		f, l, r, ok := splitFileLine(tt.in)
		if ok != tt.ok || f != tt.file || l != tt.line || r != tt.rest {
			t.Errorf("splitFileLine(%q) = %q %d %q %v", tt.in, f, l, r, ok)
		}
	}
}

func TestParseGasStderr(t *testing.T) {
	dir := "/proj"
	main := "/proj/t.S"
	lm := &LineMap{Main: main, Lines: []Origin{
		{Via: -1},
		{File: main, Line: 6, Via: -1},
		{File: "/proj/inc.inc", Line: 3, Via: 6},
	}}
	stderr := strings.Join([]string{
		"/tmp/x/pp.s: Assembler messages:",
		"inc.inc:4: Error: no such instruction: `badinc %eax'",
		"/proj/t.S:9: Error: no such instruction: `bogus %ax'",
		"t.S:10: Warning: value 0x12c truncated to 0x2c",
		"t.S:11:  Info: macro invoked from here",
		"/tmp/x/pp.s:2: Error: mapped through the line map",
		"/tmp/x/pp.s:1: Error: on a marker line, dropped",
		"/tmp/x/pp.s:99: Error: past the end, dropped",
		"t.S:12: Fatal error: fatal",
		"t.S:13: Something: unknown kind",
		"",
	}, "\n")
	got := parseGasStderr(stderr, "/tmp/x/pp.s", dir, lm)
	want := []rawDiag{
		{file: "/proj/inc.inc", line: 3, col: -1, sev: protocol.SeverityError, msg: "no such instruction: `badinc %eax'", via: 6},
		{file: main, line: 8, col: -1, sev: protocol.SeverityError, msg: "no such instruction: `bogus %ax'", via: -1},
		{file: main, line: 9, col: -1, sev: protocol.SeverityWarning, msg: "value 0x12c truncated to 0x2c", via: -1},
		{file: main, line: 10, col: -1, sev: protocol.SeverityInformation, msg: "macro invoked from here", via: -1},
		{file: main, line: 6, col: -1, sev: protocol.SeverityError, msg: "mapped through the line map", via: -1},
		{file: main, line: 11, col: -1, sev: protocol.SeverityError, msg: "fatal", via: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseGasStderr =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseCppStderr(t *testing.T) {
	main := "/proj/u.S"
	stderr := strings.Join([]string{
		"In file included from /proj/u.S:3:",
		"inc/a.h:2:10: fatal error: nope.h: No such file or directory",
		"    2 | #include \"nope.h\"",
		"      |          ^~~~~~~~",
		"compilation terminated.",
		"In file included from inc/b.h:1,",
		"                 from u.S:7:",
		"inc/c.h:5:2: error: #error boom",
		"u.S:9:2: warning: #warning careful",
		"u.S:10: note: no column",
		"<command-line>: fatal error: pre.h: No such file or directory",
		"cc1: warning: something global",
		"some random line: with colon space",
		"",
	}, "\n")
	got := parseCppStderr(stderr, main, "/proj")
	want := []rawDiag{
		{file: "/proj/inc/a.h", line: 1, col: 9, sev: protocol.SeverityError, msg: "nope.h: No such file or directory", via: 2},
		{file: "/proj/inc/c.h", line: 4, col: 1, sev: protocol.SeverityError, msg: "#error boom", via: 6},
		{file: main, line: 8, col: 1, sev: protocol.SeverityWarning, msg: "#warning careful", via: -1},
		{file: main, line: 9, col: -1, sev: protocol.SeverityInformation, msg: "no column", via: -1},
		{file: main, line: 0, col: -1, sev: protocol.SeverityError, msg: "pre.h: No such file or directory", via: -1},
		{file: main, line: 0, col: -1, sev: protocol.SeverityWarning, msg: "something global", via: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseCppStderr =\n%+v\nwant\n%+v", got, want)
	}
}

func TestToDiagnostics(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "t.S")
	inc := filepath.Join(dir, "a.inc")
	writeFile(t, main, "nop\n\t#include \"a.inc\"\n  bogus %ax\n")
	writeFile(t, inc, "x\n\tbad\n")
	raw := []rawDiag{
		{file: main, line: 2, col: -1, sev: protocol.SeverityError, msg: "m", via: -1},
		{file: main, line: 2, col: -1, sev: protocol.SeverityError, msg: "m", via: -1}, // duplicate
		{file: inc, line: 1, col: -1, sev: protocol.SeverityError, msg: "e", via: 1},
		{file: main, line: 0, col: 1, sev: protocol.SeverityWarning, msg: "c", via: -1},
		{file: main, line: -1, col: 99, sev: protocol.SeverityWarning, msg: "neg", via: -1},
		{file: filepath.Join(dir, "gone.h"), line: 4, col: -1, sev: protocol.SeverityError, msg: "g", via: -1},
	}
	got := toDiagnostics(raw, main)
	r := func(l, s, e int) protocol.Range {
		return protocol.Range{Start: protocol.Position{Line: l, Character: s}, End: protocol.Position{Line: l, Character: e}}
	}
	wantMain := []protocol.Diagnostic{
		{Range: r(2, 2, 11), Severity: protocol.SeverityError, Source: Source, Message: "m"},
		{Range: r(1, 1, 17), Severity: protocol.SeverityError, Source: Source, Message: "a.inc:2: e"},
		{Range: r(0, 1, 3), Severity: protocol.SeverityWarning, Source: Source, Message: "c"},
		{Range: r(0, 0, 3), Severity: protocol.SeverityWarning, Source: Source, Message: "neg"},
	}
	if !reflect.DeepEqual(got[main], wantMain) {
		t.Errorf("main diags =\n%+v\nwant\n%+v", got[main], wantMain)
	}
	if d := got[inc]; len(d) != 1 || d[0].Range != r(1, 1, 4) {
		t.Errorf("inc diags = %+v", d)
	}
	if d := got[filepath.Join(dir, "gone.h")]; len(d) != 1 || d[0].Range != r(4, 0, 0) {
		t.Errorf("unreadable file diags = %+v", d)
	}
	if toDiagnostics(nil, main) != nil {
		t.Error("no raw diags should give nil")
	}
}

func TestParseGasListingSections(t *testing.T) {
	rows := parseGasListing(readFixture(t, "sections.lst"))
	src := readFixture(t, "sections.s")
	lm := identityLines("/p/s.s", src)
	got := buildListing(rows, lm)
	type exp struct {
		line    int
		off     uint64
		has     bool
		section string
		bytes   string
	}
	want := []exp{
		{0, 0, false, ".text", ""},
		{1, 0, true, ".text", "90"},
		{3, 0, true, ".data", "01000000"},
		{5, 0, true, ".bss", "00000000000000000000000000000000"},
		{7, 0, true, ".rodata", "01"},
		{9, 1, true, ".text", "000000000000000000000000000000"},
		{10, 0x10, true, ".text", "c3"},
		{12, 0x11, true, ".text", "48b88877665544332211"},
		{14, 0, true, ".foo", "09"},
		{16, 0x1b, true, ".text", "90"},
		{18, 1, true, ".rodata", "07"},
		{20, 2, true, ".rodata", "08"},
	}
	byLine := map[int]ListingLine{}
	for _, l := range got {
		byLine[l.Line] = l
	}
	for _, w := range want {
		l, ok := byLine[w.line]
		if !ok {
			t.Errorf("line %d missing", w.line)
			continue
		}
		if l.HasOffset != w.has || l.Offset != w.off || l.Section != w.section || hexOf(l.Bytes) != w.bytes {
			t.Errorf("line %d = off %#x has %v sec %s bytes %s; want %+v", w.line, l.Offset, l.HasOffset, l.Section, hexOf(l.Bytes), w)
		}
	}
	if n, ok := PadBytes(got, 9); !ok || n != 15 {
		t.Errorf("PadBytes(.org 0x10) = %d %v, want 15", n, ok)
	}
	if _, ok := PadBytes(got, 1); ok {
		t.Error("PadBytes on a nop line")
	}
	if _, ok := PadBytes(got, 999); ok {
		t.Error("PadBytes on a missing line")
	}
}

func identityLines(file, src string) *LineMap {
	n := strings.Count(src, "\n") + 1
	lm := &LineMap{Main: file}
	for i := 0; i < n; i++ {
		lm.Lines = append(lm.Lines, Origin{File: file, Line: i, Via: -1})
	}
	return lm
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	var buf bytes.Buffer
	for _, c := range b {
		buf.WriteByte(digits[c>>4])
		buf.WriteByte(digits[c&15])
	}
	return buf.String()
}

func TestParseGasListingMacros(t *testing.T) {
	rows := parseGasListing(readFixture(t, "macros.lst"))
	var exp int
	for _, r := range rows {
		if r.expansion {
			exp++
		}
	}
	if exp != 8 {
		t.Errorf("expansion rows = %d, want 8", exp)
	}
	got := buildListing(rows, identityLines("/p/m.s", readFixture(t, "macros.s")))
	byLine := map[int]ListingLine{}
	for _, l := range got {
		byLine[l.Line] = l
	}
	// Line 9 (`m`) emits nop and ret into .text and a byte into .data; the
	// .data byte is not part of the .text run.
	if l := byLine[8]; l.Section != ".text" || l.Offset != 0 || hexOf(l.Bytes) != "90c3" || l.Source != "m" {
		t.Errorf("macro line = %+v", l)
	}
	if l := byLine[11]; hexOf(l.Bytes) != "909090" || l.Offset != 2 {
		t.Errorf(".endr line = %+v", l)
	}
	if l := byLine[12]; l.Offset != 5 || l.Section != ".text" {
		t.Errorf("trailing nop = %+v", l)
	}
}

func TestBootListingFixture(t *testing.T) {
	out := readFixture(t, "boot.pp.s")
	main := "/proj/falcon-bios/src/start/boot.S"
	lm := ParseLineMap(out, main, "/proj")
	got := buildListing(parseGasListing(readFixture(t, "boot.lst")), lm)
	byLine := map[int]ListingLine{}
	for _, l := range got {
		byLine[l.Line] = l
	}
	// boot.S:59 `cli` is the first byte of the sector.
	if l := byLine[58]; !l.HasOffset || l.Offset != 0 || hexOf(l.Bytes) != "fa" || l.Section != ".text.stage1" {
		t.Errorf("cli = %+v", l)
	}
	if n, ok := PadBytes(got, 285); !ok || n != 70 {
		t.Errorf("PadBytes(. = _start + 510) = %d %v, want 70", n, ok)
	}
	if n, ok := PadBytes(got, 269); !ok || n != 0x1b2-0x19d {
		t.Errorf("PadBytes(.org 0x1B2) = %d %v", n, ok)
	}
	if n, ok := PadBytes(got, 272); !ok || n != 0 {
		t.Errorf("PadBytes(.org 0x1B6) = %d %v, want 0", n, ok)
	}
	// The signature line is last in the section: listing alone cannot close
	// it, section sizes can.
	sig := byLine[286]
	if sig.Offset != 0x1fe || hexOf(sig.Bytes) != "55aa" {
		t.Errorf("signature = %+v", sig)
	}
	// Truncated fill: gas lists 20 of the 70 fill bytes.
	if l := byLine[285]; len(l.Bytes) != 20 {
		t.Errorf("fill bytes listed = %d", len(l.Bytes))
	}
	last := []ListingLine{{Line: 0, HasOffset: true, Offset: 0x10, Section: ".x", Source: ".org 0x20"}}
	if _, ok := PadBytes(last, 0); ok {
		t.Error("last move without sizes should be unknown")
	}
	if n, ok := padBytes(last, 0, map[string]uint64{".x": 0x20}); !ok || n != 0x10 {
		t.Errorf("padBytes with sizes = %d %v", n, ok)
	}
}

func TestParseGasListingOddRows(t *testing.T) {
	text := "\fGAS LISTING x.s \t\t\tpage 1\n\n\n" +
		"   1 0000 ZZ       \tbad bytes\n" +
		"   1      ZZ\n" +
		"   2      0102\n" + // continuation without a parent row
		"****  Error: something\n" +
		"   3 0000 90       \t>\n" +
		"   4 zzzz          \tnot an address\n"
	rows := parseGasListing(text)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].bytes != nil || !rows[0].hasAddr {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if !rows[1].expansion || rows[1].source != "" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if rows[2].hasAddr {
		t.Errorf("row 2 = %+v", rows[2])
	}
}

func TestSectionTracker(t *testing.T) {
	tr := newSectionTracker()
	steps := []struct{ src, want string }{
		{"\t.data", ".data"},
		{`lbl: .section ".rodata.str", "aMS"`, ".rodata.str"},
		{".previous", ".data"},
		{".previous", ".rodata.str"},
		{".pushsection .x,\"a\"", ".x"},
		{".text 1", ".text"},
		{".popsection", ".rodata.str"},
		{".previous", ".data"},
		{".popsection", ".data"},
		{`.ascii "x;.bss" # .text`, ".data"},
		{"nop; .bss", ".bss"},
		{".section", ".bss"},
		{`.section "unterminated`, ".bss"},
		{".BSS", ".bss"},
	}
	for _, s := range steps {
		tr.apply(s.src)
		if tr.cur != s.want {
			t.Errorf("after %q cur = %q, want %q", s.src, tr.cur, s.want)
		}
	}
}

func TestIsLocationMove(t *testing.T) {
	yes := []string{".org 0x10", "\t.org\t_start + 440", ". = _start + 510", ".=0x200", "x: . = 4", "nop; .org 4"}
	no := []string{".orgx 1", ". == 1", "nop", ".long 0", "", "# .org 4", ".byte '.'"}
	for _, s := range yes {
		if !isLocationMove(s) {
			t.Errorf("%q not a location move", s)
		}
	}
	for _, s := range no {
		if isLocationMove(s) {
			t.Errorf("%q is a location move", s)
		}
	}
}

func TestParseDefines(t *testing.T) {
	out := "#define A 1\n#define F(x,y) ((x)+(y))\n#define EMPTY\n#define G() 2\n#define BAD(x\n#undef Z\n#define 9X 1\n"
	got := ParseDefines(out)
	want := map[string]Macro{
		"A":     {Value: "1"},
		"F":     {Value: "((x)+(y))", Function: true, Params: "x,y"},
		"EMPTY": {},
		"G":     {Value: "2", Function: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseDefines = %+v", got)
	}
}

func TestURIRoundTrip(t *testing.T) {
	p := "/a b/c#d.S"
	u := PathToURI(p)
	if u != "file:///a%20b/c%23d.S" {
		t.Errorf("PathToURI = %q", u)
	}
	if back, ok := URIToPath(u); !ok || back != p {
		t.Errorf("URIToPath = %q %v", back, ok)
	}
	if _, ok := URIToPath("untitled:1"); ok {
		t.Error("non-file URI accepted")
	}
	if _, ok := URIToPath("%zz"); ok {
		t.Error("bad URI accepted")
	}
}

func TestLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"a.S": "assembler-with-cpp", "a.sx": "assembler-with-cpp", "a.s": "assembler",
		"a.c": "c", "a.h": "c", "a.cpp": "c++",
	} {
		if got := language(in); got != want {
			t.Errorf("language(%s) = %s", in, got)
		}
	}
}
