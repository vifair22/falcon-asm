package hwdb

import (
	"fmt"
	"strconv"
	"strings"
)

// Num is a number in the tables. JSON accepts either a plain number or a
// string in any Go integer syntax ("0x3F8", "0b1010", "1016"), because port
// numbers, MSR indexes and masks are only readable in hex and JSON has no hex
// literal.
type Num uint64

// UnmarshalJSON implements json.Unmarshaler.
func (n *Num) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	v, err := strconv.ParseUint(strings.ReplaceAll(s, "_", ""), 0, 64)
	if err != nil {
		return fmt.Errorf("bad number %s", string(b))
	}
	*n = Num(v)
	return nil
}

// Enum names one value of a field, port or status code.
type Enum struct {
	V    Num    `json:"v"`
	Name string `json:"name"`
}

// Field is a bit field of a register, MSR, port value or CPUID output.
//
// Bits is written the way the SDM writes it, "hi:lo" or a single bit "n",
// so a table line can be checked against the manual by eye. Lo and Hi are
// filled from it when the table is loaded.
//
// ISA, Insns and Level only appear on CPUID feature bits: the internal/isa
// ISA_SET names (a trailing "*" matches a prefix, for XED's per-width
// AVX-512 sets) and mnemonics the bit guards, and the x86-64 psABI level
// that requires it. Mnemonics are listed when the ISA set is shared with
// instructions guarded by other bits (rdtsc and rdmsr are both PENTIUMREAL).
type Field struct {
	Name       string   `json:"name"`
	Bits       string   `json:"bits"`
	Desc       string   `json:"desc,omitempty"`
	Reg        string   `json:"reg,omitempty"` // for BIOS result fields: the register holding it
	Values     []Enum   `json:"values,omitempty"`
	Enum       string   `json:"enum,omitempty"`   // name of a shared value list in the same file
	RO         bool     `json:"ro,omitempty"`     // read-only (writes ignored or must preserve)
	Strict     bool     `json:"strict,omitempty"` // values not in Values are reserved and fault
	ISA        []string `json:"isa,omitempty"`
	Insns      []string `json:"insns,omitempty"`
	Level      int      `json:"level,omitempty"`
	Confidence string   `json:"confidence,omitempty"`

	Lo, Hi int `json:"-"`
}

// Mask is the field's bits in place.
func (f *Field) Mask() uint64 {
	w := f.Hi - f.Lo + 1
	if w >= 64 {
		return ^uint64(0)
	}
	return ((uint64(1) << uint(w)) - 1) << uint(f.Lo)
}

// Get extracts the field from v.
func (f *Field) Get(v uint64) uint64 {
	return (v & f.Mask()) >> uint(f.Lo)
}

// Width is the field's size in bits.
func (f *Field) Width() int { return f.Hi - f.Lo + 1 }

// ValueName is the name the field's Values give v, or "".
func (f *Field) ValueName(v uint64) string {
	for _, e := range f.Values {
		if uint64(e.V) == v {
			return e.Name
		}
	}
	return ""
}

// parseBits turns "hi:lo" or "n" into the bit range.
func parseBits(s string) (lo, hi int, err error) {
	if a, b, ok := strings.Cut(s, ":"); ok {
		h, e1 := strconv.Atoi(strings.TrimSpace(a))
		l, e2 := strconv.Atoi(strings.TrimSpace(b))
		if e1 != nil || e2 != nil {
			return 0, 0, fmt.Errorf("bad bit range %q", s)
		}
		if l > h {
			return 0, 0, fmt.Errorf("bit range %q is low:high, want high:low", s)
		}
		return l, h, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, 0, fmt.Errorf("bad bit %q", s)
	}
	return n, n, nil
}

// finishFields fills Lo/Hi and checks every field lies inside width bits and
// that no two fields overlap, returning the union of their masks. path
// prefixes errors so a project override's mistake points at its JSON path.
func finishFields(fs []Field, width int, path string) (uint64, error) {
	var used uint64
	for i := range fs {
		f := &fs[i]
		p := fmt.Sprintf("%s[%d]", path, i)
		if f.Name == "" {
			return 0, fmt.Errorf("%s.name: empty", p)
		}
		lo, hi, err := parseBits(f.Bits)
		if err != nil {
			return 0, fmt.Errorf("%s.bits: %w", p, err)
		}
		if lo < 0 || hi >= width {
			return 0, fmt.Errorf("%s.bits: %q outside a %d-bit value", p, f.Bits, width)
		}
		f.Lo, f.Hi = lo, hi
		m := f.Mask()
		if used&m != 0 {
			return 0, fmt.Errorf("%s.bits: %s (%q) overlaps another field", p, f.Name, f.Bits)
		}
		used |= m
		for j, e := range f.Values {
			if f.Width() < 64 && uint64(e.V)>>uint(f.Width()) != 0 {
				return 0, fmt.Errorf("%s.values[%d]: %#x does not fit in %d bits", p, j, uint64(e.V), f.Width())
			}
		}
	}
	return used, nil
}

// widthMask is the mask of a width-bit value.
func widthMask(width int) uint64 {
	if width >= 64 {
		return ^uint64(0)
	}
	return (uint64(1) << uint(width)) - 1
}

// setFields lists the fields of v that are non-zero, rendered "NAME" for a
// set single bit and "NAME=value" (with the value's name when known) for
// wider fields.
func setFields(fs []Field, v uint64) []FieldValue {
	var out []FieldValue
	for i := range fs {
		f := &fs[i]
		x := f.Get(v)
		// Zero is shown only for a multi-bit field that names it (a PAT
		// entry of 0 is UC, which matters); a clear flag is not listed.
		if x == 0 && (f.Width() == 1 || f.ValueName(0) == "") {
			continue
		}
		fv := FieldValue{Name: f.Name, Value: x, Lo: f.Lo, Hi: f.Hi}
		switch {
		case f.Width() == 1:
			fv.Text = f.Name
		case f.ValueName(x) != "":
			fv.Text = fmt.Sprintf("%s=%s", f.Name, f.ValueName(x))
		default:
			fv.Text = fmt.Sprintf("%s=%#x", f.Name, x)
		}
		out = append(out, fv)
	}
	return out
}

// FieldValue is one field of a decoded value.
type FieldValue struct {
	Name   string
	Value  uint64
	Text   string // "PE", "TYPE=WB", "BASE=0x1000"
	Lo, Hi int
}

// resolveEnums replaces each field's Enum reference with the shared list it
// names, so a value set used by many fields (MTRR memory types) is written
// once in the JSON.
func resolveEnums(fs []Field, enums map[string][]Enum, path string) error {
	for i := range fs {
		if fs[i].Enum == "" {
			continue
		}
		vs, ok := enums[fs[i].Enum]
		if !ok {
			return fmt.Errorf("%s[%d].enum: unknown value list %q", path, i, fs[i].Enum)
		}
		if len(fs[i].Values) == 0 {
			fs[i].Values = vs
		}
	}
	return nil
}
