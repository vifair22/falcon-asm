package hwdb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Helper is a C function (or macro) whose argument is a port, MSR index or
// CPUID leaf, so rule 56 can show the same hover on `outb(0x92, v)` as on
// `outb %al, $0x92`. Arg, Sub and Value are zero-based argument positions.
type Helper struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // "port", "msr", "cpuid"; "none" in an override removes a default
	Arg   int    `json:"arg"`
	Sub   *int   `json:"sub,omitempty"`   // CPUID subleaf argument
	Value *int   `json:"value,omitempty"` // argument holding the value written
	Write bool   `json:"write,omitempty"`
	Width int    `json:"width,omitempty"` // port access width in bits
}

func argp(n int) *int { return &n }

// defaultHelpers follow falcon's own signatures (port first:
// `outb(uint16_t port, uint8_t v)`, `rdmsr(uint32_t msr)`,
// `wrmsr(uint32_t msr, uint64_t v)`), GCC's <cpuid.h>, MSVC's intrinsics
// and EDK2's BaseLib/IoLib. Linux's `outb(value, port)` order is
// deliberately absent: it would collide with falcon's; a project that uses it
// overrides outb with "arg": 1.
var defaultHelpers = func() map[string]*Helper {
	hs := []*Helper{
		{Name: "inb", Kind: "port", Width: 8},
		{Name: "inw", Kind: "port", Width: 16},
		{Name: "inl", Kind: "port", Width: 32},
		{Name: "outb", Kind: "port", Width: 8, Write: true, Value: argp(1)},
		{Name: "outw", Kind: "port", Width: 16, Write: true, Value: argp(1)},
		{Name: "outl", Kind: "port", Width: 32, Write: true, Value: argp(1)},
		{Name: "rdmsr", Kind: "msr"},
		{Name: "wrmsr", Kind: "msr", Write: true, Value: argp(1)},
		{Name: "cpuid", Kind: "cpuid"},
		{Name: "cpuid_count", Kind: "cpuid", Sub: argp(1)},
		{Name: "__get_cpuid", Kind: "cpuid"},
		{Name: "__get_cpuid_count", Kind: "cpuid", Sub: argp(1)},
		{Name: "__cpuid", Kind: "cpuid"},
		{Name: "__cpuid_count", Kind: "cpuid", Sub: argp(1)},
		{Name: "__readmsr", Kind: "msr"},
		{Name: "__writemsr", Kind: "msr", Write: true, Value: argp(1)},
		{Name: "__inbyte", Kind: "port", Width: 8},
		{Name: "__inword", Kind: "port", Width: 16},
		{Name: "__indword", Kind: "port", Width: 32},
		{Name: "__outbyte", Kind: "port", Width: 8, Write: true, Value: argp(1)},
		{Name: "__outword", Kind: "port", Width: 16, Write: true, Value: argp(1)},
		{Name: "__outdword", Kind: "port", Width: 32, Write: true, Value: argp(1)},
		{Name: "IoRead8", Kind: "port", Width: 8},
		{Name: "IoRead16", Kind: "port", Width: 16},
		{Name: "IoRead32", Kind: "port", Width: 32},
		{Name: "IoWrite8", Kind: "port", Width: 8, Write: true, Value: argp(1)},
		{Name: "IoWrite16", Kind: "port", Width: 16, Write: true, Value: argp(1)},
		{Name: "IoWrite32", Kind: "port", Width: 32, Write: true, Value: argp(1)},
		{Name: "AsmReadMsr64", Kind: "msr"},
		{Name: "AsmWriteMsr64", Kind: "msr", Write: true, Value: argp(1)},
		{Name: "AsmCpuid", Kind: "cpuid"},
		{Name: "AsmCpuidEx", Kind: "cpuid", Sub: argp(1)},
	}
	m := make(map[string]*Helper, len(hs))
	for _, h := range hs {
		m[h.Name] = h
	}
	return m
}()

// Helper returns the C helper called name.
func (d *DB) Helper(name string) (*Helper, bool) {
	h, ok := d.helperTab()[name]
	return h, ok
}

// Helpers lists the helper names, sorted.
func (d *DB) Helpers() []string {
	m := d.helperTab()
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func validateHelper(h *Helper, path string) error {
	if h.Name == "" {
		return fmt.Errorf("%s.name: empty", path)
	}
	switch h.Kind {
	case "port", "msr", "cpuid", "none":
	default:
		return fmt.Errorf("%s.kind: %q, want \"port\", \"msr\", \"cpuid\" or \"none\"", path, h.Kind)
	}
	if h.Arg < 0 || (h.Sub != nil && *h.Sub < 0) || (h.Value != nil && *h.Value < 0) {
		return fmt.Errorf("%s: argument positions start at 0", path)
	}
	switch h.Width {
	case 0, 8, 16, 32:
	default:
		return fmt.Errorf("%s.width: %d, want 8, 16 or 32", path, h.Width)
	}
	return nil
}

// overrideKeys are the sections an override object may have.
var overrideKeys = map[string]bool{"ports": true, "msrs": true, "bios": true, "helpers": true}

// Load merges a project's overrides (the "hwdb" object of .falcon-asm.json)
// over the built-in tables:
//
//	{"ports":   [{"port": "0x510", "name": "fw_cfg selector", "widths": [16]}],
//	 "msrs":    [{"index": "0xC0011029", "name": "DE_CFG", "reserved": "0"}],
//	 "bios":    [{"int": "0x15", "ah": "0xE8", "al": "0x20", "quirks": ["..."]}],
//	 "helpers": [{"name": "port_out8", "kind": "port", "write": true, "value": 1}]}
//
// An entry whose key (port and access, MSR index, INT/AH/AL, helper name)
// matches a built-in one is merged into it: fields it sets replace the
// built-in's, and notes, quirks and clobber lists are appended. Other entries
// are added. A helper with "kind": "none" removes the helper of that name. A
// project port that a block register covers (0x3FD in COM1) shadows it.
//
// Errors name the JSON path of the offending value ("ports[2].fields[0].bits:
// ..."). An empty or null message returns Default().
func Load(overrides json.RawMessage) (*DB, error) {
	raw := bytes.TrimSpace(overrides)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return Default(), nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("hwdb: %w", err)
	}
	for k := range top {
		if !overrideKeys[k] {
			return nil, fmt.Errorf("hwdb: unknown section %q (want ports, msrs, bios or helpers)", k)
		}
	}
	db := &DB{}
	var err error
	if r, ok := top["ports"]; ok {
		if db.ports, err = overridePorts(r); err != nil {
			return nil, err
		}
	}
	if r, ok := top["msrs"]; ok {
		if db.msrs, err = overrideMSRs(r); err != nil {
			return nil, err
		}
	}
	if r, ok := top["bios"]; ok {
		if db.bios, err = overrideBIOS(r); err != nil {
			return nil, err
		}
	}
	if r, ok := top["helpers"]; ok {
		if db.helpers, err = overrideHelpers(r); err != nil {
			return nil, err
		}
	}
	return db, nil
}

// elements splits a JSON array into its raw elements.
func elements(raw json.RawMessage, section string) ([]json.RawMessage, error) {
	var els []json.RawMessage
	if err := json.Unmarshal(raw, &els); err != nil {
		return nil, fmt.Errorf("hwdb: %s: want an array: %w", section, err)
	}
	return els, nil
}

func decodeElem(raw json.RawMessage, v any, path string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("hwdb: %s: %w", path, err)
	}
	return nil
}

func setStr(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

func overridePorts(raw json.RawMessage) (*portTable, error) {
	els, err := elements(raw, "ports")
	if err != nil {
		return nil, err
	}
	// A fresh parse of the built-in file, so merging never writes into the
	// shared default table other goroutines are reading.
	var f portFile
	if err := readJSON("ports.json", &f); err != nil {
		return nil, err
	}
	index := map[[3]string]int{}
	for i, p := range f.Ports {
		index[[3]string{fmt.Sprint(uint64(p.Port)), p.Access, p.Cond}] = i
	}
	shadow := map[uint64]bool{}
	for i, el := range els {
		path := fmt.Sprintf("ports[%d]", i)
		var p Port
		if err := decodeElem(el, &p, path); err != nil {
			return nil, err
		}
		k := [3]string{fmt.Sprint(uint64(p.Port)), p.Access, p.Cond}
		at, ok := index[k]
		if !ok && p.Access == "" {
			// An entry that does not say read or write merges into the
			// port's only entry, whichever direction that one has.
			at, ok = onlyEntry(f.Ports, uint64(p.Port), p.Cond)
		}
		if ok {
			mergePort(&f.Ports[at], p)
			chk := f.Ports[at]
			chk.Fields = append([]Field(nil), chk.Fields...)
			if err := finishPort(&chk, path); err != nil {
				return nil, fmt.Errorf("hwdb: %w", err)
			}
			continue
		}
		chk := p
		chk.Fields = append([]Field(nil), p.Fields...)
		if err := finishPort(&chk, path); err != nil {
			return nil, fmt.Errorf("hwdb: %w", err)
		}
		index[k] = len(f.Ports)
		f.Ports = append(f.Ports, p)
		last := uint64(p.Last)
		if last == 0 {
			last = uint64(p.Port)
		}
		for n := uint64(p.Port); n <= last && n <= 0xFFFF; n++ {
			shadow[n] = true
		}
	}
	t, err := buildPortsShadow(f, "hwdb override", shadow)
	if err != nil {
		return nil, fmt.Errorf("hwdb: %w", err)
	}
	return t, nil
}

func onlyEntry(ps []Port, port uint64, cond string) (int, bool) {
	at, n := -1, 0
	for i := range ps {
		if uint64(ps[i].Port) == port && ps[i].Last == 0 && ps[i].Cond == cond {
			at, n = i, n+1
		}
	}
	return at, n == 1
}

func mergePort(dst *Port, src Port) {
	setStr(&dst.Name, src.Name)
	setStr(&dst.Long, src.Long)
	setStr(&dst.Device, src.Device)
	setStr(&dst.Detail, src.Detail)
	setStr(&dst.ValuesName, src.ValuesName)
	setStr(&dst.Confidence, src.Confidence)
	if src.Last != 0 {
		dst.Last = src.Last
	}
	if src.ValueMask != 0 {
		dst.ValueMask = src.ValueMask
	}
	if len(src.Widths) > 0 {
		dst.Widths = src.Widths
	}
	if len(src.Fields) > 0 {
		dst.Fields = src.Fields
	}
	if len(src.Values) > 0 {
		dst.Values = src.Values
	}
	dst.Notes = append(dst.Notes, src.Notes...)
}

func overrideMSRs(raw json.RawMessage) (*msrTable, error) {
	els, err := elements(raw, "msrs")
	if err != nil {
		return nil, err
	}
	var f msrFile
	if err := readJSON("msrs.json", &f); err != nil {
		return nil, err
	}
	index := map[uint64]int{}
	for i, m := range f.MSRs {
		index[uint64(m.Index)] = i
	}
	for i, el := range els {
		path := fmt.Sprintf("msrs[%d]", i)
		var m MSR
		if err := decodeElem(el, &m, path); err != nil {
			return nil, err
		}
		if m.Like != 0 {
			return nil, fmt.Errorf("hwdb: %s.like: not supported in overrides", path)
		}
		target := &m
		if at, ok := index[uint64(m.Index)]; ok {
			mergeMSR(&f.MSRs[at], m)
			target = &f.MSRs[at]
		}
		chk := *target
		chk.Fields = append([]Field(nil), target.Fields...)
		if chk.Like != 0 {
			chk.Like = 0 // the built-in's like is resolved by the build below
		}
		if err := finishMSR(&chk, f.Enums, path); err != nil {
			return nil, fmt.Errorf("hwdb: %w", err)
		}
		if target == &m {
			index[uint64(m.Index)] = len(f.MSRs)
			f.MSRs = append(f.MSRs, m)
		}
	}
	t, err := buildMSRs(f.MSRs, f.Enums, "hwdb override")
	if err != nil {
		return nil, fmt.Errorf("hwdb: %w", err)
	}
	return t, nil
}

func mergeMSR(dst *MSR, src MSR) {
	setStr(&dst.Name, src.Name)
	setStr(&dst.Alias, src.Alias)
	setStr(&dst.Vendor, src.Vendor)
	setStr(&dst.Access, src.Access)
	setStr(&dst.Detail, src.Detail)
	setStr(&dst.Confidence, src.Confidence)
	if src.Last != 0 {
		dst.Last, dst.Stride = src.Last, src.Stride
	}
	if len(src.Fields) > 0 {
		dst.Fields = src.Fields
		dst.Like = 0
	}
	if src.Reserved != 0 {
		dst.Reserved = src.Reserved
	}
	dst.Notes = append(dst.Notes, src.Notes...)
}

func overrideBIOS(raw json.RawMessage) (*biosTable, error) {
	els, err := elements(raw, "bios")
	if err != nil {
		return nil, err
	}
	var f biosFile
	if err := readJSON("bios.json", &f); err != nil {
		return nil, err
	}
	index := map[biosKey]int{}
	for i := range f.Services {
		index[keyOf(&f.Services[i])] = i
	}
	structs := map[string]*BIOSStruct{}
	for i := range f.Structs {
		structs[f.Structs[i].Name] = &f.Structs[i]
	}
	for i, el := range els {
		path := fmt.Sprintf("bios[%d]", i)
		var s BIOSService
		if err := decodeElem(el, &s, path); err != nil {
			return nil, err
		}
		k := keyOf(&s)
		if at, ok := index[k]; ok {
			mergeBIOS(&f.Services[at], s)
			chk := f.Services[at]
			chk.Fields = append([]Field(nil), chk.Fields...)
			if err := finishService(&chk, &f, structs, path); err != nil {
				return nil, fmt.Errorf("hwdb: %w", err)
			}
			continue
		}
		chk := s
		chk.Fields = append([]Field(nil), s.Fields...)
		if err := finishService(&chk, &f, structs, path); err != nil {
			return nil, fmt.Errorf("hwdb: %w", err)
		}
		index[k] = len(f.Services)
		f.Services = append(f.Services, s)
	}
	t, err := buildBIOS(f, "hwdb override")
	if err != nil {
		return nil, fmt.Errorf("hwdb: %w", err)
	}
	return t, nil
}

func mergeBIOS(dst *BIOSService, src BIOSService) {
	setStr(&dst.Name, src.Name)
	setStr(&dst.Since, src.Since)
	setStr(&dst.Err, src.Err)
	setStr(&dst.ErrNote, src.ErrNote)
	setStr(&dst.Status, src.Status)
	setStr(&dst.IF, src.IF)
	setStr(&dst.Confidence, src.Confidence)
	if len(src.In) > 0 {
		dst.In = src.In
	}
	if len(src.Out) > 0 {
		dst.Out = src.Out
	}
	if len(src.Fields) > 0 {
		dst.Fields = src.Fields
	}
	if len(src.Struct) > 0 {
		dst.Struct = src.Struct
	}
	dst.Clobbers = append(dst.Clobbers, src.Clobbers...)
	dst.MayClobber = append(dst.MayClobber, src.MayClobber...)
	dst.Quirks = append(dst.Quirks, src.Quirks...)
}

func overrideHelpers(raw json.RawMessage) (map[string]*Helper, error) {
	els, err := elements(raw, "helpers")
	if err != nil {
		return nil, err
	}
	m := make(map[string]*Helper, len(defaultHelpers)+len(els))
	for k, v := range defaultHelpers {
		m[k] = v
	}
	for i, el := range els {
		path := fmt.Sprintf("helpers[%d]", i)
		h := &Helper{}
		if err := decodeElem(el, h, path); err != nil {
			return nil, err
		}
		if err := validateHelper(h, path); err != nil {
			return nil, fmt.Errorf("hwdb: %w", err)
		}
		if h.Kind == "none" {
			delete(m, h.Name)
			continue
		}
		m[h.Name] = h
	}
	return m, nil
}
