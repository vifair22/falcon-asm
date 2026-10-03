package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// asmDocs is what the generator takes from asm-lsp's docs_store: one-line
// summaries keyed by upper-case Intel mnemonic, and the GAS spellings each
// mnemonic's forms are written with.
type asmDocs struct {
	Summary  map[string]string
	GasNames map[string][]string
}

func newAsmDocs() *asmDocs {
	return &asmDocs{Summary: map[string]string{}, GasNames: map[string][]string{}}
}

// loadAsmLSP reads the opcodes XML files (x86-64 first, so its summaries win
// over the 32-bit file's) and then the felixcloutier-derived instruction
// index, which fills in the system and privileged instructions the
// user-mode opcodes database leaves out.
func loadAsmLSP(root string) (*asmDocs, error) {
	d := newAsmDocs()
	store := filepath.Join(root, "docs_store")
	for _, name := range []string{"x86_64.xml", "x86.xml"} {
		f, err := os.Open(filepath.Join(store, "opcodes", name))
		if err != nil {
			return nil, fmt.Errorf("open asm-lsp opcodes: %w", err)
		}
		err = d.readOpcodesXML(f)
		// Read-only: a close error cannot lose data, and the read error
		// above is the one that matters.
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	f, err := os.Open(filepath.Join(store, "instr_info_cache", "x86_instr_docs.html"))
	if err != nil {
		return nil, fmt.Errorf("open asm-lsp instruction index: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only, see above
	if err := d.readIndexHTML(f); err != nil {
		return nil, fmt.Errorf("x86_instr_docs.html: %w", err)
	}
	return d, nil
}

// readOpcodesXML streams an <InstructionSet> document in the PeachPy
// "opcodes" schema, keeping only names, summaries and gas-name attributes.
// Existing summaries are not overwritten, which is how file precedence is
// expressed.
func (d *asmDocs) readOpcodesXML(r io.Reader) error {
	dec := xml.NewDecoder(r)
	var cur string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "Instruction":
			cur = strings.ToUpper(attr(se, "name"))
			if s := attr(se, "summary"); s != "" && cur != "" {
				if _, have := d.Summary[cur]; !have {
					d.Summary[cur] = cleanText(s)
				}
			}
		case "InstructionForm":
			g := attr(se, "gas-name")
			if g == "" || cur == "" {
				continue
			}
			if !contains(d.GasNames[cur], g) {
				d.GasNames[cur] = append(d.GasNames[cur], g)
			}
		}
	}
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

var indexRowRE = regexp.MustCompile(`<tr><td><a href='[^']*'>([^<]*)</a></td><td>([^<]*)</td>`)

// readIndexHTML scrapes the mnemonic/summary table. Entries are only added
// where the XML has nothing: the index is coarser (one summary per SDM
// page, so MOVSD there is the string instruction, not the SSE one).
func (d *asmDocs) readIndexHTML(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read html: %w", err)
	}
	for _, m := range indexRowRE.FindAllStringSubmatch(string(b), -1) {
		name := strings.ToUpper(strings.TrimSpace(html.UnescapeString(m[1])))
		sum := cleanText(html.UnescapeString(m[2]))
		if name == "" || sum == "" {
			continue
		}
		if _, have := d.Summary[name]; !have {
			d.Summary[name] = sum
		}
	}
	return nil
}

// condCodes are the XED condition-code spellings used in Jcc, SETcc and
// CMOVcc iclass names, which the instruction index files under one "cc"
// entry per family.
var condCodes = []string{"B", "NB", "BE", "NBE", "L", "NL", "LE", "NLE", "O", "NO", "P", "NP", "S", "NS", "Z", "NZ"}

// lookup finds a summary for an upper-case mnemonic, falling back to the
// family entry ("Jcc") for condition-code instructions.
func (d *asmDocs) lookup(name string) (string, bool) {
	if s, ok := d.Summary[name]; ok {
		return s, true
	}
	for _, fam := range []string{"CMOV", "SET", "J"} {
		rest, ok := strings.CutPrefix(name, fam)
		if !ok {
			continue
		}
		if contains(condCodes, rest) {
			if s, ok := d.Summary[fam+"CC"]; ok {
				return s, true
			}
		}
	}
	return "", false
}

// asciiMap transliterates the non-ASCII punctuation that appears in
// upstream text. Anything else outside ASCII is dropped: the project rule
// is pure ASCII in every file, including generated data.
var asciiMap = map[rune]string{
	'\u2018': "'", '\u2019': "'", '\u201c': `"`, '\u201d': `"`,
	'\u2013': "-", '\u2014': "-", '\u2026': "...", '\u00a0': " ",
	'\u2122': "", '\u00ae': "", '\u00d7': "x", '\u2192': "->",
}

// cleanText makes upstream prose safe for the generated file: ASCII only,
// single spaces, trimmed.
func cleanText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		if rep, ok := asciiMap[r]; ok {
			b.WriteString(rep)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
