// Package project is what the language server and the lint CLI share about
// a file in a project: which language it is, the build target it is
// assembled for (from compile flags, .falcon-asm.json, editor settings and
// in-file comments, in one precedence order), and how a whole document,
// assembly or C with inline asm, is linted.
//
// Keeping this in one place is the point: the editor and the CLI must
// report the same findings for the same file, or the CLI in CI and the
// squiggles in the editor would argue with each other.
package project

import (
	"fmt"
	"path/filepath"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/analysis"
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/callconv"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// CExts are the extensions of C-family sources: served for inline asm.
var CExts = map[string]bool{
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".hh": true,
}

// AsmExts are the extensions the CLI collects as assembly when walking a
// tree. The server serves any non-C document as assembly; a walk has to
// be choosier, or every README would be linted.
var AsmExts = map[string]bool{".s": true}

// IsC reports whether path names a C-family source.
func IsC(path string) bool { return CExts[strings.ToLower(filepath.Ext(path))] }

// IsAsm reports whether path names an assembly source (.S or .s). A
// preprocessed linker script (jmod.ld.S) ends in .S too but is not one.
func IsAsm(path string) bool {
	return AsmExts[strings.ToLower(filepath.Ext(path))] && !toolchain.IsLinkerScript(path)
}

// TargetInputs are the sources of a file's target other than its own
// text, which ResolveTarget combines.
type TargetInputs struct {
	Flags    toolchain.Flags
	HasFlags bool
	Config   lint.TargetConfig // from .falcon-asm.json, overrides applied
	// SettingLevel is the editor's falcon-asm.level setting; the CLI has
	// none.
	SettingLevel  isa.Level
	SettingSource string
}

// LevelComment is an in-file "falcon-asm: level=..." comment.
type LevelComment struct {
	Level  isa.Level
	Source string
	OK     bool
}

// AsmLevelComment finds the level comment of an assembly document. The
// source text matches what internal/analysis reports for the same comment.
func AsmLevelComment(toks []lexer.Token) LevelComment {
	lvl, line, ok := analysis.LevelOverrideTokens(toks)
	if !ok {
		return LevelComment{}
	}
	return LevelComment{Level: lvl, Source: fmt.Sprintf("falcon-asm: level comment at line %d", line+1), OK: true}
}

// CLevelComment finds the level comment of a C document, which applies to
// all of its inline asm.
func CLevelComment(text string) LevelComment {
	lvl, ok := analysis.LevelOverride(text)
	if !ok {
		return LevelComment{}
	}
	return LevelComment{Level: lvl, Source: "falcon-asm: level comment", OK: true}
}

// ResolveTarget combines the inputs into a target. Precedence, highest
// first:
//
//   - mode: .falcon-asm.json target.mode, then the -m flag, else 64
//   - level: the in-file comment, then .falcon-asm.json, then the editor
//     setting, then -march=x86-64-vN
//   - cpu: .falcon-asm.json, then a -march naming a CPU
//   - cc: .falcon-asm.json, then inferred from the flags (64-bit: sysv,
//     or ms for a mingw compiler; 32-bit: cdecl; 16-bit: none)
//
// The project file outranks the editor setting because it is shared by
// everyone building the project, and the comment outranks everything
// because it is the file speaking for itself.
func ResolveTarget(in TargetInputs, comment LevelComment) lint.Target {
	t := lint.Target{Mode: 64, Sources: map[string]string{}}
	switch {
	case in.Config.Mode != 0:
		t.Mode = asm.Mode(in.Config.Mode)
		t.Sources["mode"] = lint.ConfigFile + " target.mode"
	case in.HasFlags && (in.Flags.Mode == 16 || in.Flags.Mode == 32 || in.Flags.Mode == 64):
		t.Mode = asm.Mode(in.Flags.Mode)
		t.Sources["mode"] = "compile flags"
	}

	cfgLevel, cfgOK := lint.ParseLevel(in.Config.Level)
	switch {
	case comment.OK:
		t.Level = int(comment.Level)
		t.Sources["level"] = comment.Source
	case in.Config.Level != "" && cfgOK:
		t.Level = cfgLevel
		t.Sources["level"] = lint.ConfigFile + " target.level " + in.Config.Level
	case in.SettingLevel != 0:
		t.Level = int(in.SettingLevel)
		t.Sources["level"] = in.SettingSource
	case in.HasFlags && in.Flags.Level != 0:
		t.Level = int(in.Flags.Level)
		t.Sources["level"] = in.Flags.LevelSource
	}

	switch {
	case in.Config.CPU != "":
		t.CPU = in.Config.CPU
		t.Sources["cpu"] = lint.ConfigFile + " target.cpu"
	case in.HasFlags:
		if cpu := marchCPU(in.Flags.Args); cpu != "" {
			t.CPU = cpu
			t.Sources["cpu"] = "-march=" + cpu
		}
	}

	switch {
	case in.Config.CC != "":
		if in.Config.CC != "none" {
			t.CC = in.Config.CC
		}
		t.Sources["cc"] = lint.ConfigFile + " target.cc"
	case in.HasFlags:
		if cc := inferCC(in.Flags); cc != "" {
			t.CC = cc
			t.Sources["cc"] = "inferred from compile flags"
		}
	}
	return t
}

// marchCPU is the CPU a -march names; the x86-64 level names are levels,
// not CPUs, and native depends on the build machine.
func marchCPU(args []string) string {
	cpu := ""
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "-march="); ok {
			cpu = strings.ToLower(v)
		}
	}
	if cpu == "native" || cpu == "x86-64" || strings.HasPrefix(cpu, "x86-64-v") {
		return ""
	}
	return cpu
}

// inferCC is the platform's default convention for the flags (roadmap
// item 32). 16-bit code has no standard convention; it stays none until
// the file declares one. In 32-bit code, gcc's -mregparm=N passes the
// first N integer arguments in eax, edx, ecx and -mrtd makes callees pop
// their arguments; the last of each flag wins, as with gcc.
func inferCC(f toolchain.Flags) string {
	switch f.Mode {
	case 64:
		if strings.Contains(strings.ToLower(filepath.Base(f.Compiler)), "mingw") {
			return "ms"
		}
		return "sysv"
	case 32:
		cc := "cdecl"
		for _, a := range f.Args {
			switch {
			case a == "-mrtd":
				cc = "stdcall"
			case a == "-mno-rtd":
				cc = "cdecl"
			case strings.HasPrefix(a, "-mregparm="):
				if n := strings.TrimPrefix(a, "-mregparm="); n != "0" && callconv.Valid("regparm("+n+")") {
					cc = "regparm(" + n + ")"
				} else if n == "0" {
					cc = "cdecl"
				}
			}
		}
		return cc
	}
	return ""
}
