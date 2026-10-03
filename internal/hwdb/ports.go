package hwdb

import (
	"fmt"
	"sort"
	"strings"
)

// Port is one I/O port register. In a PortBlock, Port is the offset from the
// block's base.
//
// One port number can carry several entries: a different register on read
// and write (Access "r" / "w", e.g. 0x64 status and command), or under a
// condition (Cond, e.g. "DLAB=1" for a UART's divisor latch).
type Port struct {
	Port       Num      `json:"port"`
	Last       Num      `json:"last,omitempty"` // inclusive end of a range entry; 0 = one port
	Name       string   `json:"name"`
	Long       string   `json:"long,omitempty"` // spelled-out name
	Device     string   `json:"device,omitempty"`
	Widths     []int    `json:"widths,omitempty"` // access widths in bits; empty = 8 only
	Access     string   `json:"access,omitempty"` // "r", "w" or "" (both)
	Cond       string   `json:"cond,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	Fields     []Field  `json:"fields,omitempty"`
	Values     []Enum   `json:"values,omitempty"` // named values written (commands, indexes)
	ValuesName string   `json:"valuesName,omitempty"`
	ValueMask  Num      `json:"valueMask,omitempty"` // bits of the value Values names (0 = all)
	Notes      []string `json:"notes,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
}

// PortBase is one instance of a PortBlock: COM1 at 0x3F8.
type PortBase struct {
	Base Num    `json:"base"`
	Name string `json:"name"`
}

// PortBlock is a register file that appears at several bases with the same
// layout: UARTs, parallel ports, IDE channels.
type PortBlock struct {
	Name   string     `json:"name"`
	Device string     `json:"device,omitempty"`
	Span   int        `json:"span"`
	Bases  []PortBase `json:"bases"`
	Regs   []Port     `json:"regs"`
	Notes  []string   `json:"notes,omitempty"`
}

// PortHit is one register a port number resolves to.
type PortHit struct {
	Port  uint64
	Reg   *Port
	Unit  string     // the block instance, "COM1"; "" for a standalone port
	Block *PortBlock // nil for a standalone port
}

// Name is the hit's display name: "COM1 LSR" or "PIC1 command".
func (h PortHit) Name() string {
	if h.Unit != "" {
		return h.Unit + " " + h.Reg.Name
	}
	return h.Reg.Name
}

// AllowsWidth reports whether an access of width bits is documented for the
// register.
func (p *Port) AllowsWidth(width int) bool {
	if len(p.Widths) == 0 {
		return width == 8
	}
	for _, w := range p.Widths {
		if w == width {
			return true
		}
	}
	return false
}

// Readable and Writable reflect Access.
func (p *Port) Readable() bool { return p.Access != "w" }

// Writable reflects Access.
func (p *Port) Writable() bool { return p.Access != "r" }

// ValueName names a value written to the port, from Values under ValueMask.
func (p *Port) ValueName(v uint64) string {
	if p.ValueMask != 0 {
		v &= uint64(p.ValueMask)
	}
	for _, e := range p.Values {
		if uint64(e.V) == v {
			return e.Name
		}
	}
	return ""
}

type portFile struct {
	Ports  []Port      `json:"ports"`
	Blocks []PortBlock `json:"blocks"`
}

type portTable struct {
	file  portFile
	byNum map[uint64][]PortHit
}

func loadPorts() (*portTable, error) {
	var f portFile
	if err := readJSON("ports.json", &f); err != nil {
		return &portTable{byNum: map[uint64][]PortHit{}}, err
	}
	t, err := buildPorts(f, "ports.json")
	if err != nil {
		return &portTable{byNum: map[uint64][]PortHit{}}, err
	}
	return t, nil
}

// finishPort validates one entry and resolves its field bit ranges.
func finishPort(p *Port, path string) error {
	if p.Name == "" {
		return fmt.Errorf("%s.name: empty", path)
	}
	if p.Last != 0 && p.Last < p.Port {
		return fmt.Errorf("%s.last: %#x before port %#x", path, uint64(p.Last), uint64(p.Port))
	}
	if p.Port > 0xFFFF || p.Last > 0xFFFF {
		return fmt.Errorf("%s.port: %#x outside the 64 KiB I/O space", path, uint64(p.Port))
	}
	if p.Access != "" && p.Access != "r" && p.Access != "w" {
		return fmt.Errorf("%s.access: %q, want \"r\", \"w\" or empty", path, p.Access)
	}
	width := 8
	for _, w := range p.Widths {
		if w != 8 && w != 16 && w != 32 {
			return fmt.Errorf("%s.widths: %d, want 8, 16 or 32", path, w)
		}
		width = max(width, w)
	}
	if _, err := finishFields(p.Fields, width, path+".fields"); err != nil {
		return err
	}
	seen := map[uint64]bool{}
	for i, e := range p.Values {
		if seen[uint64(e.V)] {
			return fmt.Errorf("%s.values[%d]: duplicate value %#x", path, i, uint64(e.V))
		}
		seen[uint64(e.V)] = true
		if uint64(e.V)&^widthMask(width) != 0 {
			return fmt.Errorf("%s.values[%d]: %#x wider than the port", path, i, uint64(e.V))
		}
	}
	return nil
}

// buildPorts indexes every port number. Range entries and block registers
// are expanded into the map: the largest range is 16 ports and the blocks
// add under 100 entries, so a flat map keeps lookups to one probe.
func buildPorts(f portFile, file string) (*portTable, error) {
	return buildPortsShadow(f, file, nil)
}

// buildPortsShadow is buildPorts where the block registers at the ports in
// shadow are left out, because a project override defines those ports
// itself.
func buildPortsShadow(f portFile, file string, shadow map[uint64]bool) (*portTable, error) {
	t := &portTable{file: f, byNum: make(map[uint64][]PortHit, 256)}
	type key struct {
		port         uint64
		access, cond string
	}
	seen := map[key]string{}
	add := func(h PortHit, path string) error {
		k := key{h.Port, h.Reg.Access, h.Reg.Cond}
		if prev, dup := seen[k]; dup {
			return fmt.Errorf("%s: port %#x (access %q) already defined at %s", path, h.Port, h.Reg.Access, prev)
		}
		// A read-and-write entry conflicts with a read-only or write-only one.
		for _, o := range t.byNum[h.Port] {
			if o.Reg.Cond == h.Reg.Cond && (o.Reg.Access == "" || h.Reg.Access == "") {
				return fmt.Errorf("%s: port %#x overlaps %s", path, h.Port, o.Name())
			}
		}
		seen[k] = path
		t.byNum[h.Port] = append(t.byNum[h.Port], h)
		return nil
	}
	for i := range t.file.Ports {
		p := &t.file.Ports[i]
		path := fmt.Sprintf("%s: ports[%d]", file, i)
		if err := finishPort(p, path); err != nil {
			return nil, err
		}
		last := uint64(p.Last)
		if last == 0 {
			last = uint64(p.Port)
		}
		if last-uint64(p.Port) > 64 {
			return nil, fmt.Errorf("%s: range of %d ports, use a block", path, last-uint64(p.Port)+1)
		}
		for n := uint64(p.Port); n <= last; n++ {
			if err := add(PortHit{Port: n, Reg: p}, path); err != nil {
				return nil, err
			}
		}
	}
	for bi := range t.file.Blocks {
		b := &t.file.Blocks[bi]
		bpath := fmt.Sprintf("%s: blocks[%d]", file, bi)
		if b.Span <= 0 || len(b.Bases) == 0 {
			return nil, fmt.Errorf("%s: needs span and bases", bpath)
		}
		for ri := range b.Regs {
			r := &b.Regs[ri]
			path := fmt.Sprintf("%s.regs[%d]", bpath, ri)
			if err := finishPort(r, path); err != nil {
				return nil, err
			}
			if int(r.Port) >= b.Span || r.Last != 0 {
				return nil, fmt.Errorf("%s.port: offset %#x outside span %d", path, uint64(r.Port), b.Span)
			}
			for _, base := range b.Bases {
				h := PortHit{Port: uint64(base.Base) + uint64(r.Port), Reg: r, Unit: base.Name, Block: b}
				if shadow[h.Port] {
					continue
				}
				if err := add(h, path); err != nil {
					return nil, err
				}
			}
		}
	}
	for k := range t.byNum {
		hs := t.byNum[k]
		// Read entries first, then write, so "status / command" reads in
		// the order the register is usually explained.
		sort.SliceStable(hs, func(i, j int) bool { return accessRank(hs[i].Reg) < accessRank(hs[j].Reg) })
	}
	return t, nil
}

func accessRank(p *Port) int {
	r := 0
	switch p.Access {
	case "r":
		r = 1
	case "w":
		r = 2
	}
	if p.Cond != "" {
		r += 3
	}
	return r
}

// LookupPort returns every register at port, read entries before write
// entries, unconditional before conditional (DLAB).
func (d *DB) LookupPort(port uint64) []PortHit {
	return d.portTab().byNum[port]
}

// PortFor picks the register an access actually reaches: the write entry for
// an out, the read entry for an in. Conditional entries (DLAB=1) are only
// returned when nothing unconditional matches.
func (d *DB) PortFor(port uint64, write bool) (PortHit, bool) {
	var cond PortHit
	found := false
	for _, h := range d.LookupPort(port) {
		if (write && !h.Reg.Writable()) || (!write && !h.Reg.Readable()) {
			continue
		}
		if h.Reg.Cond == "" {
			return h, true
		}
		if !found {
			cond, found = h, true
		}
	}
	return cond, found
}

// PortIndex names the register an index port selects: the CMOS register
// for a value written to 0x70, the sequencer register for 0x3C4.
func (d *DB) PortIndex(port, value uint64) (string, bool) {
	h, ok := d.PortFor(port, true)
	if !ok {
		return "", false
	}
	n := h.Reg.ValueName(value)
	return n, n != ""
}

func hitsName(hits []PortHit) string {
	var names []string
	for _, h := range hits {
		n := h.Name()
		switch {
		case h.Reg.Cond != "":
			n += " (" + h.Reg.Cond + ")"
		case h.Reg.Access == "r" && len(hits) > 1:
			n += " (read)"
		case h.Reg.Access == "w" && len(hits) > 1:
			n += " (write)"
		}
		names = append(names, n)
	}
	return strings.Join(names, " / ")
}
