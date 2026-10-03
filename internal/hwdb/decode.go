package hwdb

import (
	"fmt"
	"sort"
	"strings"
)

// Decoded is a value taken apart: the set fields, any reserved bits that are
// set, and problems that make the value invalid where it is used (CR0.PG
// without PE, a code descriptor with both L and D).
type Decoded struct {
	Kind     string
	Value    uint64
	Summary  string // one line: "CR0: PE PG" or "code32 base=0 limit=0xFFFFF G ..."
	Fields   []FieldValue
	Reserved uint64 // reserved bits that are set
	Problems []string
	Notes    []string // facts worth showing that are not problems
}

// RegLayout is a register's bit layout from data/regs.json.
type RegLayout struct {
	Name     string  `json:"name"`
	Title    string  `json:"title"`
	Width    int     `json:"width"`
	Reserved Num     `json:"reserved,omitempty"`
	Fields   []Field `json:"fields"`
}

type regFile struct {
	Regs []RegLayout `json:"regs"`
}

type regTable struct {
	byName map[string]*RegLayout
}

func loadRegs() (*regTable, error) {
	var f regFile
	if err := readJSON("regs.json", &f); err != nil {
		return &regTable{byName: map[string]*RegLayout{}}, err
	}
	t, err := buildRegs(f, "regs.json")
	if err != nil {
		return &regTable{byName: map[string]*RegLayout{}}, err
	}
	return t, nil
}

func buildRegs(f regFile, file string) (*regTable, error) {
	t := &regTable{byName: map[string]*RegLayout{}}
	for i := range f.Regs {
		r := &f.Regs[i]
		path := fmt.Sprintf("%s: regs[%d]", file, i)
		if r.Width <= 0 || r.Width > 64 {
			return nil, fmt.Errorf("%s.width: %d", path, r.Width)
		}
		used, err := finishFields(r.Fields, r.Width, path+".fields")
		if err != nil {
			return nil, err
		}
		if used&uint64(r.Reserved) != 0 {
			return nil, fmt.Errorf("%s.reserved: overlaps fields at %#x", path, used&uint64(r.Reserved))
		}
		if _, dup := t.byName[r.Name]; dup {
			return nil, fmt.Errorf("%s: duplicate %q", path, r.Name)
		}
		t.byName[r.Name] = r
	}
	return t, nil
}

// Register returns the bit layout of a control register, XCR0, RFLAGS or a
// selector ("cr0", "cr3-pcid", "xcr0").
func Register(name string) (*RegLayout, bool) {
	r, ok := regTab().byName[strings.ToLower(name)]
	return r, ok
}

// decoders maps a kind to its decoder. Register layouts from regs.json are
// decoded generically and then checked by an optional validity function.
var decoders = map[string]func(v uint64) *Decoded{
	"cr0":        func(v uint64) *Decoded { return decodeReg("cr0", v, checkCR0) },
	"cr3":        func(v uint64) *Decoded { return decodeReg("cr3", v, nil) },
	"cr3-pcid":   func(v uint64) *Decoded { return decodeReg("cr3-pcid", v, nil) },
	"cr3-pae":    func(v uint64) *Decoded { return decodeReg("cr3-pae", v, nil) },
	"cr3-32":     func(v uint64) *Decoded { return decodeReg("cr3-32", v, nil) },
	"cr4":        func(v uint64) *Decoded { return decodeReg("cr4", v, nil) },
	"xcr0":       func(v uint64) *Decoded { return decodeReg("xcr0", v, checkXCR0) },
	"rflags":     func(v uint64) *Decoded { return decodeReg("rflags", v, checkRFLAGS) },
	"eflags":     func(v uint64) *Decoded { return decodeReg("rflags", v&0xFFFFFFFF, checkRFLAGS) },
	"selector":   func(v uint64) *Decoded { return decodeReg("selector", v, checkSelector) },
	"efer":       decodeEFER,
	"pci-config": decodePCIAddress,
	"descriptor": func(v uint64) *Decoded { return DecodeDescriptor(v, nil, false) },
	"gate32":     func(v uint64) *Decoded { return DecodeGate(v, nil) },
	"gate64":     func(v uint64) *Decoded { h := uint64(0); return DecodeGate(v, &h) },
	"pml5e":      func(v uint64) *Decoded { return DecodePTE("pml5e", v) },
	"pml4e":      func(v uint64) *Decoded { return DecodePTE("pml4e", v) },
	"pdpte":      func(v uint64) *Decoded { return DecodePTE("pdpte", v) },
	"pde":        func(v uint64) *Decoded { return DecodePTE("pde", v) },
	"pte":        func(v uint64) *Decoded { return DecodePTE("pte", v) },
	"pae-pdpte":  func(v uint64) *Decoded { return DecodePTE("pae-pdpte", v) },
	"pae-pde":    func(v uint64) *Decoded { return DecodePTE("pae-pde", v) },
	"pae-pte":    func(v uint64) *Decoded { return DecodePTE("pae-pte", v) },
	"pde32":      func(v uint64) *Decoded { return DecodePTE("pde32", v) },
	"pte32":      func(v uint64) *Decoded { return DecodePTE("pte32", v) },
}

// DecodeKinds lists the kinds Decode accepts, sorted.
func DecodeKinds() []string {
	out := make([]string, 0, len(decoders))
	for k := range decoders {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Decode takes value apart as kind (see DecodeKinds). Unknown kinds return
// false. "descriptor" decodes an 8-byte legacy descriptor; use
// DecodeDescriptor directly for the 16-byte long-mode system form.
func Decode(kind string, value uint64) (*Decoded, bool) {
	f, ok := decoders[strings.ToLower(kind)]
	if !ok {
		return nil, false
	}
	return f(value), true
}

func decodeReg(name string, v uint64, check func(*Decoded)) *Decoded {
	r, ok := Register(name)
	if !ok {
		return &Decoded{Kind: name, Value: v, Summary: name}
	}
	d := &Decoded{Kind: name, Value: v, Fields: setFields(r.Fields, v)}
	d.Reserved = v & uint64(r.Reserved)
	if r.Width < 64 {
		d.Reserved |= v &^ widthMask(r.Width)
	}
	if d.Reserved != 0 {
		d.Problems = append(d.Problems, fmt.Sprintf("reserved bits %#x set", d.Reserved))
	}
	d.Summary = summarize(r.Title, d.Fields)
	if check != nil {
		check(d)
	}
	return d
}

// summarize renders "TITLE: A B C=x", or "TITLE: (none)" for zero.
func summarize(title string, fs []FieldValue) string {
	if len(fs) == 0 {
		return title + ": (no bits set)"
	}
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Text
	}
	return title + ": " + strings.Join(parts, " ")
}

func bit(v uint64, n uint) bool { return v>>n&1 == 1 }

func checkCR0(d *Decoded) {
	v := d.Value
	if bit(v, 31) && !bit(v, 0) {
		d.Problems = append(d.Problems, "PG set without PE: mov to CR0 raises #GP")
	}
	if bit(v, 29) && !bit(v, 30) {
		d.Problems = append(d.Problems, "NW set without CD: mov to CR0 raises #GP")
	}
	if bit(v, 30) {
		d.Notes = append(d.Notes, "caches disabled (CD)")
	}
}

func decodeEFER(v uint64) *Decoded {
	d, ok := Default().DecodeMSR(0xC0000080, v)
	if !ok {
		return &Decoded{Kind: "efer", Value: v, Summary: "EFER"}
	}
	d.Kind = "efer"
	return d
}

func checkXCR0(d *Decoded) {
	v := d.Value
	if !bit(v, 0) {
		d.Problems = append(d.Problems, "X87 (bit 0) clear: xsetbv raises #GP")
	}
	if bit(v, 2) && !bit(v, 1) {
		d.Problems = append(d.Problems, "AVX without SSE: xsetbv raises #GP")
	}
	if bit(v, 3) != bit(v, 4) {
		d.Problems = append(d.Problems, "BNDREG and BNDCSR must be set together")
	}
	avx512 := v >> 5 & 7
	if avx512 != 0 && avx512 != 7 {
		d.Problems = append(d.Problems, "OPMASK, ZMM_HI256 and HI16_ZMM must be set together")
	}
	if avx512 == 7 && !bit(v, 2) {
		d.Problems = append(d.Problems, "AVX-512 state without AVX: xsetbv raises #GP")
	}
	if bit(v, 17) != bit(v, 18) {
		d.Problems = append(d.Problems, "XTILECFG and XTILEDATA must be set together")
	}
}

func checkRFLAGS(d *Decoded) {
	if !bit(d.Value, 1) {
		d.Notes = append(d.Notes, "bit 1 reads as 1; a popf ignores the written 0")
	}
}

func checkSelector(d *Decoded) {
	if d.Value&0xFFFC == 0 {
		d.Notes = append(d.Notes, "null selector: loading it into CS or SS faults, into DS/ES/FS/GS faults on first use")
	}
}

func decodePCIAddress(v uint64) *Decoded {
	f, _ := Default().PortFor(0xCF8, true)
	var fs []Field
	if f.Reg != nil {
		fs = f.Reg.Fields
	}
	d := &Decoded{Kind: "pci-config", Value: v, Fields: setFields(fs, v)}
	bus, dev, fn, reg := v>>16&0xFF, v>>11&0x1F, v>>8&7, v&0xFC
	d.Summary = fmt.Sprintf("PCI %02x:%02x.%d offset %#02x", bus, dev, fn, reg)
	if v>>24&0xF != 0 {
		d.Summary += fmt.Sprintf(" (extended %#x)", v>>24&0xF<<8|reg)
	}
	if !bit(v, 31) {
		d.Problems = append(d.Problems, "enable bit 31 clear: the 0xCFC access is not a configuration cycle")
	}
	if v&3 != 0 {
		d.Problems = append(d.Problems, "bits 1:0 set: they must be 0; byte offsets go in the 0xCFC-0xCFF port")
	}
	if v>>32 != 0 || v>>28&7 != 0 {
		d.Reserved = v &^ 0x8FFFFFFF
		d.Problems = append(d.Problems, fmt.Sprintf("reserved bits %#x set", d.Reserved))
	}
	return d
}

// System descriptor types, legacy (S=0).
var sysTypes32 = [16]string{
	0: "", 1: "16-bit TSS (available)", 2: "LDT", 3: "16-bit TSS (busy)",
	4: "16-bit call gate", 5: "task gate", 6: "16-bit interrupt gate", 7: "16-bit trap gate",
	8: "", 9: "32-bit TSS (available)", 10: "", 11: "32-bit TSS (busy)",
	12: "32-bit call gate", 13: "", 14: "32-bit interrupt gate", 15: "32-bit trap gate",
}

// In IA-32e mode the same type numbers mean the 64-bit forms and the 16-bit
// types are invalid.
var sysTypes64 = [16]string{
	2: "LDT", 9: "64-bit TSS (available)", 11: "64-bit TSS (busy)",
	12: "64-bit call gate", 14: "64-bit interrupt gate", 15: "64-bit trap gate",
}

// Descriptor is a decoded segment descriptor.
type Descriptor struct {
	Base   uint64
	Limit  uint32 // raw 20-bit limit
	Bytes  uint64 // effective limit in bytes, inclusive (G applied)
	Type   int
	S      bool // code/data (true) or system
	DPL    int
	P      bool
	AVL    bool
	L      bool
	DB     bool
	G      bool
	Code   bool
	TypeNm string
}

// ParseDescriptor splits the low 8 bytes of a descriptor (and the high 8 of
// a 16-byte long-mode system descriptor, when hi is non-nil).
func ParseDescriptor(lo uint64, hi *uint64) Descriptor {
	d := Descriptor{
		Base:  lo>>16&0xFFFFFF | (lo>>56&0xFF)<<24,
		Limit: uint32(lo&0xFFFF | (lo>>48&0xF)<<16),
		Type:  int(lo >> 40 & 0xF),
		S:     bit(lo, 44),
		DPL:   int(lo >> 45 & 3),
		P:     bit(lo, 47),
		AVL:   bit(lo, 52),
		L:     bit(lo, 53),
		DB:    bit(lo, 54),
		G:     bit(lo, 55),
	}
	if hi != nil {
		d.Base |= (*hi & 0xFFFFFFFF) << 32
	}
	d.Bytes = uint64(d.Limit)
	if d.G {
		d.Bytes = d.Bytes<<12 | 0xFFF
	}
	if d.S {
		d.Code = d.Type&8 != 0
		d.TypeNm = codeDataType(d.Type)
	} else if hi != nil {
		d.TypeNm = sysTypes64[d.Type]
	} else {
		d.TypeNm = sysTypes32[d.Type]
	}
	return d
}

func codeDataType(t int) string {
	var parts []string
	if t&8 != 0 {
		parts = append(parts, "code")
		if t&4 != 0 {
			parts = append(parts, "conforming")
		}
		if t&2 != 0 {
			parts = append(parts, "readable")
		} else {
			parts = append(parts, "execute-only")
		}
	} else {
		parts = append(parts, "data")
		if t&4 != 0 {
			parts = append(parts, "expand-down")
		}
		if t&2 != 0 {
			parts = append(parts, "writable")
		} else {
			parts = append(parts, "read-only")
		}
	}
	if t&1 != 0 {
		parts = append(parts, "accessed")
	}
	return strings.Join(parts, " ")
}

// DecodeDescriptor decodes a GDT/LDT entry. lo is the 8-byte descriptor;
// for a long-mode system descriptor (TSS, LDT, call gate) pass the second
// quadword in hi. longMode selects IA-32e interpretation of system types
// even without hi (to flag a 16-byte descriptor written as 8 bytes).
func DecodeDescriptor(lo uint64, hi *uint64, longMode bool) *Decoded {
	dec := &Decoded{Kind: "descriptor", Value: lo}
	if lo == 0 {
		dec.Summary = "null descriptor"
		return dec
	}
	d := ParseDescriptor(lo, hi)
	add := func(name string, v uint64, lo, hi int) {
		dec.Fields = append(dec.Fields, FieldValue{Name: name, Value: v, Text: fmt.Sprintf("%s=%#x", name, v), Lo: lo, Hi: hi})
	}
	flag := func(name string, set bool, at int) {
		if set {
			dec.Fields = append(dec.Fields, FieldValue{Name: name, Value: 1, Text: name, Lo: at, Hi: at})
		}
	}
	add("BASE", d.Base, 16, 63)
	add("LIMIT", uint64(d.Limit), 0, 51)
	add("TYPE", uint64(d.Type), 40, 43)
	add("DPL", uint64(d.DPL), 45, 46)
	flag("S", d.S, 44)
	flag("P", d.P, 47)
	flag("AVL", d.AVL, 52)
	flag("L", d.L, 53)
	flag("DB", d.DB, 54)
	flag("G", d.G, 55)

	var kind string
	switch {
	case d.S && d.Code && d.L:
		kind = "code64"
	case d.S && d.Code && d.DB:
		kind = "code32"
	case d.S && d.Code:
		kind = "code16"
	case d.S && d.DB:
		kind = "data32"
	case d.S:
		kind = "data16"
	default:
		kind = "system"
	}
	tn := d.TypeNm
	if tn == "" {
		tn = fmt.Sprintf("reserved system type %d", d.Type)
	}
	dec.Summary = fmt.Sprintf("%s (%s) base=%#x limit=%#x (%s) DPL=%d", kind, tn, d.Base, d.Limit, sizeText(d.Bytes+1), d.DPL)
	if !d.P {
		dec.Summary += " not-present"
	}

	if d.S && d.Code && d.L && d.DB {
		dec.Problems = append(dec.Problems, "L and D both set: reserved; loading it into CS in IA-32e mode raises #GP")
	}
	if d.S && !d.Code && d.L {
		dec.Notes = append(dec.Notes, "L is ignored on a data segment")
	}
	if !d.S && d.TypeNm == "" {
		dec.Problems = append(dec.Problems, fmt.Sprintf("system type %d is reserved", d.Type))
	}
	if !d.S && longMode && hi == nil && sysTypes64[d.Type] != "" {
		dec.Notes = append(dec.Notes, "in long mode this system descriptor is 16 bytes: the next quadword holds base 63:32")
	}
	if hi != nil && *hi>>40&0x1F != 0 {
		dec.Problems = append(dec.Problems, "bits 12:8 of the upper quadword's high dword must be 0")
	}
	if !d.P {
		dec.Notes = append(dec.Notes, "not present: loading it raises #NP (#SS for SS)")
	}
	if d.S && d.Type&1 == 0 && d.P {
		dec.Notes = append(dec.Notes, "accessed bit clear: the CPU sets it on first load, which faults if the GDT is in read-only memory")
	}
	if d.S && d.Code && d.Type&2 == 0 {
		dec.Notes = append(dec.Notes, "execute-only: data reads through CS fault")
	}
	if !d.S && (d.Type == 9 || d.Type == 11) && d.Bytes < 0x67 {
		dec.Problems = append(dec.Problems, fmt.Sprintf("TSS limit %#x is below the minimum 0x67", d.Bytes))
	}
	return dec
}

// DecodeGate decodes an IDT (or GDT call gate) entry. For a 64-bit gate pass
// the upper quadword in hi; nil decodes a 32-bit (or 16-bit) gate.
func DecodeGate(lo uint64, hi *uint64) *Decoded {
	dec := &Decoded{Kind: "gate", Value: lo}
	typ := int(lo >> 40 & 0xF)
	sel := lo >> 16 & 0xFFFF
	off := lo&0xFFFF | (lo>>48&0xFFFF)<<16
	var name string
	if hi != nil {
		dec.Kind = "gate64"
		off |= (*hi & 0xFFFFFFFF) << 32
		name = sysTypes64[typ]
	} else {
		name = sysTypes32[typ]
	}
	isGate := strings.Contains(name, "gate")
	if lo == 0 {
		dec.Summary = "empty gate (not present)"
		return dec
	}
	dpl := lo >> 45 & 3
	p := bit(lo, 47)
	dec.Fields = []FieldValue{
		{Name: "OFFSET", Value: off, Text: fmt.Sprintf("OFFSET=%#x", off)},
		{Name: "SELECTOR", Value: sel, Text: fmt.Sprintf("SELECTOR=%#x", sel), Lo: 16, Hi: 31},
		{Name: "TYPE", Value: uint64(typ), Text: fmt.Sprintf("TYPE=%d", typ), Lo: 40, Hi: 43},
		{Name: "DPL", Value: dpl, Text: fmt.Sprintf("DPL=%d", dpl), Lo: 45, Hi: 46},
	}
	if p {
		dec.Fields = append(dec.Fields, FieldValue{Name: "P", Value: 1, Text: "P", Lo: 47, Hi: 47})
	}
	if !isGate {
		if name == "" {
			name = fmt.Sprintf("type %d", typ)
		}
		dec.Summary = fmt.Sprintf("not a gate: %s", name)
		dec.Problems = append(dec.Problems, fmt.Sprintf("type %d is not a gate type here", typ))
		return dec
	}
	dec.Summary = fmt.Sprintf("%s -> %#x:%#x DPL=%d", name, sel, off, dpl)
	if typ == 5 {
		dec.Summary = fmt.Sprintf("task gate -> TSS selector %#x DPL=%d", sel, dpl)
	}
	if !p {
		dec.Summary += " not-present"
		dec.Notes = append(dec.Notes, "not present: delivering through it raises #NP")
	}
	if bit(lo, 44) {
		dec.Problems = append(dec.Problems, "S bit set: a gate is a system descriptor (S=0)")
	}
	if hi != nil {
		ist := lo >> 32 & 7
		if ist != 0 && typ != 12 {
			dec.Fields = append(dec.Fields, FieldValue{Name: "IST", Value: ist, Text: fmt.Sprintf("IST=%d", ist), Lo: 32, Hi: 34})
			dec.Summary += fmt.Sprintf(" IST=%d", ist)
		}
		if *hi>>32 != 0 {
			dec.Problems = append(dec.Problems, "upper quadword bits 63:32 are reserved")
		}
		if off>>47 != 0 && off>>47 != 0x1FFFF {
			dec.Problems = append(dec.Problems, "handler offset is not canonical")
		}
	} else if (typ == 6 || typ == 7 || typ == 14 || typ == 15) && lo>>37&7 != 0 {
		dec.Problems = append(dec.Problems, "bits 39:37 must be 0 in an interrupt or trap gate")
	}
	if sel&0xFFFC == 0 && typ != 5 {
		dec.Problems = append(dec.Problems, "null code selector: delivery raises #GP")
	}
	return dec
}

// PTE level kinds and what is special about each.
type pteLevel struct {
	title    string
	canPS    bool   // PS selects a large page here
	psRes    uint64 // reserved address bits when PS=1
	addrLo   int    // address field low bit (table or 4 KiB page)
	addrHi   int
	nx       bool   // bit 63 is XD
	patBit   int    // PAT bit for a 4 KiB leaf (7) or large page (12); 0 = none
	leaf     bool   // always maps a page
	res      uint64 // always-reserved bits
	largeLbl string // "2 MiB", "1 GiB", "4 MiB"
}

var pteLevels = map[string]pteLevel{
	"pml5e":     {title: "PML5E", addrLo: 12, addrHi: 51, nx: true, res: 1 << 7},
	"pml4e":     {title: "PML4E", addrLo: 12, addrHi: 51, nx: true, res: 1 << 7},
	"pdpte":     {title: "PDPTE", canPS: true, psRes: 0x3FFFE000, addrLo: 12, addrHi: 51, nx: true, largeLbl: "1 GiB"},
	"pde":       {title: "PDE", canPS: true, psRes: 0x1FE000, addrLo: 12, addrHi: 51, nx: true, largeLbl: "2 MiB"},
	"pte":       {title: "PTE", addrLo: 12, addrHi: 51, nx: true, patBit: 7, leaf: true},
	"pae-pdpte": {title: "PAE PDPTE", addrLo: 12, addrHi: 51, res: 0x1E6 | 1<<63},
	"pae-pde":   {title: "PAE PDE", canPS: true, psRes: 0x1FE000, addrLo: 12, addrHi: 51, nx: true, largeLbl: "2 MiB"},
	"pae-pte":   {title: "PAE PTE", addrLo: 12, addrHi: 51, nx: true, patBit: 7, leaf: true},
	"pde32":     {title: "PDE (32-bit)", canPS: true, psRes: 1 << 21, addrLo: 12, addrHi: 31, largeLbl: "4 MiB"},
	"pte32":     {title: "PTE (32-bit)", addrLo: 12, addrHi: 31, patBit: 7, leaf: true},
}

// DecodePTE decodes a paging-structure entry. kind is one of pml5e, pml4e,
// pdpte, pde, pte (4/5-level paging), pae-pdpte, pae-pde, pae-pte (PAE), and
// pde32, pte32 (32-bit paging). Reserved bits are those reserved at every
// MAXPHYADDR; bits between MAXPHYADDR and 51 are not checked, since the
// physical address width is unknown at lint time.
func DecodePTE(kind string, v uint64) *Decoded {
	lv, ok := pteLevels[kind]
	dec := &Decoded{Kind: kind, Value: v}
	if !ok {
		dec.Summary = kind
		return dec
	}
	flag := func(name string, at int) {
		if bit(v, uint(at)) {
			dec.Fields = append(dec.Fields, FieldValue{Name: name, Value: 1, Text: name, Lo: at, Hi: at})
		}
	}
	if !bit(v, 0) {
		dec.Summary = lv.title + ": not present"
		if v != 0 {
			dec.Notes = append(dec.Notes, "P clear: every other bit is available to software")
		}
		return dec
	}
	ps := lv.canPS && bit(v, 7)
	flag("P", 0)
	if kind != "pae-pdpte" {
		flag("RW", 1)
		flag("US", 2)
	}
	flag("PWT", 3)
	flag("PCD", 4)
	if kind != "pae-pdpte" {
		flag("A", 5)
	}
	if lv.leaf || ps {
		flag("D", 6)
		flag("G", 8)
	}
	if ps {
		flag("PS", 7)
	}
	if ps {
		flag("PAT", 12)
	} else if lv.patBit != 0 {
		flag("PAT", lv.patBit)
	}
	if lv.nx {
		flag("XD", 63)
	}
	addrLo := lv.addrLo
	if ps {
		switch kind {
		case "pdpte":
			addrLo = 30
		case "pde", "pae-pde":
			addrLo = 21
		case "pde32":
			addrLo = 22
		}
	}
	addr := v & (widthMask(lv.addrHi+1) &^ widthMask(addrLo))
	if kind == "pde32" && ps {
		// PSE-36: bits 20:13 hold physical address bits 39:32.
		addr |= (v >> 13 & 0xFF) << 32
	}
	dec.Fields = append(dec.Fields, FieldValue{Name: "ADDR", Value: addr, Text: fmt.Sprintf("ADDR=%#x", addr), Lo: addrLo, Hi: lv.addrHi})
	// Bits 62:59 are the protection key in a leaf entry when CR4.PKE is
	// set, and ignored otherwise; show them only when non-zero.
	if pk := v >> 59 & 0xF; lv.nx && (lv.leaf || ps) && pk != 0 {
		dec.Fields = append(dec.Fields, FieldValue{Name: "PKEY", Value: pk, Text: fmt.Sprintf("PKEY=%d", pk), Lo: 59, Hi: 62})
	}
	what := "table"
	if lv.leaf {
		what = "4 KiB page"
	} else if ps {
		what = lv.largeLbl + " page"
	}
	var flags []string
	for _, f := range dec.Fields {
		if f.Name != "P" && f.Name != "ADDR" {
			flags = append(flags, f.Text)
		}
	}
	dec.Summary = fmt.Sprintf("%s -> %s at %#x", lv.title, what, addr)
	if len(flags) > 0 {
		dec.Summary += " " + strings.Join(flags, " ")
	}
	res := lv.res
	if ps {
		res |= lv.psRes
	}
	if r := v & res; r != 0 {
		dec.Reserved = r
		dec.Problems = append(dec.Problems, fmt.Sprintf("reserved bits %#x set: the walk raises #PF with RSVD", r))
	}
	if kind == "pde32" && ps {
		dec.Notes = append(dec.Notes, "a 4 MiB page needs CR4.PSE")
	}
	if bit(v, 63) && lv.nx {
		dec.Notes = append(dec.Notes, "XD is reserved (#PF RSVD) unless EFER.NXE is set")
	}
	return dec
}

func sizeText(n uint64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GiB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
