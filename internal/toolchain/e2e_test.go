package toolchain

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func needGCC(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not on PATH")
	}
}

// project lays out files under a temp root with a compile database whose
// .S entries use cmd (with {} replaced by the file), and returns the root
// and the database.
func project(t *testing.T, files map[string]string, cmd string) (string, *DB) {
	t.Helper()
	root := t.TempDir()
	var entries []rawEntry
	for name, content := range files {
		writeFile(t, filepath.Join(root, name), content)
		if ext := filepath.Ext(name); ext == ".S" || ext == ".s" || ext == ".c" {
			entries = append(entries, rawEntry{Directory: root, File: name,
				Command: strings.ReplaceAll(cmd, "{}", name)})
		}
	}
	writeDB(t, filepath.Join(root, DBFile), entries)
	db, err := LoadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, db
}

func flagsFor(t *testing.T, db *DB, file string) Flags {
	t.Helper()
	f, ok := db.Flags(file)
	if !ok {
		t.Fatalf("no flags for %s", file)
	}
	return f
}

func TestMacrosE2E(t *testing.T) {
	needGCC(t)
	root, db := project(t, map[string]string{
		"src/t.S": "#include <defs.h>\n" +
			"#define LOCAL 0x10\n" +
			"#define ADD(a, b) ((a)+(b))\n" +
			"\tmovw $LOCAL, %ax\n",
		"inc/defs.h": "/* #define NOTME 1 */\n" +
			"#ifndef DEFS_H\n#define DEFS_H\n" +
			"#include \"more.h\"\n" +
			"  #  define STAGE1_S2_LBA_OFF 0x1B2\n" +
			"#endif\n",
		"inc/more.h": "#define MORE 3\n",
		"inc/pre.h":  "#define PRE 9\n",
	}, "gcc -m32 -DCMDLINE=7 -Iinc -include pre.h -c {} -o x.o")
	file := filepath.Join(root, "src/t.S")
	var r Runner
	m, err := r.Macros(context.Background(), file, flagsFor(t, db, file))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name, value, params string
		fn                  bool
		defFile             string
		defLine, defChar    int
	}{
		{name: "LOCAL", value: "0x10", defFile: "src/t.S", defLine: 1, defChar: 8},
		{name: "ADD", value: "((a)+(b))", params: "a,b", fn: true, defFile: "src/t.S", defLine: 2, defChar: 8},
		{name: "STAGE1_S2_LBA_OFF", value: "0x1B2", defFile: "inc/defs.h", defLine: 4, defChar: 12},
		{name: "MORE", value: "3", defFile: "inc/more.h", defLine: 0, defChar: 8},
		{name: "PRE", value: "9", defFile: "inc/pre.h", defLine: 0, defChar: 8},
		{name: "CMDLINE", value: "7"},
		{name: "__i386__", value: "1"},
	}
	for _, c := range checks {
		mac, ok := m[c.name]
		if !ok {
			t.Errorf("%s missing", c.name)
			continue
		}
		if mac.Value != c.value || mac.Params != c.params || mac.Function != c.fn {
			t.Errorf("%s = %+v", c.name, mac)
		}
		if c.defFile == "" {
			if mac.Def != nil {
				t.Errorf("%s has Def %+v", c.name, mac.Def)
			}
			continue
		}
		want := protocol.Location{URI: PathToURI(filepath.Join(root, c.defFile)), Range: protocol.Range{
			Start: protocol.Position{Line: c.defLine, Character: c.defChar},
			End:   protocol.Position{Line: c.defLine, Character: c.defChar + len(c.name)},
		}}
		if mac.Def == nil || *mac.Def != want {
			t.Errorf("%s Def = %+v, want %+v", c.name, mac.Def, want)
		}
	}
	if _, ok := m["NOTME"]; ok {
		t.Error("commented-out define reported")
	}
	if _, ok := m["__x86_64__"]; ok {
		t.Error("-m32 not applied: __x86_64__ defined")
	}

	// Plain .s files never see cpp.
	sfile := filepath.Join(root, "src/plain.s")
	writeFile(t, sfile, "nop\n")
	if m, err := r.Macros(context.Background(), sfile, flagsFor(t, db, sfile)); err != nil || len(m) != 0 {
		t.Errorf(".s macros = %v %v", m, err)
	}
	// A preprocessor failure with no output is an error.
	bad := filepath.Join(root, "src/bad.S")
	writeFile(t, bad, "#include \"missing.h\"\n")
	if _, err := r.Macros(context.Background(), bad, flagsFor(t, db, bad)); err == nil {
		t.Error("missing include: no error")
	}
}

func TestAssembleE2E(t *testing.T) {
	needGCC(t)
	root, db := project(t, map[string]string{
		"src/t.S": "#include \"bad.inc\"\n" + // 0
			"\t.code16\n" + // 1
			"\t.section .text.stage1, \"ax\"\n" + // 2
			"\t.globl _start\n" + // 3
			"_start:\n" + // 4
			"\tmovw $0x2401, %ax\n" + // 5
			"#if 0\n" + // 6
			"\tthis is not assembled\n" + // 7
			"#endif\n" + // 8
			"\tbogus %ax\n" + // 9
			"\t. = _start + 510\n" + // 10
			"\t.byte 0x55, 0xAA\n", // 11
		"src/bad.inc": "/* first */\n" +
			"\tnop\n" +
			"\tbadinc %eax\n",
	}, "gcc -m32 -c {} -o x.o")
	file := filepath.Join(root, "src/t.S")
	var r Runner
	res, err := r.Assemble(context.Background(), file, flagsFor(t, db, file))
	if err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(root, "src/bad.inc")
	findDiag := func(path string, line int, substr string) bool {
		for _, d := range res.Diagnostics[path] {
			if d.Range.Start.Line == line && strings.Contains(d.Message, substr) && d.Severity == protocol.SeverityError {
				return true
			}
		}
		return false
	}
	if !findDiag(file, 9, "bogus") {
		t.Errorf("no error on t.S:10: %+v", res.Diagnostics)
	}
	if !findDiag(inc, 2, "badinc") {
		t.Errorf("no error on bad.inc:3: %+v", res.Diagnostics)
	}
	if !findDiag(file, 0, "bad.inc:3: no such instruction") {
		t.Errorf("include error not mirrored on t.S:1: %+v", res.Diagnostics[file])
	}
	if d := res.Diagnostics[file]; len(d) > 0 && d[0].Range.Start.Line == 9 {
		if d[0].Range.Start.Character != 1 || d[0].Range.End.Character != 10 {
			t.Errorf("range = %+v", d[0].Range)
		}
	}
	if len(res.Inactive) != 1 || res.Inactive[0] != lineRange(7, 8) {
		t.Errorf("Inactive = %+v", res.Inactive)
	}
	// With errors gas still writes the listing, but prints ???? for every
	// address: bytes are known, offsets are not.
	mov := listed(res, 5)
	if mov == nil || hexOf(mov.Bytes) != "b80124" || mov.HasOffset || !mov.Unknown || mov.Section != ".text.stage1" {
		t.Errorf("movw listing = %+v", mov)
	}
	if _, ok := res.PadBytes(10); ok {
		t.Error("PadBytes answered with unknown offsets")
	}

	// The same file without the errors: real offsets and the padding count.
	src, _ := os.ReadFile(file)
	clean := strings.Replace(strings.Replace(string(src), "\tbogus %ax\n", "\n", 1), "#include \"bad.inc\"\n", "\n", 1)
	writeFile(t, file, clean)
	res, err = r.Assemble(context.Background(), file, flagsFor(t, db, file))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diagnostics) != 0 {
		t.Errorf("clean diags = %+v", res.Diagnostics)
	}
	mov = listed(res, 5)
	if mov == nil || hexOf(mov.Bytes) != "b80124" || !mov.HasOffset || mov.Offset != 0 || mov.Section != ".text.stage1" {
		t.Errorf("movw listing = %+v", mov)
	}
	if listed(res, 7) != nil {
		t.Error("inactive line listed")
	}
	if len(res.Inactive) != 1 || res.Inactive[0] != lineRange(7, 8) {
		t.Errorf("Inactive = %+v", res.Inactive)
	}
	if n, ok := res.PadBytes(10); !ok || n != 510-3 {
		t.Errorf("PadBytes = %d %v, want 507", n, ok)
	}
	if sig := listed(res, 11); sig == nil || sig.Offset != 510 || hexOf(sig.Bytes) != "55aa" {
		t.Errorf("signature = %+v", sig)
	}
}

func listed(res *AsmResult, line int) *ListingLine {
	for i := range res.Listing {
		if res.Listing[i].Line == line {
			return &res.Listing[i]
		}
	}
	return nil
}

func TestAssembleCleanAndSizes(t *testing.T) {
	needGCC(t)
	root, db := project(t, map[string]string{
		"a/t.S": ".code16\n_start:\n\tmovw $0x2401, %ax\n\t.org 0x20\n",
		"a/p.s": ".text\n\tnop\n\t.data\n\t.long 1\n",
		"a/w.S": "\t.byte 300\n",
		"a/c.c": "int x;\n",
	}, "gcc -m32 -Wa,--fatal-warnings -c {} -o x.o")
	var r Runner
	file := filepath.Join(root, "a/t.S")
	res, err := r.Assemble(context.Background(), file, flagsFor(t, db, file))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diagnostics) != 0 {
		t.Errorf("diags = %+v", res.Diagnostics)
	}
	// The .org is the last thing in .text: only the object's section size
	// can close it.
	if _, ok := PadBytes(res.Listing, 3); ok {
		t.Error("listing alone closed the last gap")
	}
	if n, ok := res.PadBytes(3); !ok || n != 0x20-3 {
		t.Errorf("PadBytes = %d %v", n, ok)
	}

	// A plain .s is assembled directly.
	p := filepath.Join(root, "a/p.s")
	res, err = r.Assemble(context.Background(), p, flagsFor(t, db, p))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Listing) < 4 || res.Listing[3].Section != ".data" || hexOf(res.Listing[3].Bytes) != "01000000" {
		t.Errorf(".s listing = %+v", res.Listing)
	}

	// --fatal-warnings: the warning is still reported as a warning.
	w := filepath.Join(root, "a/w.S")
	res, err = r.Assemble(context.Background(), w, flagsFor(t, db, w))
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Diagnostics[w]; len(d) == 0 || d[0].Severity != protocol.SeverityWarning || d[0].Range.Start.Line != 0 {
		t.Errorf("warning diags = %+v", res.Diagnostics)
	}

	c := filepath.Join(root, "a/c.c")
	if _, err := r.Assemble(context.Background(), c, flagsFor(t, db, c)); !errors.Is(err, ErrNotAssembly) {
		t.Errorf("C file err = %v", err)
	}
}

func TestAssemblePreprocessorError(t *testing.T) {
	needGCC(t)
	root, db := project(t, map[string]string{
		"s/t.S":      "nop\n#include \"h.h\"\n",
		"s/h.h":      "#include \"missing.h\"\n",
		"s/u.S":      "#error stop here\n",
		"s/v.S":      "#include \"nested.h\"\n",
		"s/nested.h": "#error in header\n",
	}, "gcc -m32 -c {}")
	var r Runner
	file := filepath.Join(root, "s/t.S")
	res, err := r.Assemble(context.Background(), file, flagsFor(t, db, file))
	if err != nil {
		t.Fatal(err)
	}
	h := filepath.Join(root, "s/h.h")
	if d := res.Diagnostics[h]; len(d) != 1 || !strings.Contains(d[0].Message, "missing.h") || d[0].Range.Start.Character != 9 {
		t.Errorf("header diags = %+v", res.Diagnostics)
	}
	if d := res.Diagnostics[file]; len(d) != 1 || d[0].Range.Start.Line != 1 {
		t.Errorf("mirrored diag = %+v", res.Diagnostics[file])
	}
	if res.Listing != nil {
		t.Error("listing produced after a cpp failure")
	}
	u := filepath.Join(root, "s/u.S")
	res, err = r.Assemble(context.Background(), u, flagsFor(t, db, u))
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Diagnostics[u]; len(d) != 1 || d[0].Message != "#error stop here" {
		t.Errorf("#error diags = %+v", res.Diagnostics)
	}
}

func TestNoToolchain(t *testing.T) {
	f := Flags{Compiler: "falcon-asm-no-such-compiler-xyz", Dir: t.TempDir()}
	var r Runner
	if _, err := r.Assemble(context.Background(), filepath.Join(f.Dir, "x.S"), f); !errors.Is(err, ErrNoToolchain) {
		t.Errorf("Assemble err = %v", err)
	}
	if _, err := r.Macros(context.Background(), filepath.Join(f.Dir, "x.S"), f); !errors.Is(err, ErrNoToolchain) {
		t.Errorf("Macros err = %v", err)
	}
	if _, err := compilerPath(Flags{}); !errors.Is(err, ErrNoToolchain) {
		t.Errorf("empty compiler err = %v", err)
	}
}

// slowCompiler writes a stand-in compiler that never finishes, to exercise
// timeouts and supersession without depending on gcc's speed.
func slowCompiler(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	p := filepath.Join(t.TempDir(), "slowcc")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTimeout(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "t.S")
	writeFile(t, file, "nop\n")
	f := Flags{Compiler: slowCompiler(t), Dir: dir, File: file}
	r := Runner{Timeout: 150 * time.Millisecond}
	start := time.Now()
	_, err := r.Assemble(context.Background(), file, f)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Assemble err = %v", err)
	}
	_, err = r.Macros(context.Background(), file, f)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Macros err = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("timeouts took %v", d)
	}
	// The caller's own cancellation is honoured too.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Assemble(ctx, file, f); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx err = %v", err)
	}
}

func TestSupersede(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "t.S")
	writeFile(t, file, "nop\n")
	f := Flags{Compiler: slowCompiler(t), Dir: dir, File: file}
	r := Runner{Timeout: 20 * time.Second}
	first := make(chan error, 1)
	go func() {
		_, err := r.Assemble(context.Background(), file, f)
		first <- err
	}()
	// Wait until the first run is registered before starting the second.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.running)
		r.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := r.Assemble(ctx, file, f)
		second <- err
	}()
	select {
	case err := <-first:
		if !errors.Is(err, ErrSuperseded) {
			t.Errorf("first err = %v, want ErrSuperseded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first run not cancelled")
	}
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Errorf("second err = %v", err)
	}
	r.mu.Lock()
	n := len(r.running)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("%d runs still registered", n)
	}
}

func TestRunExecFailure(t *testing.T) {
	// A compiler that exists but cannot be executed is an exec error, not
	// an exit status.
	dir := t.TempDir()
	p := filepath.Join(dir, "notexec")
	writeFile(t, p, "not a program")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "t.S")
	writeFile(t, file, "nop\n")
	var r Runner
	if _, err := r.Assemble(context.Background(), file, Flags{Compiler: p, Dir: dir}); err == nil {
		t.Error("no error from a non-executable compiler")
	}
	// A compiler that fails without any parseable message.
	fail := filepath.Join(dir, "failcc")
	writeFile(t, fail, "#!/bin/sh\necho 'something broke' >&2\nexit 1\n")
	if err := os.Chmod(fail, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Assemble(context.Background(), file, Flags{Compiler: fail, Dir: dir}); err == nil ||
		!strings.Contains(err.Error(), "something broke") {
		t.Errorf("silent failure err = %v", err)
	}
	if _, err := r.Macros(context.Background(), file, Flags{Compiler: fail, Dir: dir}); err == nil {
		t.Error("Macros: no error from failing compiler")
	}
	// cpp succeeds (writes nothing) but the assembler step fails silently.
	half := filepath.Join(dir, "halfcc")
	writeFile(t, half, "#!/bin/sh\nfor a; do case $a in -E) touch \"$(eval echo \\${$#})\"; exit 0;; esac; done\necho 'as died' >&2\nexit 1\n")
	if err := os.Chmod(half, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := r.Assemble(context.Background(), file, Flags{Compiler: half, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Diagnostics[file]; len(d) != 1 || !strings.Contains(d[0].Message, "as died") {
		t.Errorf("diags = %+v", res.Diagnostics)
	}
}
