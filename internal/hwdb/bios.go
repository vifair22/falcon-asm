package hwdb

import (
	"fmt"
	"sort"
	"strings"
)

// Error conventions a BIOS service uses to report failure.
const (
	ErrCFAH     = "cf+ah"    // CF set on error, AH = status code
	ErrCF       = "cf"       // CF set on error, no status code
	ErrAH       = "ah"       // AH = status, CF not reliable
	ErrZF       = "zf"       // ZF set = nothing available (INT 16h AH=01h)
	ErrVBE      = "vbe"      // AX != 004Fh
	ErrAH7      = "ah7"      // AH bit 7 set = timeout (INT 14h)
	ErrNone     = "none"     // cannot fail
	ErrNoReturn = "noreturn" // does not return to the caller
)

// BIOSService is one legacy BIOS function: INT Int with AH (and AL when the
// subfunction is in AL; AX=E820h is AH=E8h AL=20h). AH nil means the vector
// takes no function number.
//
// In and Out are [register, meaning] pairs in the order the reference lists
// them. Clobbers are registers the service is documented to change beyond
// Out (INT 13h AH=08h loads ES:DI); MayClobber are registers some BIOSes
// are known to destroy although no reference says so (BP across INT 10h on
// old IBM-compatible BIOSes, EDX across E820h). Rule 27 treats both as
// unknown after the call; hover keeps them apart.
type BIOSService struct {
	Int        Num         `json:"int"`
	AH         *Num        `json:"ah,omitempty"`
	AL         *Num        `json:"al,omitempty"`
	Name       string      `json:"name"`
	Since      string      `json:"since,omitempty"`
	In         [][2]string `json:"in,omitempty"`
	Out        [][2]string `json:"out,omitempty"`
	Fields     []Field     `json:"fields,omitempty"` // bit layout of a result register (Reg)
	Err        string      `json:"err,omitempty"`
	ErrNote    string      `json:"errNote,omitempty"`
	Status     string      `json:"status,omitempty"` // status code table for AH
	Clobbers   []string    `json:"clobbers,omitempty"`
	MayClobber []string    `json:"mayClobber,omitempty"`
	IF         string      `json:"if,omitempty"` // effect on the interrupt flag
	Struct     []string    `json:"struct,omitempty"`
	Quirks     []string    `json:"quirks,omitempty"`
	Confidence string      `json:"confidence,omitempty"`
}

// BIOSVector is what applies to every service of an interrupt vector.
type BIOSVector struct {
	Int        Num      `json:"int"`
	Name       string   `json:"name"`
	Detail     string   `json:"detail,omitempty"`
	MayClobber []string `json:"mayClobber,omitempty"`
	Quirks     []string `json:"quirks,omitempty"`
}

// BIOSStruct is a memory structure a BIOS service reads or fills, laid out
// so rule 45 can check one built with .byte/.word/.long.
type BIOSStruct struct {
	Name   string        `json:"name"`
	Title  string        `json:"title"`
	Sizes  []int         `json:"sizes"` // valid total sizes; the first is the basic form
	Align  int           `json:"align,omitempty"`
	Fields []StructField `json:"fields"`
	Notes  []string      `json:"notes,omitempty"`
}

// StructField is one member. Const lists the values the member must hold
// when the caller builds the structure (the DAP size byte); empty means any.
// Since is the smallest structure size that has the member.
type StructField struct {
	Off   int    `json:"off"`
	Size  int    `json:"size"`
	Name  string `json:"name"`
	Desc  string `json:"desc,omitempty"`
	Const []Num  `json:"const,omitempty"`
	Since int    `json:"since,omitempty"`
}

// Title is "INT 13h AH=42h: Extended read".
func (s *BIOSService) Title() string {
	return s.Key() + ": " + s.Name
}

// Key is "INT 13h AH=42h", "INT 15h AX=E820h" or "INT 12h".
func (s *BIOSService) Key() string {
	k := fmt.Sprintf("INT %02Xh", uint64(s.Int))
	switch {
	case s.AH != nil && s.AL != nil:
		k += fmt.Sprintf(" AX=%02X%02Xh", uint64(*s.AH), uint64(*s.AL))
	case s.AH != nil:
		k += fmt.Sprintf(" AH=%02Xh", uint64(*s.AH))
	}
	return k
}

// Detail is a one-line summary of the inputs, used by lint.Tables.
func (s *BIOSService) Detail() string {
	var parts []string
	for _, in := range s.In {
		parts = append(parts, in[0]+" = "+in[1])
	}
	return strings.Join(parts, "; ")
}

// AllClobbers is every register the call may change: outputs, documented
// clobbers and those some BIOSes are known to destroy, lowercased, without
// duplicates. Rule 27 treats each as unknown after the call.
func (s *BIOSService) AllClobbers(v *BIOSVector) []string {
	seen := map[string]bool{}
	var out []string
	add := func(r string) {
		for _, one := range splitRegs(r) {
			if !seen[one] {
				seen[one] = true
				out = append(out, one)
			}
		}
	}
	for _, o := range s.Out {
		add(o[0])
	}
	for _, r := range s.Clobbers {
		add(r)
	}
	for _, r := range s.MayClobber {
		add(r)
	}
	if v != nil {
		for _, r := range v.MayClobber {
			add(r)
		}
	}
	return out
}

// splitRegs turns "ES:DI", "CX:DX" or "CF" into lowercase register names.
func splitRegs(r string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(r, func(c rune) bool { return c == ':' || c == ',' || c == ' ' || c == '/' }) {
		p = strings.ToLower(p)
		if isRegName(p) {
			out = append(out, p)
		}
	}
	return out
}

var biosRegNames = map[string]bool{}

func init() {
	for _, r := range strings.Fields("al ah ax eax bl bh bx ebx cl ch cx ecx dl dh dx edx si esi di edi bp ebp sp esp ds es fs gs ss cf zf if df flags") {
		biosRegNames[r] = true
	}
}

func isRegName(s string) bool { return biosRegNames[s] }

type biosKey struct {
	vec    uint64
	ah, al int // -1 = none
}

type biosFile struct {
	Vectors  []BIOSVector      `json:"vectors"`
	Services []BIOSService     `json:"services"`
	Structs  []BIOSStruct      `json:"structs"`
	Status   map[string][]Enum `json:"status"`
}

type biosTable struct {
	file    biosFile
	byKey   map[biosKey]*BIOSService
	vectors map[uint64]*BIOSVector
	structs map[string]*BIOSStruct
}

func loadBIOS() (*biosTable, error) {
	var f biosFile
	if err := readJSON("bios.json", &f); err != nil {
		return emptyBIOS(), err
	}
	t, err := buildBIOS(f, "bios.json")
	if err != nil {
		return emptyBIOS(), err
	}
	return t, nil
}

func emptyBIOS() *biosTable {
	return &biosTable{byKey: map[biosKey]*BIOSService{}, vectors: map[uint64]*BIOSVector{}, structs: map[string]*BIOSStruct{}}
}

func keyOf(s *BIOSService) biosKey {
	k := biosKey{vec: uint64(s.Int), ah: -1, al: -1}
	if s.AH != nil {
		k.ah = int(*s.AH)
	}
	if s.AL != nil {
		k.al = int(*s.AL)
	}
	return k
}

var validErr = map[string]bool{ErrCFAH: true, ErrCF: true, ErrAH: true, ErrZF: true, ErrVBE: true, ErrAH7: true, ErrNone: true, ErrNoReturn: true, "": true}

func finishService(s *BIOSService, f *biosFile, structs map[string]*BIOSStruct, path string) error {
	if s.Name == "" {
		return fmt.Errorf("%s.name: empty", path)
	}
	if s.Int > 0xFF || (s.AH != nil && *s.AH > 0xFF) || (s.AL != nil && *s.AL > 0xFF) {
		return fmt.Errorf("%s: vector, AH and AL are bytes", path)
	}
	if s.AL != nil && s.AH == nil {
		return fmt.Errorf("%s.al: set without ah", path)
	}
	if !validErr[s.Err] {
		return fmt.Errorf("%s.err: unknown convention %q", path, s.Err)
	}
	if s.Status != "" {
		if _, ok := f.Status[s.Status]; !ok {
			return fmt.Errorf("%s.status: unknown status table %q", path, s.Status)
		}
	}
	for _, st := range s.Struct {
		if _, ok := structs[st]; !ok {
			return fmt.Errorf("%s.struct: unknown structure %q", path, st)
		}
	}
	for i, r := range append(append([]string(nil), s.Clobbers...), s.MayClobber...) {
		if len(splitRegs(r)) == 0 {
			return fmt.Errorf("%s.clobbers[%d]: %q is not a register", path, i, r)
		}
	}
	for i := range s.Fields {
		if s.Fields[i].Reg == "" {
			return fmt.Errorf("%s.fields[%d].reg: result fields need the register", path, i)
		}
	}
	// Result fields sit in different registers, so overlap is checked per
	// register on copies; the originals then get their bit ranges.
	byReg := map[string][]Field{}
	for _, fl := range s.Fields {
		byReg[fl.Reg] = append(byReg[fl.Reg], fl)
	}
	for reg, fs := range byReg {
		if _, err := finishFields(fs, 32, path+".fields("+reg+")"); err != nil {
			return err
		}
	}
	for i := range s.Fields {
		s.Fields[i].Lo, s.Fields[i].Hi, _ = parseBits(s.Fields[i].Bits)
	}
	return nil
}

func finishStruct(st *BIOSStruct, path string) error {
	if st.Name == "" || len(st.Sizes) == 0 {
		return fmt.Errorf("%s: needs name and sizes", path)
	}
	maxSize := 0
	for _, s := range st.Sizes {
		maxSize = max(maxSize, s)
	}
	end := 0
	for i, f := range st.Fields {
		p := fmt.Sprintf("%s.fields[%d]", path, i)
		if f.Off < end {
			return fmt.Errorf("%s.off: %d overlaps the previous field (ends at %d)", p, f.Off, end)
		}
		if f.Size <= 0 || f.Off+f.Size > maxSize {
			return fmt.Errorf("%s: %s at %d+%d past the largest size %d", p, f.Name, f.Off, f.Size, maxSize)
		}
		for _, c := range f.Const {
			if f.Size < 8 && uint64(c)>>(8*uint(f.Size)) != 0 {
				return fmt.Errorf("%s.const: %#x does not fit %d bytes", p, uint64(c), f.Size)
			}
		}
		end = f.Off + f.Size
	}
	return nil
}

func buildBIOS(f biosFile, file string) (*biosTable, error) {
	t := &biosTable{file: f, byKey: make(map[biosKey]*BIOSService, len(f.Services)),
		vectors: map[uint64]*BIOSVector{}, structs: map[string]*BIOSStruct{}}
	for i := range t.file.Structs {
		st := &t.file.Structs[i]
		if err := finishStruct(st, fmt.Sprintf("%s: structs[%d]", file, i)); err != nil {
			return nil, err
		}
		if _, dup := t.structs[st.Name]; dup {
			return nil, fmt.Errorf("%s: structs[%d]: duplicate %q", file, i, st.Name)
		}
		t.structs[st.Name] = st
	}
	for i := range t.file.Vectors {
		v := &t.file.Vectors[i]
		if _, dup := t.vectors[uint64(v.Int)]; dup {
			return nil, fmt.Errorf("%s: vectors[%d]: duplicate INT %02Xh", file, i, uint64(v.Int))
		}
		t.vectors[uint64(v.Int)] = v
	}
	for name, codes := range t.file.Status {
		seen := map[uint64]bool{}
		for _, c := range codes {
			if seen[uint64(c.V)] {
				return nil, fmt.Errorf("%s: status %s: duplicate code %#x", file, name, uint64(c.V))
			}
			seen[uint64(c.V)] = true
		}
	}
	for i := range t.file.Services {
		s := &t.file.Services[i]
		path := fmt.Sprintf("%s: services[%d]", file, i)
		if err := finishService(s, &t.file, t.structs, path); err != nil {
			return nil, err
		}
		k := keyOf(s)
		if prev, dup := t.byKey[k]; dup {
			return nil, fmt.Errorf("%s: %s already defined (%s)", path, s.Key(), prev.Name)
		}
		t.byKey[k] = s
	}
	return t, nil
}

// group lists the AL-selected services under vector/AH, sorted by AL.
func (t *biosTable) group(vector, ah uint64) []*BIOSService {
	var out []*BIOSService
	for k, s := range t.byKey {
		if k.vec == vector && k.ah == int(ah) && k.al >= 0 {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return *out[i].AL < *out[j].AL })
	return out
}

func groupTitle(vector, ah uint64, g []*BIOSService) string {
	var parts []string
	for _, s := range g {
		parts = append(parts, fmt.Sprintf("AL=%02Xh %s", uint64(*s.AL), s.Name))
	}
	return fmt.Sprintf("INT %02Xh AH=%02Xh: AL selects: %s", vector, ah, strings.Join(parts, ", "))
}

// LookupBIOS finds the service for INT vector with AH and, when alKnown, AL.
// The most specific entry wins: (AH, AL), then (AH), then the vector alone
// for INTs that take no function number. When AL is unknown and only
// AL-specific entries exist under AH, nothing is returned; BIOS() and the
// hover list the group instead.
func (d *DB) LookupBIOS(vector, ah, al uint64, alKnown bool) (*BIOSService, bool) {
	t := d.biosTab()
	if alKnown {
		if s, ok := t.byKey[biosKey{vector, int(ah & 0xFF), int(al & 0xFF)}]; ok {
			return s, true
		}
	}
	if s, ok := t.byKey[biosKey{vector, int(ah & 0xFF), -1}]; ok {
		return s, true
	}
	if s, ok := t.byKey[biosKey{vector, -1, -1}]; ok {
		return s, true
	}
	return nil, false
}

// BIOSVectorInfo is the per-vector information for INT vector.
func (d *DB) BIOSVectorInfo(vector uint64) (*BIOSVector, bool) {
	v, ok := d.biosTab().vectors[vector]
	return v, ok
}

// BIOSStatus names a status code from the named table ("int13", "pcibios").
func (d *DB) BIOSStatus(table string, code uint64) (string, bool) {
	for _, e := range d.biosTab().file.Status[table] {
		if uint64(e.V) == code {
			return e.Name, true
		}
	}
	return "", false
}

// Struct returns the BIOS memory structure called name ("dap", "e820").
func (d *DB) Struct(name string) (*BIOSStruct, bool) {
	s, ok := d.biosTab().structs[name]
	return s, ok
}

// Structs lists the structure names, sorted.
func (d *DB) Structs() []string {
	var out []string
	for n := range d.biosTab().structs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// CheckStruct validates a structure built from data (the bytes of
// .byte/.word/.long directives): its size against the valid sizes, and each
// member with a Const against the bytes present. Missing trailing bytes are
// only reported through the size check.
func (st *BIOSStruct) CheckStruct(data []byte) []string {
	var out []string
	okSize := false
	for _, s := range st.Sizes {
		if len(data) == s {
			okSize = true
		}
	}
	if !okSize {
		out = append(out, fmt.Sprintf("%s is %d bytes; valid sizes: %s", st.Title, len(data), intsList(st.Sizes)))
	}
	for _, f := range st.Fields {
		if len(f.Const) == 0 || f.Off+f.Size > len(data) {
			continue
		}
		var v uint64
		for i := f.Size - 1; i >= 0; i-- {
			v = v<<8 | uint64(data[f.Off+i])
		}
		match := false
		for _, c := range f.Const {
			if uint64(c) == v {
				match = true
			}
		}
		if !match {
			var want []string
			for _, c := range f.Const {
				want = append(want, fmt.Sprintf("%#x", uint64(c)))
			}
			out = append(out, fmt.Sprintf("%s.%s (offset %d) is %#x, want %s", st.Name, f.Name, f.Off, v, strings.Join(want, " or ")))
		}
	}
	return out
}

func intsList(xs []int) string {
	var s []string
	for _, x := range xs {
		s = append(s, fmt.Sprintf("%d (%#x)", x, x))
	}
	return strings.Join(s, ", ")
}
