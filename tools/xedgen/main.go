// Command xedgen generates internal/isa/data/xed.json from Intel XED's
// instruction datafiles and asm-lsp's x86 documentation data. See README.md
// in this directory for sources, pinned versions and licensing.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "xedgen: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, log io.Writer) error {
	fs := flag.NewFlagSet("xedgen", flag.ContinueOnError)
	fs.SetOutput(log)
	xedPath := fs.String("xed", "build/xed", "XED checkout (or its datafiles directory)")
	asmPath := fs.String("asmlsp", "build/asm-lsp", "asm-lsp checkout")
	outPath := fs.String("out", "internal/isa/data/xed.json", "output JSON file")
	fallbacks := fs.String("fallbacks", "", "optional file to list instructions whose summary fell back to XED")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("flags: %w", err)
	}

	datafiles := *xedPath
	if _, err := os.Stat(filepath.Join(datafiles, "datafiles")); err == nil {
		datafiles = filepath.Join(datafiles, "datafiles")
	}
	db, err := loadXED(datafiles)
	if err != nil {
		return fmt.Errorf("load xed: %w", err)
	}
	docs, err := loadAsmLSP(*asmPath)
	if err != nil {
		return fmt.Errorf("load asm-lsp: %w", err)
	}
	insts, st := build(db, docs)
	data, err := encode(insts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if *fallbacks != "" {
		body := strings.Join(st.Fallback, "\n") + "\n"
		if err := os.WriteFile(*fallbacks, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write fallbacks: %w", err)
		}
	}
	forms := 0
	for _, in := range insts {
		forms += len(in.Forms)
	}
	fmt.Fprintf(log, "xedgen: %d XED records, %d instructions, %d forms, %d XED-derived summaries, %d bytes -> %s\n",
		len(db.Records), len(insts), forms, len(st.Fallback), len(data), *outPath)
	return nil
}

// encode writes one instruction per line: still compact, but a regenerated
// file diffs line by line. HTML escaping is off so operands like "m16&32"
// stay readable.
func encode(insts []Instruction) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("[\n")
	for i, in := range insts {
		var line bytes.Buffer
		enc := json.NewEncoder(&line)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(in); err != nil {
			return nil, fmt.Errorf("encode %s: %w", in.Name, err)
		}
		out.Write(bytes.TrimRight(line.Bytes(), "\n"))
		if i < len(insts)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString("]\n")
	for i, b := range out.Bytes() {
		if b >= 0x80 {
			return nil, fmt.Errorf("non-ASCII byte 0x%02x at offset %d", b, i)
		}
	}
	return out.Bytes(), nil
}
