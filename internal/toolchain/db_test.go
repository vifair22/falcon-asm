package toolchain

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDB(t *testing.T, path string, entries []rawEntry) {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b))
}

// synthDB builds a project resembling airies-boot: .S files compiled with
// -m32, a C file with a mingw cross compiler, all as "command" strings.
func synthDB(t *testing.T) string {
	root := t.TempDir()
	writeDB(t, filepath.Join(root, DBFile), []rawEntry{
		{Directory: root, File: filepath.Join(root, "bios/src/start/boot.S"),
			Command: "gcc -m32 -c -Wa,--fatal-warnings -Ibios/src -Icore/src -MMD -MF build/boot.o.d -c -o build/boot.o " + filepath.Join(root, "bios/src/start/boot.S")},
		{Directory: root, File: "bios/src/start/stage2.S",
			Arguments: []string{"gcc", "-m16", "-DSTAGE=2", "-iquote", "q", "-c", "bios/src/start/stage2.S", "-o", "x.o"}},
		{Directory: root, File: "bios/src/start/helper.c",
			Command: "gcc -std=c11 -O2 -Wall -Ibios/src -c bios/src/start/helper.c -o h.o"},
		{Directory: root, File: "efi/main.c",
			Command: "x86_64-w64-mingw32-gcc -std=c11 -march=x86-64-v3 -mno-red-zone -Iefi/src -c -o m.o efi/main.c"},
		{Directory: root, File: "efi/deep/sub/x.c",
			Command: "i686-elf-gcc -march=x86-64 -c efi/deep/sub/x.c"},
		{Directory: root, File: "bad.c", Command: `gcc "unterminated`},
		{Directory: root, File: "", Command: "gcc -c"},
		{Directory: "rel", File: "r.c", Command: "gcc -c r.c"},
	})
	return root
}

func TestLoadDBAndFlags(t *testing.T) {
	root := synthDB(t)
	db, err := LoadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	if db.Len() != 6 {
		t.Fatalf("Len = %d, want 6 (malformed and fileless entries skipped)", db.Len())
	}
	tests := []struct {
		name     string
		file     string
		ok       bool
		exact    bool
		compiler string
		mode     int
		level    isa.Level
		levelSrc string
		args     []string
	}{
		{name: "exact asm", file: "bios/src/start/boot.S", ok: true, exact: true, compiler: "gcc", mode: 32,
			args: []string{"-m32", "-Wa,--fatal-warnings", "-Ibios/src", "-Icore/src"}},
		{name: "exact arguments form", file: "bios/src/start/stage2.S", ok: true, exact: true, compiler: "gcc", mode: 16,
			args: []string{"-m16", "-DSTAGE=2", "-iquote", "q"}},
		{name: "same dir prefers asm", file: "bios/src/start/new.S", ok: true, compiler: "gcc", mode: 32,
			args: []string{"-m32", "-Wa,--fatal-warnings", "-Ibios/src", "-Icore/src"}},
		{name: "same dir C prefers C", file: "bios/src/start/other.c", ok: true, compiler: "gcc", mode: 64,
			args: []string{"-std=c11", "-O2", "-Ibios/src"}},
		{name: "same top-level dir", file: "bios/src/other/thing.inc", ok: true, compiler: "gcc", mode: 32,
			args: []string{"-m32", "-Wa,--fatal-warnings", "-Ibios/src", "-Icore/src"}},
		{name: "mingw level v3", file: "efi/main.c", ok: true, exact: true, compiler: "x86_64-w64-mingw32-gcc",
			mode: 64, level: 3, levelSrc: "-march=x86-64-v3",
			args: []string{"-std=c11", "-march=x86-64-v3", "-mno-red-zone", "-Iefi/src"}},
		{name: "i686 triple is 32-bit, x86-64 is v1", file: "efi/deep/sub/x.c", ok: true, exact: true,
			compiler: "i686-elf-gcc", mode: 32, level: 1, levelSrc: "-march=x86-64", args: []string{"-march=x86-64"}},
		{name: "unrelated top-level dir", file: "tools/x.S", ok: false},
		{name: "file in root", file: "loose.S", ok: false},
		{name: "outside root", file: "/elsewhere/x.S", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := tt.file
			if !filepath.IsAbs(file) {
				file = filepath.Join(root, file)
			}
			f, ok := db.Flags(file)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if f.Exact != tt.exact || f.Compiler != tt.compiler || f.Mode != tt.mode ||
				f.Level != tt.level || f.LevelSource != tt.levelSrc || f.Dir != root || f.File != file {
				t.Errorf("got %+v", f)
			}
			if !reflect.DeepEqual(f.Args, tt.args) {
				t.Errorf("Args = %q, want %q", f.Args, tt.args)
			}
		})
	}
	var nilDB *DB
	if _, ok := nilDB.Flags("x"); ok {
		t.Error("nil DB returned flags")
	}
}

func TestLoadDBLocations(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadDB(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty root: err = %v, want ErrNotExist", err)
	}
	writeDB(t, filepath.Join(root, "build", DBFile), []rawEntry{
		{Directory: root, File: "a/x.S", Command: "gcc -m16 -c a/x.S"},
	})
	db, err := LoadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	if db.Root != root || db.Path != filepath.Join(root, "build", DBFile) {
		t.Errorf("Root/Path = %q %q", db.Root, db.Path)
	}
	// FindDB walks up from a nested file.
	nested := filepath.Join(root, "a", "b", "c", "y.S")
	writeFile(t, nested, "nop\n")
	db2, err := FindDB(nested)
	if err != nil {
		t.Fatal(err)
	}
	if db2.Path != db.Path {
		t.Errorf("FindDB path = %q", db2.Path)
	}
	if f, ok := db2.Flags(filepath.Join(root, "a", "x.S")); !ok || f.Mode != 16 {
		t.Errorf("flags = %+v %v", f, ok)
	}

	bad := t.TempDir()
	writeFile(t, filepath.Join(bad, DBFile), "{not json")
	if _, err := LoadDB(bad); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("bad json err = %v", err)
	}
	if _, err := FindDB(filepath.Join(bad, "x", "y.S")); err == nil {
		t.Error("FindDB through a corrupt database succeeded")
	}
}

func TestFindDBNone(t *testing.T) {
	// The temp dir has no database anywhere above it, normally; skip if
	// the machine happens to have one in a parent.
	dir := t.TempDir()
	if _, err := FindDB(filepath.Join(dir, "x.S")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v", err)
	}
}

func TestFilterArgs(t *testing.T) {
	in := []string{
		"-c", "-o", "out.o", "-oout2.o", "-MMD", "-MF", "dep.d", "-MT", "t", "-MQ", "q", "-MD", "-MP",
		"-Wall", "-Werror", "-Wa,--32", "-Wp,-DX", "-g", "-g3", "-x", "c", "-xassembler",
		"-I", "inc", "-Iinc2", "-isystem", "sys", "-isystemsys2", "-iquote", "q", "-idirafter", "late",
		"-D", "A=1", "-DB", "-U", "C", "-UD", "-include", "pre.h", "-imacros", "m.h",
		"-m32", "-march=x86-64-v2", "-std=gnu11", "-O2", "-ffreestanding", "-fdiagnostics-color=always",
		"-nostdinc", "-Xassembler", "--fatal-warnings", "-target", "i386", "--target=x86_64",
		"-lfoo", "-L", "lib", "-Wl,-T,x.ld", "-pipe", "src.S", "-I",
	}
	want := []string{
		"-Wa,--32", "-Wp,-DX",
		"-I", "inc", "-Iinc2", "-isystem", "sys", "-isystemsys2", "-iquote", "q", "-idirafter", "late",
		"-D", "A=1", "-DB", "-U", "C", "-UD", "-include", "pre.h", "-imacros", "m.h",
		"-m32", "-march=x86-64-v2", "-std=gnu11", "-O2", "-ffreestanding",
		"-nostdinc", "-Xassembler", "--fatal-warnings", "-target", "i386", "--target=x86_64",
	}
	if got := filterArgs(in); !reflect.DeepEqual(got, want) {
		t.Errorf("filterArgs =\n%q\nwant\n%q", got, want)
	}
}

func TestModeAndLevel(t *testing.T) {
	tests := []struct {
		compiler string
		args     []string
		mode     int
		level    isa.Level
	}{
		{"gcc", nil, 64, 0},
		{"gcc", []string{"-m32", "-m16"}, 16, 0},
		{"gcc", []string{"-m16", "-m64"}, 64, 0},
		{"gcc", []string{"-mx32"}, 64, 0},
		{"/opt/x/bin/i386-elf-gcc", nil, 32, 0},
		{"i686-w64-mingw32-gcc", []string{"-m64"}, 64, 0},
		{"x86_64-w64-mingw32-gcc", []string{"-march=x86-64-v4"}, 64, 4},
		{"gcc", []string{"-march=x86-64-v2"}, 64, 2},
		{"gcc", []string{"-march=x86-64-v3", "-march=native"}, 64, 0},
		{"gcc", []string{"-march=haswell"}, 64, 0},
	}
	for _, tt := range tests {
		if m := modeFromArgs(tt.compiler, tt.args); m != tt.mode {
			t.Errorf("mode(%s %v) = %d, want %d", tt.compiler, tt.args, m, tt.mode)
		}
		if l, _ := levelFromArgs(tt.args); l != tt.level {
			t.Errorf("level(%v) = %d, want %d", tt.args, l, tt.level)
		}
	}
}

func TestIncludeDirsAndResolve(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src/start/boot.S"), "")
	writeFile(t, filepath.Join(root, "src/start/local.h"), "")
	writeFile(t, filepath.Join(root, "src/start/both.h"), "")
	writeFile(t, filepath.Join(root, "inc/start/biosdefs.h"), "")
	writeFile(t, filepath.Join(root, "inc/both.h"), "")
	writeFile(t, filepath.Join(root, "q/quoted.h"), "")
	writeFile(t, filepath.Join(root, "sys/sys.h"), "")
	writeFile(t, filepath.Join(root, "late/late.h"), "")
	writeDB(t, filepath.Join(root, DBFile), []rawEntry{{
		Directory: root, File: "src/start/boot.S",
		Command: "/usr/bin/gcc -isystem sys -Iinc -iquote q -idirafter late -I" + filepath.Join(root, "inc") + " -c src/start/boot.S",
	}})
	db, err := LoadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "src/start/boot.S")
	f, ok := db.Flags(main)
	if !ok {
		t.Fatal("no flags")
	}
	wantDirs := []string{
		filepath.Join(root, "src/start"), filepath.Join(root, "q"), filepath.Join(root, "inc"),
		filepath.Join(root, "sys"), filepath.Join(root, "late"),
	}
	if got := f.IncludeDirs(); !reflect.DeepEqual(got, wantDirs) {
		t.Errorf("IncludeDirs = %q, want %q", got, wantDirs)
	}
	tests := []struct {
		name   string
		angled bool
		want   string
	}{
		{"start/biosdefs.h", true, "inc/start/biosdefs.h"},
		{"start/biosdefs.h", false, "inc/start/biosdefs.h"},
		{"local.h", false, "src/start/local.h"},
		{"local.h", true, ""},
		{"both.h", false, "src/start/both.h"},
		{"both.h", true, "inc/both.h"},
		{"quoted.h", false, "q/quoted.h"},
		{"quoted.h", true, ""},
		{"sys.h", true, "sys/sys.h"},
		{"late.h", true, "late/late.h"},
		{"missing.h", false, ""},
		{"", false, ""},
		{filepath.Join(root, "inc/both.h"), true, "inc/both.h"},
	}
	for _, tt := range tests {
		got, ok := f.ResolveInclude(main, tt.name, tt.angled)
		want := ""
		if tt.want != "" {
			want = filepath.Join(root, tt.want)
		}
		if ok != (want != "") || (ok && got != want) {
			t.Errorf("ResolveInclude(%q, %v) = %q %v, want %q", tt.name, tt.angled, got, ok, want)
		}
	}
	// A relative compiler path is anchored at the entry's directory.
	writeDB(t, filepath.Join(root, DBFile), []rawEntry{{
		Directory: root, File: "src/start/boot.S", Command: "tools/cc -c src/start/boot.S",
	}})
	db, _ = LoadDB(root)
	f, _ = db.Flags(main)
	if f.Compiler != filepath.Join(root, "tools/cc") {
		t.Errorf("Compiler = %q", f.Compiler)
	}
	// Duplicates: the later entry wins.
	writeDB(t, filepath.Join(root, DBFile), []rawEntry{
		{Directory: root, File: "src/start/boot.S", Command: "gcc -m32 -c src/start/boot.S"},
		{Directory: root, File: "src/start/boot.S", Command: "gcc -m16 -c src/start/boot.S"},
	})
	db, _ = LoadDB(root)
	if f, _ = db.Flags(main); f.Mode != 16 || db.Len() != 1 {
		t.Errorf("duplicate handling: mode %d len %d", f.Mode, db.Len())
	}
}

func TestLoadDBFile(t *testing.T) {
	root := t.TempDir()
	entries := []rawEntry{{Directory: root, File: "a.S", Command: "gcc -m16 -c a.S"}}
	writeDB(t, filepath.Join(root, "build", DBFile), entries)
	writeDB(t, filepath.Join(root, "other.json"), entries)

	db, err := LoadDBFile(filepath.Join(root, "build", DBFile))
	if err != nil || db.Root != root || db.Len() != 1 {
		t.Fatalf("build/ file: %+v %v", db, err)
	}
	db, err = LoadDBFile(filepath.Join(root, "other.json"))
	if err != nil || db.Root != root {
		t.Fatalf("named file: %+v %v", db, err)
	}
	if f, ok := db.Flags(filepath.Join(root, "a.S")); !ok || f.Mode != 16 {
		t.Fatalf("flags = %+v %v", f, ok)
	}
	db, err = LoadDBFile(root)
	if err != nil || db.Path != filepath.Join(root, "build", DBFile) {
		t.Fatalf("directory: %+v %v", db, err)
	}
	if _, err := LoadDBFile(filepath.Join(root, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}
