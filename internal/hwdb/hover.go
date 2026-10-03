package hwdb

import (
	"fmt"
	"strings"
)

// The hover renderers follow internal/hover's conventions: a bold title
// with " - " before the summary, paragraphs separated by blank lines (a
// single newline is a soft break), values in code spans, and every piece of
// table text escaped so names like __start or *ptr render literally.

const mdSpecial = "\\`*_[]<>#|~"

func mdEscape(s string) string {
	if !strings.ContainsAny(s, mdSpecial) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(mdSpecial, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mdCode is a code span; table text never contains backticks except in
// details that already use them, which are passed through mdText instead.
func mdCode(s string) string {
	if strings.Contains(s, "`") {
		return "``" + s + "``"
	}
	return "`" + s + "`"
}

func mdBold(s string) string { return "**" + mdEscape(s) + "**" }

// mdText escapes table prose but keeps `code` spans the tables write
// deliberately (`outb %al, $0x80`).
func mdText(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		return mdEscape(s)
	}
	for i := range parts {
		if i%2 == 0 {
			parts[i] = mdEscape(parts[i])
		} else {
			parts[i] = "`" + parts[i] + "`"
		}
	}
	return strings.Join(parts, "")
}

func mdJoin(lines []string) string {
	var kept []string
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n\n")
}

func mdList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "- " + strings.Join(items, "\n- ")
}

func bitsLabel(f *Field) string {
	if f.Lo == f.Hi {
		return fmt.Sprintf("%d", f.Lo)
	}
	return fmt.Sprintf("%d:%d", f.Hi, f.Lo)
}

// fieldItems renders a bit layout, one item per field.
func fieldItems(fs []Field) []string {
	var out []string
	for i := range fs {
		f := &fs[i]
		s := mdCode(bitsLabel(f)) + " " + mdEscape(f.Name)
		if f.Desc != "" {
			s += " - " + mdText(f.Desc)
		}
		if len(f.Values) > 0 && len(f.Values) <= 8 {
			var vs []string
			for _, e := range f.Values {
				vs = append(vs, fmt.Sprintf("%d %s", uint64(e.V), mdEscape(e.Name)))
			}
			s += " (" + strings.Join(vs, ", ") + ")"
		}
		if f.RO {
			s += " (read-only)"
		}
		out = append(out, s)
	}
	return out
}

func valuesLine(label string, vs []Enum) string {
	if len(vs) == 0 {
		return ""
	}
	if label == "" {
		label = "Values"
	}
	var parts []string
	for _, e := range vs {
		parts = append(parts, mdCode(fmt.Sprintf("0x%02X", uint64(e.V)))+" "+mdEscape(e.Name))
	}
	return mdEscape(label) + ": " + strings.Join(parts, "; ")
}

func notesBlock(prefix string, notes []string) string {
	var items []string
	for _, n := range notes {
		items = append(items, mdText(n))
	}
	if len(items) == 0 {
		return ""
	}
	if prefix != "" {
		return prefix + "\n\n" + mdList(items)
	}
	return mdList(items)
}

func widthsText(p *Port) string {
	if len(p.Widths) == 0 {
		return "8-bit"
	}
	var ws []string
	for _, w := range p.Widths {
		ws = append(ws, fmt.Sprintf("%d", w))
	}
	return strings.Join(ws, "/") + "-bit"
}

func accessText(p *Port) string {
	switch p.Access {
	case "r":
		return "read"
	case "w":
		return "write"
	}
	return "read/write"
}

// PortHover renders a port for an `in` (write false) or `out` (write true)
// of width bits (0 = unknown). The register the access reaches comes first;
// other registers at the same port (the other direction, DLAB variants)
// follow as one line each. Returns "" for an unknown port.
func (d *DB) PortHover(port uint64, width int, write bool) string {
	hits := d.LookupPort(port)
	if len(hits) == 0 {
		return ""
	}
	main, ok := d.PortFor(port, write)
	if !ok {
		main = hits[0]
	}
	r := main.Reg
	title := mdBold(fmt.Sprintf("0x%X", port)) + " - " + mdEscape(main.Name())
	if r.Long != "" {
		title += " (" + mdEscape(r.Long) + ")"
	}
	meta := []string{}
	if main.Block != nil {
		meta = append(meta, mdEscape(main.Block.Device)+fmt.Sprintf(" at base 0x%X + %d", port-uint64(r.Port), uint64(r.Port)))
	} else if r.Device != "" {
		meta = append(meta, mdEscape(r.Device))
	}
	meta = append(meta, widthsText(r), accessText(r))
	if r.Cond != "" {
		meta = append(meta, "when "+mdEscape(r.Cond))
	}
	lines := []string{title, strings.Join(meta, " - ")}

	var warn []string
	if width > 0 && !r.AllowsWidth(width) {
		warn = append(warn, fmt.Sprintf("%d-bit access; the register is %s", width, widthsText(r)))
	}
	if write && !r.Writable() {
		warn = append(warn, "read-only register: the write has no defined effect")
	}
	if !write && !r.Readable() {
		warn = append(warn, "write-only register: the read returns an undefined value")
	}
	if len(warn) > 0 {
		lines = append(lines, "Warning: "+strings.Join(warn, "; "))
	}
	if r.Detail != "" {
		lines = append(lines, mdText(r.Detail))
	}
	if len(r.Fields) > 0 {
		lines = append(lines, "Bits:\n\n"+mdList(fieldItems(r.Fields)))
	}
	lines = append(lines, valuesLine(r.ValuesName, r.Values))
	var others []string
	for _, h := range hits {
		if h.Reg == r {
			continue
		}
		o := mdEscape(h.Name()) + " (" + accessText(h.Reg)
		if h.Reg.Cond != "" {
			o += ", " + mdEscape(h.Reg.Cond)
		}
		o += ")"
		if h.Reg.Long != "" {
			o += " - " + mdEscape(h.Reg.Long)
		}
		others = append(others, o)
	}
	if len(others) > 0 {
		lines = append(lines, "Also at this port: "+strings.Join(others, "; "))
	}
	notes := append([]string(nil), r.Notes...)
	if main.Block != nil {
		notes = append(notes, main.Block.Notes...)
	}
	lines = append(lines, notesBlock("Notes:", notes))
	if r.Confidence != "" {
		lines = append(lines, "_Confidence: "+mdEscape(r.Confidence)+"_")
	}
	return mdJoin(lines)
}

// MSRHover renders an MSR, and the decoded value when one is known.
func (d *DB) MSRHover(index uint64, value *uint64) string {
	m, ok := d.LookupMSR(index)
	if !ok {
		return ""
	}
	title := mdBold(fmt.Sprintf("MSR 0x%X", index)) + " - " + mdEscape(m.NameFor(index))
	if m.Alias != "" {
		title += " (" + mdEscape(m.Alias) + ")"
	}
	var meta []string
	if m.Vendor != "" {
		meta = append(meta, strings.ToUpper(m.Vendor[:1])+m.Vendor[1:]+" only")
	}
	switch m.Access {
	case "ro":
		meta = append(meta, "read-only")
	case "wo":
		meta = append(meta, "write-only")
	}
	lines := []string{title, strings.Join(meta, " - "), mdText(m.Detail)}
	if value != nil {
		dec, _ := d.DecodeMSR(index, *value)
		lines = append(lines, "Value "+mdCode(fmt.Sprintf("0x%X", *value))+": "+mdEscape(strings.TrimPrefix(dec.Summary, m.NameFor(index)+": ")))
		if len(dec.Problems) > 0 {
			lines = append(lines, "Warning: "+mdEscape(strings.Join(dec.Problems, "; ")))
		}
	}
	if len(m.Fields) > 0 {
		lines = append(lines, "Bits:\n\n"+mdList(fieldItems(m.Fields)))
	}
	if m.Reserved != 0 {
		lines = append(lines, "Reserved (must be 0): "+mdCode(fmt.Sprintf("0x%X", uint64(m.Reserved))))
	}
	lines = append(lines, notesBlock("Notes:", m.Notes))
	if m.Confidence != "" {
		lines = append(lines, "_Confidence: "+mdEscape(m.Confidence)+"_")
	}
	return mdJoin(lines)
}

var errText = map[string]string{
	ErrCFAH:     "CF set on error, AH = status",
	ErrCF:       "CF set on error",
	ErrAH:       "AH = status",
	ErrZF:       "ZF set = nothing available",
	ErrVBE:      "AX != 004Fh on error (AL = 4Fh: supported, AH = status)",
	ErrAH7:      "AH bit 7 set = timeout",
	ErrNone:     "cannot fail",
	ErrNoReturn: "does not return",
}

func pairItems(ps [][2]string) []string {
	var out []string
	for _, p := range ps {
		out = append(out, mdCode(p[0])+" "+mdText(p[1]))
	}
	return out
}

// BIOSHover renders a BIOS service: INT vector with AH, and AL when known.
// When AL is unknown and selects among several services under AH, the hover
// lists them. A known vector with an unknown function shows the vector.
func (d *DB) BIOSHover(vector, ah, al uint64, alKnown bool) string {
	v, vok := d.BIOSVectorInfo(vector)
	s, ok := d.LookupBIOS(vector, ah, al, alKnown)
	if !ok {
		if g := d.biosTab().group(vector, ah); len(g) > 0 {
			var items []string
			for _, gs := range g {
				items = append(items, mdCode(fmt.Sprintf("AX=%02X%02Xh", ah, uint64(*gs.AL)))+" "+mdEscape(gs.Name))
			}
			return mdJoin([]string{mdBold(fmt.Sprintf("INT %02Xh AH=%02Xh", vector, ah)) + " - AL selects the function", mdList(items)})
		}
		if !vok {
			return ""
		}
		return mdJoin([]string{mdBold(fmt.Sprintf("INT %02Xh", vector)) + " - " + mdEscape(v.Name),
			fmt.Sprintf("Function AH=%02Xh is not in the table.", ah&0xFF), mdText(v.Detail), notesBlock("Quirks:", v.Quirks)})
	}
	title := mdBold(s.Key()) + " - " + mdEscape(s.Name)
	if vok {
		title += " (" + mdEscape(v.Name) + ")"
	}
	lines := []string{title}
	if s.Since != "" {
		lines = append(lines, "Since: "+mdEscape(s.Since))
	}
	if len(s.In) > 0 {
		lines = append(lines, "In:\n\n"+mdList(pairItems(s.In)))
	}
	if len(s.Out) > 0 {
		lines = append(lines, "Out:\n\n"+mdList(pairItems(s.Out)))
	}
	if len(s.Fields) > 0 {
		lines = append(lines, "Result bits:\n\n"+mdList(fieldItemsReg(s.Fields)))
	}
	if e := errText[s.Err]; e != "" {
		er := "Errors: " + mdEscape(e)
		if s.ErrNote != "" {
			er += ". " + mdText(s.ErrNote)
		}
		lines = append(lines, er)
	}
	var cl []string
	if len(s.Clobbers) > 0 {
		cl = append(cl, "destroys "+mdEscape(strings.Join(s.Clobbers, ", ")))
	}
	may := append(append([]string(nil), s.MayClobber...), vectorMay(v, vok)...)
	if len(may) > 0 {
		cl = append(cl, "may destroy "+mdEscape(strings.Join(dedupe(may), ", "))+" on some BIOSes")
	}
	if len(cl) > 0 {
		lines = append(lines, "Registers: "+strings.Join(cl, "; "))
	}
	if s.IF != "" {
		lines = append(lines, "Interrupts: "+mdText(s.IF))
	}
	for _, name := range s.Struct {
		if st, ok := d.Struct(name); ok {
			lines = append(lines, structSummary(st))
		}
	}
	quirks := append([]string(nil), s.Quirks...)
	if vok {
		quirks = append(quirks, v.Quirks...)
	}
	lines = append(lines, notesBlock("Quirks:", quirks))
	if s.Confidence != "" {
		lines = append(lines, "_Confidence: "+mdEscape(s.Confidence)+"_")
	}
	return mdJoin(lines)
}

func vectorMay(v *BIOSVector, ok bool) []string {
	if !ok {
		return nil
	}
	return v.MayClobber
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		k := strings.ToLower(x)
		if !seen[k] {
			seen[k] = true
			out = append(out, x)
		}
	}
	return out
}

func fieldItemsReg(fs []Field) []string {
	items := fieldItems(fs)
	for i := range fs {
		items[i] = mdEscape(fs[i].Reg) + " " + items[i]
	}
	return items
}

// structSummary is the layout of a BIOS structure as a compact table.
func structSummary(st *BIOSStruct) string {
	var sizes []string
	for _, s := range st.Sizes {
		sizes = append(sizes, fmt.Sprintf("%d", s))
	}
	var rows []string
	for _, f := range st.Fields {
		r := fmt.Sprintf("| %d | %d | %s | %s |", f.Off, f.Size, mdEscape(f.Name), mdText(f.Desc))
		rows = append(rows, r)
	}
	return mdBold(st.Title) + " (" + strings.Join(sizes, " or ") + " bytes)\n\n| off | size | field | meaning |\n|---|---|---|---|\n" + strings.Join(rows, "\n")
}

// StructHover renders a BIOS structure's layout ("dap", "e820").
func (d *DB) StructHover(name string) string {
	st, ok := d.Struct(name)
	if !ok {
		return ""
	}
	return mdJoin([]string{structSummary(st), notesBlock("Notes:", st.Notes)})
}

// CPUIDHover renders a CPUID leaf: each output register's fields, feature
// bits annotated with the x86-64 level that requires them.
func (d *DB) CPUIDHover(leaf, subleaf uint64) string {
	l, ok := CPUID(leaf, subleaf)
	if !ok {
		return ""
	}
	lines := []string{mdBold(l.Key()) + " - " + mdEscape(l.Name), mdText(l.Detail)}
	for i, fs := range l.Regs() {
		if len(fs) == 0 {
			continue
		}
		var parts []string
		for j := range fs {
			f := &fs[j]
			p := mdCode(bitsLabel(f)) + " " + mdEscape(f.Name)
			if f.Level > 1 {
				p += fmt.Sprintf(" (v%d)", f.Level)
			}
			parts = append(parts, p)
		}
		lines = append(lines, [4]string{"EAX", "EBX", "ECX", "EDX"}[i]+": "+strings.Join(parts, ", "))
	}
	lines = append(lines, notesBlock("Notes:", l.Notes))
	return mdJoin(lines)
}

// DecodeHover renders Decode(kind, value): the summary, the set fields and
// any problems. Returns "" for an unknown kind.
func (d *DB) DecodeHover(kind string, value uint64) string {
	dec, ok := Decode(kind, value)
	if !ok {
		return ""
	}
	return decodedMarkdown(dec)
}

func decodedMarkdown(dec *Decoded) string {
	lines := []string{mdBold(dec.Summary), mdCode(fmt.Sprintf("0x%X", dec.Value))}
	if len(dec.Problems) > 0 {
		lines = append(lines, "Warning:\n\n"+mdList(escapeAll(dec.Problems)))
	}
	lines = append(lines, notesBlock("Notes:", dec.Notes))
	return mdJoin(lines)
}

func escapeAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = mdEscape(x)
	}
	return out
}
