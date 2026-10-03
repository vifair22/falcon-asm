package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// xedRecord is one flattened XED instruction definition: the record-level
// keys (ICLASS, CATEGORY, ...) combined with a single PATTERN/OPERANDS pair.
// XED's structured input allows several pattern groups inside one { } block;
// they share the header keys, so flattening up front lets everything
// downstream treat a record as one encoding.
type xedRecord struct {
	IClass     string
	UName      string
	Category   string
	Extension  string
	ISASet     string
	CPL        int
	Attributes []string
	Flags      string
	Pattern    []string
	Operands   []string
	IForm      string
	File       string
}

// hasAttr reports whether the record carries attribute a (case-insensitive,
// because XED spells a few attributes in lower case).
func (r *xedRecord) hasAttr(a string) bool {
	for _, x := range r.Attributes {
		if strings.EqualFold(x, a) {
			return true
		}
	}
	return false
}

// hasToken reports whether the decode pattern contains token t exactly.
func (r *xedRecord) hasToken(t string) bool {
	for _, x := range r.Pattern {
		if x == t {
			return true
		}
	}
	return false
}

// xedDB is everything the generator needs from the datafiles: the surviving
// records (after UDELETE/DELETE processing) and the operand width table.
type xedDB struct {
	Records []*xedRecord
	Widths  map[string]width
}

// width is one row of an XED operand-width table. Bits holds one value, or
// three for widths that vary with the effective operand size (16/32/64).
type width struct {
	XType string
	Bits  []int
}

// bitsFor returns the width for effective operand size osz (16, 32 or 64).
func (w width) bitsFor(osz int) int {
	if len(w.Bits) == 0 {
		return 0
	}
	if len(w.Bits) < 3 {
		return w.Bits[0]
	}
	switch osz {
	case 16:
		return w.Bits[0]
	case 32:
		return w.Bits[1]
	default:
		return w.Bits[2]
	}
}

// varies reports whether the width depends on the effective operand size.
func (w width) varies() bool {
	return len(w.Bits) == 3 && !(w.Bits[0] == w.Bits[1] && w.Bits[1] == w.Bits[2])
}

// cfgEntry is one "kind : file" line from an XED files*.cfg.
type cfgEntry struct {
	Kind string
	Path string
}

// parseCfg reads an XED extension config. Lines look like
// "dec-instructions : foo.txt" or "add:dec-spine:foo.txt:2"; paths are
// relative to the cfg's directory.
func parseCfg(r io.Reader, dir string) ([]cfgEntry, error) {
	var out []cfgEntry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := stripComment(sc.Text())
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "add:")
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}
		kind := strings.TrimSpace(parts[0])
		file := strings.TrimSpace(parts[1])
		if kind == "" || file == "" {
			continue
		}
		out = append(out, cfgEntry{Kind: kind, Path: filepath.Join(dir, file)})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read cfg: %w", err)
	}
	return out, nil
}

func stripComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// loadXED walks every *.cfg under datafiles and reads each referenced
// instruction and width file once. Taking every extension (rather than
// reproducing the chip selection in xed_mbuild.py) is deliberate: the
// language server wants to know about every instruction GAS can assemble,
// and XED's own UDELETE records already resolve the few places where two
// extensions define competing versions of the same encoding.
func loadXED(datafiles string) (*xedDB, error) {
	var cfgs []string
	err := filepath.WalkDir(datafiles, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".cfg") {
			cfgs = append(cfgs, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", datafiles, err)
	}
	// Root configs first so base definitions precede extensions, then a
	// stable order for reproducible output.
	sort.Slice(cfgs, func(i, j int) bool {
		di, dj := filepath.Dir(cfgs[i]), filepath.Dir(cfgs[j])
		if (di == filepath.Clean(datafiles)) != (dj == filepath.Clean(datafiles)) {
			return di == filepath.Clean(datafiles)
		}
		return cfgs[i] < cfgs[j]
	})

	var instFiles, widthFiles []string
	seen := map[string]bool{}
	for _, c := range cfgs {
		f, err := os.Open(c)
		if err != nil {
			return nil, fmt.Errorf("open cfg: %w", err)
		}
		entries, err := parseCfg(f, filepath.Dir(c))
		_ = f.Close() // read-only: the parse error is the one that matters
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c, err)
		}
		for _, e := range entries {
			key := e.Kind + "|" + e.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			switch e.Kind {
			case "dec-instructions":
				instFiles = append(instFiles, e.Path)
			case "widths":
				widthFiles = append(widthFiles, e.Path)
			}
		}
	}

	db := &xedDB{Widths: map[string]width{}}
	for _, p := range widthFiles {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("open widths: %w", err)
		}
		err = parseWidths(f, db.Widths)
		_ = f.Close() // read-only
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}

	var all []*xedRecord
	unames := map[string]bool{}
	iclasses := map[string]bool{}
	for _, p := range instFiles {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("open isa file: %w", err)
		}
		rel, _ := filepath.Rel(datafiles, p)
		pf, err := parseISA(f, rel)
		_ = f.Close() // read-only
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		all = append(all, pf.Records...)
		for _, u := range pf.UDeletes {
			unames[u] = true
		}
		for _, d := range pf.Deletes {
			iclasses[d] = true
		}
	}
	db.Records = applyDeletes(all, unames, iclasses)
	return db, nil
}

// applyDeletes mirrors XED's generator: deletes are collected across all
// files and applied afterwards, so ordering between files does not matter.
func applyDeletes(recs []*xedRecord, unames, iclasses map[string]bool) []*xedRecord {
	out := recs[:0:0]
	for _, r := range recs {
		if iclasses[r.IClass] || (r.UName != "" && unames[r.UName]) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// parseWidths reads an operand-width table: "name xtype w16 [w32 w64]", where
// each value is bytes, or bits when suffixed with "bits".
func parseWidths(r io.Reader, into map[string]width) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(stripComment(sc.Text()))
		if len(f) < 3 {
			continue
		}
		w := width{XType: f[1]}
		for _, v := range f[2:] {
			b, err := parseWidthValue(v)
			if err != nil {
				return fmt.Errorf("width %s: %w", f[0], err)
			}
			w.Bits = append(w.Bits, b)
		}
		into[f[0]] = w
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read widths: %w", err)
	}
	return nil
}

func parseWidthValue(v string) (int, error) {
	if s, ok := strings.CutSuffix(v, "bits"); ok {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("bad bit width %q: %w", v, err)
		}
		return n, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("bad byte width %q: %w", v, err)
	}
	return n * 8, nil
}

// parsedISA is the content of one instruction datafile.
type parsedISA struct {
	Records  []*xedRecord
	UDeletes []string
	Deletes  []string
}

// parseISA reads one XED instruction file. Only the parts the generator
// needs are interpreted: { } records, UDELETE and DELETE. Nonterminal
// headers ("INSTRUCTIONS()::") are skipped; every { } block in an
// instruction file is an instruction regardless of which header it sits
// under.
func parseISA(r io.Reader, name string) (*parsedISA, error) {
	out := &parsedISA{}
	lines, err := logicalLines(r)
	if err != nil {
		return nil, err
	}
	var hdr *xedRecord // record-level keys of the open block
	var cur *xedRecord // current pattern group within the block
	var group []*xedRecord
	for _, raw := range lines {
		line := stripComment(raw)
		if line == "" {
			continue
		}
		if hdr == nil {
			switch {
			case line == "{":
				hdr = &xedRecord{File: name, CPL: 3}
				cur = nil
				group = nil
			case strings.HasPrefix(line, "UDELETE"):
				if v, ok := keyValue(line); ok {
					out.UDeletes = append(out.UDeletes, v)
				}
			case strings.HasPrefix(line, "DELETE"):
				if v, ok := keyValue(line); ok {
					out.Deletes = append(out.Deletes, v)
				}
			}
			continue
		}
		if line == "}" {
			for _, g := range group {
				out.Records = append(out.Records, finishRecord(hdr, g))
			}
			hdr, cur, group = nil, nil, nil
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "PATTERN":
			cur = &xedRecord{Pattern: strings.Fields(val)}
			group = append(group, cur)
		case "OPERANDS":
			if cur == nil {
				return nil, fmt.Errorf("%s: OPERANDS before PATTERN in %s", name, hdr.IClass)
			}
			cur.Operands = strings.Fields(val)
		case "IFORM":
			if cur != nil {
				cur.IForm = val
			}
		case "ICLASS":
			hdr.IClass = val
		case "UNAME":
			hdr.UName = val
		case "CATEGORY":
			hdr.Category = val
		case "EXTENSION":
			hdr.Extension = val
		case "ISA_SET":
			hdr.ISASet = val
		case "CPL":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: bad CPL %q: %w", name, hdr.IClass, val, err)
			}
			hdr.CPL = n
		case "ATTRIBUTES":
			hdr.Attributes = append(hdr.Attributes, strings.Fields(val)...)
		case "FLAGS":
			hdr.Flags = val
		}
	}
	if hdr != nil {
		return nil, fmt.Errorf("%s: unterminated record %s", name, hdr.IClass)
	}
	return out, nil
}

// finishRecord merges the block header into one pattern group. XED defaults
// ISA_SET to the EXTENSION when a record does not name one.
func finishRecord(hdr, g *xedRecord) *xedRecord {
	r := *hdr
	r.Pattern = g.Pattern
	r.Operands = g.Operands
	r.IForm = g.IForm
	if r.ISASet == "" {
		r.ISASet = r.Extension
	}
	return &r
}

// logicalLines joins backslash-continued lines, which XED uses for a few
// long OPERANDS lists.
func logicalLines(r io.Reader) ([]string, error) {
	var out []string
	var pending strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		t := sc.Text()
		trimmed := strings.TrimRight(t, " \t\r")
		if strings.HasSuffix(trimmed, "\\") && !strings.Contains(trimmed, "#") {
			pending.WriteString(strings.TrimSuffix(trimmed, "\\"))
			pending.WriteByte(' ')
			continue
		}
		pending.WriteString(t)
		out = append(out, pending.String())
		pending.Reset()
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read isa: %w", err)
	}
	if pending.Len() > 0 {
		out = append(out, pending.String())
	}
	return out, nil
}

func keyValue(line string) (string, bool) {
	_, v, ok := strings.Cut(line, ":")
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}
