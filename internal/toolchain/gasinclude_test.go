package toolchain

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A .include'd file is listed inline by gas, numbered by its own lines;
// its rows must not land on the main file's lines, its section switches
// must still count, and its errors must be mirrored on the .include line.
func TestGasIncludeE2E(t *testing.T) {
	needGCC(t)
	for _, main := range []string{"src/t.S", "src/u.s"} {
		t.Run(filepath.Ext(main), func(t *testing.T) {
			root, db := project(t, map[string]string{
				main: "\t.text\nstart:\n\tnop\n\t.include \"defs.inc\"\n\tfoo 3\n\tret\n\tmovl $1, %eax\n",
				// Seven lines, so its rows 1..7 would otherwise collide with
				// main lines 0..6; it ends in .data, which the main file's
				// following lines are assembled into.
				"inc/defs.inc": "# defs\n\t.macro foo a\n\tmovl $\\a, %eax\n\t.endm\n" +
					"helper:\n\tmovl %eax, %ebx\n\t.data\n",
				"inc/bad.inc": "\tbogus %eax\n",
			}, "gcc -m32 -Wa,-I,inc -c {} -o x.o")
			file := filepath.Join(root, main)
			f := flagsFor(t, db, file)
			if got := f.GasIncludeDirs(); !reflect.DeepEqual(got, []string{filepath.Join(root, "inc")}) {
				t.Fatalf("GasIncludeDirs = %v", got)
			}
			var r Runner
			res, err := r.Assemble(context.Background(), file, f)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Diagnostics) != 0 {
				t.Fatalf("diagnostics: %v", res.Diagnostics)
			}
			got := map[int]string{}
			for _, l := range res.Listing {
				got[l.Line] = l.Section + ":" + strings.TrimSpace(l.Source)
				if l.Line == 0 && len(l.Bytes) > 0 {
					t.Fatalf("an included row landed on main line 0: %+v", l)
				}
			}
			want := map[int]string{
				0: ".text:.text", 1: ".text:start:", 2: ".text:nop", 3: ".text:.include \"defs.inc\"",
				4: ".data:foo 3", 5: ".data:ret", 6: ".data:movl $1, %eax",
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("listing = %v", got)
			}
			for _, l := range res.Listing {
				if l.Line == 4 && (l.Offset != 0 || len(l.Bytes) != 5) {
					t.Fatalf("macro line = %+v", l)
				}
			}
		})
	}
}

func TestGasIncludeErrorMirroredE2E(t *testing.T) {
	needGCC(t)
	root, db := project(t, map[string]string{
		"src/t.s":       "\tnop\n\t.include \"outer.inc\"\n\tnop\n",
		"inc/outer.inc": "\t.include \"bad.inc\"\n",
		"inc/bad.inc":   "\tnop\n\tbogus %eax\n",
	}, "gcc -m32 -Wa,-Iinc -c {} -o x.o")
	file := filepath.Join(root, "src/t.s")
	var r Runner
	res, err := r.Assemble(context.Background(), file, flagsFor(t, db, file))
	if err != nil {
		t.Fatal(err)
	}
	bad := res.Diagnostics[filepath.Join(root, "inc/bad.inc")]
	if len(bad) != 1 || bad[0].Range.Start.Line != 1 {
		t.Fatalf("bad.inc diagnostics = %v (all %v)", bad, res.Diagnostics)
	}
	mainD := res.Diagnostics[file]
	if len(mainD) != 1 || mainD[0].Range.Start.Line != 1 || !strings.HasPrefix(mainD[0].Message, "bad.inc:2: ") {
		t.Fatalf("main diagnostics = %v", mainD)
	}
}

func TestResolveGasInclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cwd.inc"), "")
	writeFile(t, filepath.Join(root, "wa/wa.inc"), "")
	writeFile(t, filepath.Join(root, "src/near.inc"), "")
	writeFile(t, filepath.Join(root, "cc/cc.inc"), "")
	f := Flags{Dir: root, Args: []string{"-Wa,-I", "-Wa,--32,-Iwa", "-Icc"}}
	f.includeDirs, f.quoteDirs = includeDirsFromArgs(f.Args, root)
	from := filepath.Join(root, "src/t.S")
	for name, want := range map[string]string{
		"cwd.inc": "cwd.inc", "wa.inc": "wa/wa.inc", "near.inc": "src/near.inc", "cc.inc": "cc/cc.inc",
	} {
		if p, ok := f.ResolveGasInclude(from, name); !ok || p != filepath.Join(root, want) {
			t.Errorf("%s -> %s %v", name, p, ok)
		}
	}
	if _, ok := f.ResolveGasInclude(from, "none.inc"); ok {
		t.Error("missing file resolved")
	}
	if _, ok := f.ResolveGasInclude(from, ""); ok {
		t.Error("empty name resolved")
	}
	if p, ok := f.ResolveGasInclude(from, filepath.Join(root, "cwd.inc")); !ok || p != filepath.Join(root, "cwd.inc") {
		t.Error("absolute name")
	}
}

func TestMarkIncludedUnit(t *testing.T) {
	// Main resumes at row 3 only where the text matches: the included
	// file's own row 3 ("x") does not end the include.
	rows := []rawListing{
		{num: 1, source: "a"}, {num: 2, source: ".include \"i\""},
		{num: 1, source: "p"}, {num: 2, source: "q"}, {num: 3, source: "x"}, {num: 4, source: "y"},
		{num: 3, source: "b"}, {num: 3, source: "> mac", expansion: true},
		{num: 4, source: ".include \"missing\""}, {num: 5, source: "c"},
	}
	via := markIncluded(rows, []string{"a", ".include \"i\"", "b", ".include \"missing\"", "c"}, nil,
		func(name string) (string, bool) { return "", false })
	var inc []bool
	for _, r := range rows {
		inc = append(inc, r.included)
	}
	want := []bool{false, false, true, true, true, true, false, false, false, false}
	if !reflect.DeepEqual(inc, want) || via != nil {
		t.Fatalf("included = %v via %v", inc, via)
	}
	if name, ok := gasIncludeName("lbl: .INCLUDE\t\"x.inc\" # c"); !ok || name != "x.inc" {
		t.Fatalf("gasIncludeName = %q %v", name, ok)
	}
	if _, ok := gasIncludeName(".include x"); ok {
		t.Fatal("unquoted include")
	}
	if !lineMatches([]string{"\tvery long line"}, 1, "\tvery lo") || lineMatches([]string{"a"}, 2, "a") {
		t.Fatal("lineMatches")
	}
}

func TestSplitFileLineInsane(t *testing.T) {
	if _, _, _, ok := splitFileLine("main.S:4294967291: Error: x"); ok {
		t.Fatal("wrapped line number accepted")
	}
}

func TestExpandLinesE2E(t *testing.T) {
	needGCC(t)
	root, db := project(t, map[string]string{
		"src/t.S": "#include \"defs.h\"\n#define TWICE(x) x; x\n\tTWICE(PUSH)\n#if 0\n\tTWICE(nop)\n#endif\n" +
			"\tmovl $(SEL \\\n\t + 1), %eax\n",
		"src/defs.h": "#define PUSH pushl $SEL\n#define SEL 0x10\n",
	}, "gcc -m32 -c {} -o x.o")
	file := filepath.Join(root, "src/t.S")
	f := flagsFor(t, db, file)
	var r Runner
	ctx := context.Background()
	text := "#include \"defs.h\"\n#define TWICE(x) x; x\n\tTWICE(PUSH)\n#if 0\n\tTWICE(nop)\n#endif\n" +
		"\tmovl $(SEL \\\n\t + 1), %eax\n"
	got, err := r.ExpandLines(ctx, file, f, text, 2, 2)
	if err != nil || strings.TrimSpace(got) != "pushl $0x10; pushl $0x10" {
		t.Fatalf("expand = %q, %v", got, err)
	}
	got, err = r.ExpandLines(ctx, file, f, text, 6, 7)
	if err != nil || !strings.Contains(got, "$(0x10") {
		t.Fatalf("continuation = %q, %v", got, err)
	}
	if _, err := r.ExpandLines(ctx, file, f, text, 4, 4); !errors.Is(err, ErrNotExpanded) {
		t.Fatalf("inactive line: %v", err)
	}
	if _, err := r.ExpandLines(ctx, file, f, text, 5, 99); err == nil {
		t.Fatal("out of range accepted")
	}
	if _, err := r.ExpandLines(ctx, file, Flags{Compiler: "no-such-cc-xyz"}, text, 2, 2); !errors.Is(err, ErrNoToolchain) {
		t.Fatalf("missing compiler: %v", err)
	}
	if _, err := between("x"); !errors.Is(err, ErrNotExpanded) {
		t.Fatal("between without markers")
	}
	if _, err := between(expandBegin + " x"); !errors.Is(err, ErrNotExpanded) {
		t.Fatal("between without end")
	}
}
