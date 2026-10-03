package hwdb

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// MSR is one model-specific register, or a run of them (Last, Stride) such as
// the variable-range MTRR pairs. Name may hold "{n}", replaced by the
// register's position in the run.
//
// Reserved is the mask of bits that must be written as zero (a set bit makes
// wrmsr #GP). It is only filled where every vendor and generation agrees;
// bits that newer parts define (EFER above bit 15 on AMD, SPEC_CTRL above
// bit 2) are left out of it, so the reserved-bit check never flags code
// written for a newer CPU than the table knows.
type MSR struct {
	Index      Num      `json:"index"`
	Last       Num      `json:"last,omitempty"`
	Stride     Num      `json:"stride,omitempty"`
	Name       string   `json:"name"`
	Alias      string   `json:"alias,omitempty"` // common short name, "EFER"
	Vendor     string   `json:"vendor,omitempty"`
	Access     string   `json:"access,omitempty"` // "ro", "wo", "" = read/write
	Detail     string   `json:"detail,omitempty"`
	Like       Num      `json:"like,omitempty"` // copy Fields/Reserved from this index
	Fields     []Field  `json:"fields,omitempty"`
	Reserved   Num      `json:"reserved,omitempty"`
	Notes      []string `json:"notes,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
}

// NameFor is the register's name at index, with "{n}" filled in.
func (m *MSR) NameFor(index uint64) string {
	if !strings.Contains(m.Name, "{n}") {
		return m.Name
	}
	stride := uint64(m.Stride)
	if stride == 0 {
		stride = 1
	}
	n := (index - uint64(m.Index)) / stride
	return strings.ReplaceAll(m.Name, "{n}", strconv.FormatUint(n, 10))
}

// covers reports whether index falls in the entry.
func (m *MSR) covers(index uint64) bool {
	if m.Last == 0 {
		return index == uint64(m.Index)
	}
	if index < uint64(m.Index) || index > uint64(m.Last) {
		return false
	}
	stride := uint64(m.Stride)
	return stride <= 1 || (index-uint64(m.Index))%stride == 0
}

type msrFile struct {
	Enums map[string][]Enum `json:"enums"`
	MSRs  []MSR             `json:"msrs"`
}

type msrTable struct {
	exact  map[uint64]*MSR
	ranges []*MSR // checked in order after exact
}

func loadMSRs() (*msrTable, error) {
	var f msrFile
	if err := readJSON("msrs.json", &f); err != nil {
		return &msrTable{exact: map[uint64]*MSR{}}, err
	}
	t, err := buildMSRs(f.MSRs, f.Enums, "msrs.json")
	if err != nil {
		return &msrTable{exact: map[uint64]*MSR{}}, err
	}
	return t, nil
}

func finishMSR(m *MSR, enums map[string][]Enum, path string) error {
	if m.Name == "" {
		return fmt.Errorf("%s.name: empty", path)
	}
	if m.Index > 0xFFFFFFFF || m.Last > 0xFFFFFFFF {
		return fmt.Errorf("%s.index: %#x is not a 32-bit MSR index", path, uint64(m.Index))
	}
	if m.Last != 0 && m.Last < m.Index {
		return fmt.Errorf("%s.last: before index", path)
	}
	switch m.Access {
	case "", "ro", "wo":
	default:
		return fmt.Errorf("%s.access: %q, want \"ro\", \"wo\" or empty", path, m.Access)
	}
	if err := resolveEnums(m.Fields, enums, path+".fields"); err != nil {
		return err
	}
	used, err := finishFields(m.Fields, 64, path+".fields")
	if err != nil {
		return err
	}
	if used&uint64(m.Reserved) != 0 {
		return fmt.Errorf("%s.reserved: %#x overlaps defined fields (%#x)", path, uint64(m.Reserved), used&uint64(m.Reserved))
	}
	return nil
}

func buildMSRs(list []MSR, enums map[string][]Enum, file string) (*msrTable, error) {
	t := &msrTable{exact: make(map[uint64]*MSR, len(list))}
	byIndex := map[uint64]*MSR{}
	for i := range list {
		byIndex[uint64(list[i].Index)] = &list[i]
	}
	for i := range list {
		m := &list[i]
		path := fmt.Sprintf("%s: msrs[%d]", file, i)
		if m.Like != 0 {
			src, ok := byIndex[uint64(m.Like)]
			if !ok || src.Like != 0 {
				return nil, fmt.Errorf("%s.like: %#x is not a defined MSR without its own like", path, uint64(m.Like))
			}
			if m.Fields == nil {
				m.Fields = append([]Field(nil), src.Fields...)
			}
			if m.Reserved == 0 {
				m.Reserved = src.Reserved
			}
		}
		if err := finishMSR(m, enums, path); err != nil {
			return nil, err
		}
		if m.Last != 0 {
			for _, r := range t.ranges {
				if rangesClash(m, r) {
					return nil, fmt.Errorf("%s: range overlaps %s without nesting inside it", path, r.Name)
				}
			}
			t.ranges = append(t.ranges, m)
			continue
		}
		if prev, dup := t.exact[uint64(m.Index)]; dup {
			return nil, fmt.Errorf("%s: index %#x already defined as %s", path, uint64(m.Index), prev.Name)
		}
		t.exact[uint64(m.Index)] = m
	}
	// Narrow ranges first, so a specific run inside a summary range (the
	// x2APIC ISR block inside 0x800-0x8FF) wins.
	sort.SliceStable(t.ranges, func(i, j int) bool {
		return t.ranges[i].Last-t.ranges[i].Index < t.ranges[j].Last-t.ranges[j].Index
	})
	return t, nil
}

// LookupMSR finds the register at index: an exact entry, else the narrowest
// range covering it.
func (d *DB) LookupMSR(index uint64) (*MSR, bool) {
	t := d.msrTab()
	if m, ok := t.exact[index]; ok {
		return m, true
	}
	for _, m := range t.ranges {
		if m.covers(index) {
			return m, true
		}
	}
	return nil, false
}

// MSRReserved is the reserved-bit mask for index, and whether the MSR is
// known at all.
func (d *DB) MSRReserved(index uint64) (uint64, bool) {
	m, ok := d.LookupMSR(index)
	if !ok {
		return 0, false
	}
	return uint64(m.Reserved), true
}

// CheckMSRWrite lists problems with writing value to index: reserved bits
// set, a write to a read-only MSR, a reserved PAT or MTRR memory type. An
// unknown MSR yields nothing; rule 53 reports that separately, as
// Information.
func (d *DB) CheckMSRWrite(index, value uint64) []string {
	m, ok := d.LookupMSR(index)
	if !ok {
		return nil
	}
	var out []string
	name := m.NameFor(index)
	if m.Access == "ro" {
		out = append(out, name+" is read-only: wrmsr raises #GP")
	}
	if r := value & uint64(m.Reserved); r != 0 {
		out = append(out, fmt.Sprintf("reserved bits %#x set in %s: wrmsr raises #GP", r, name))
	}
	for i := range m.Fields {
		f := &m.Fields[i]
		if f.RO || !f.Strict {
			continue
		}
		if x := f.Get(value); f.ValueName(x) == "" {
			out = append(out, fmt.Sprintf("%s.%s = %d is a reserved memory type: wrmsr raises #GP", name, f.Name, x))
		}
	}
	return out
}

// DecodeMSR decodes value as the contents of MSR index.
func (d *DB) DecodeMSR(index, value uint64) (*Decoded, bool) {
	m, ok := d.LookupMSR(index)
	if !ok {
		return nil, false
	}
	dec := &Decoded{Kind: "msr", Value: value, Fields: setFields(m.Fields, value),
		Reserved: value & uint64(m.Reserved)}
	dec.Summary = summarize(m.NameFor(index), dec.Fields)
	dec.Problems = d.CheckMSRWrite(index, value)
	if m.Access == "ro" && len(dec.Problems) > 0 {
		dec.Problems = dec.Problems[1:] // decoding a read value is not a write
	}
	return dec, true
}

// rangesClash reports two range entries that share an index without one
// nesting strictly inside the other. Nesting is how a specific run (the
// x2APIC ISR registers) refines a summary range (all of 0x800-0x8FF);
// anything else is a table mistake.
func rangesClash(a, b *MSR) bool {
	shared := false
	for i := uint64(a.Index); i <= uint64(a.Last); i++ {
		if a.covers(i) && b.covers(i) {
			shared = true
			break
		}
	}
	if !shared {
		return false
	}
	aIn := a.Index >= b.Index && a.Last <= b.Last
	bIn := b.Index >= a.Index && b.Last <= a.Last
	return aIn == bIn
}
