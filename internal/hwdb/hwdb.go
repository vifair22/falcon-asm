// Package hwdb is the hardware knowledge the linter and hover consult: legacy
// BIOS services and the structures they take, PC I/O ports, MSRs, CPUID
// leaves and feature bits, CPU profiles, and decoders for control registers,
// descriptors and page table entries.
//
// The tables are hand-written JSON under data/, embedded in the binary and
// parsed lazily, one table at a time, the first time something asks for it:
// a port hover never pays for the BIOS table. They come from the Intel SDM,
// the AMD APM, the IBM PC/AT and Phoenix BIOS references, the PCI Local Bus
// specification and the ATA specification; no third-party data is copied.
// Where a source was not certain the field is left empty or the entry carries
// a "confidence" note, because a wrong hover is worse than a missing one.
//
// A project can merge its own ports, MSRs, BIOS services and C helper names
// over the built-ins with Load (the "hwdb" object of .falcon-asm.json).
package hwdb

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed data/*.json
var dataFS embed.FS

// DB is a view of the tables: the built-ins, or the built-ins with a
// project's overrides merged. It is immutable once built and safe for
// concurrent use. A nil field means "the built-in table", which is what keeps
// Default free: nothing is parsed until a lookup needs it.
type DB struct {
	ports   *portTable
	msrs    *msrTable
	bios    *biosTable
	helpers map[string]*Helper
}

var defaultDB = &DB{}

// Default is the built-in tables with no project overrides.
func Default() *DB { return defaultDB }

// lazy holds one parsed built-in table behind its own sync.Once.
type lazy[T any] struct {
	once sync.Once
	v    T
	err  error
}

func (l *lazy[T]) get(build func() (T, error)) T {
	l.once.Do(func() { l.v, l.err = build() })
	return l.v
}

var (
	builtinPorts    lazy[*portTable]
	builtinMSRs     lazy[*msrTable]
	builtinBIOS     lazy[*biosTable]
	builtinCPUID    lazy[*cpuidTable]
	builtinRegs     lazy[*regTable]
	builtinProfiles lazy[*profileTable]
)

func (d *DB) portTab() *portTable {
	if d != nil && d.ports != nil {
		return d.ports
	}
	return builtinPorts.get(loadPorts)
}

func (d *DB) msrTab() *msrTable {
	if d != nil && d.msrs != nil {
		return d.msrs
	}
	return builtinMSRs.get(loadMSRs)
}

func (d *DB) biosTab() *biosTable {
	if d != nil && d.bios != nil {
		return d.bios
	}
	return builtinBIOS.get(loadBIOS)
}

func (d *DB) helperTab() map[string]*Helper {
	if d != nil && d.helpers != nil {
		return d.helpers
	}
	return defaultHelpers
}

func cpuidTab() *cpuidTable     { return builtinCPUID.get(loadCPUID) }
func regTab() *regTable         { return builtinRegs.get(loadRegs) }
func profileTab() *profileTable { return builtinProfiles.get(loadProfiles) }

// DataError reports a defect in the embedded tables (a parse or validation
// failure), forcing every table to load. The data is compiled in, so this is
// a build defect: the server logs it and the tests assert it is nil. A table
// that fails to load is replaced by an empty one so lookups degrade to "not
// known" instead of crashing.
func DataError() error {
	Default().portTab()
	Default().msrTab()
	Default().biosTab()
	cpuidTab()
	regTab()
	profileTab()
	for _, e := range []error{builtinPorts.err, builtinMSRs.err, builtinBIOS.err,
		builtinCPUID.err, builtinRegs.err, builtinProfiles.err} {
		if e != nil {
			return e
		}
	}
	return nil
}

// readJSON decodes an embedded table. Unknown fields are rejected so a typo
// in the hand-written data fails the tests instead of silently dropping a
// value.
func readJSON(name string, v any) error {
	raw, err := dataFS.ReadFile("data/" + name)
	if err != nil {
		return fmt.Errorf("hwdb: %w", err)
	}
	return decodeStrict(raw, v, name)
}

func decodeStrict(raw []byte, v any, what string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("hwdb: %s: %w", what, err)
	}
	return nil
}

// Port implements lint.Tables: the register's name and a one-line detail.
// A port with different meanings for read and write (0x64: status / command)
// reports both, joined with " / ".
func (d *DB) Port(port uint64) (name, detail string, ok bool) {
	hits := d.LookupPort(port)
	if len(hits) == 0 {
		return "", "", false
	}
	return hitsName(hits), hits[0].Reg.Detail, true
}

// MSR implements lint.Tables.
func (d *DB) MSR(index uint64) (name, detail string, ok bool) {
	m, ok := d.LookupMSR(index)
	if !ok {
		return "", "", false
	}
	return m.NameFor(index), m.Detail, true
}

// BIOS implements lint.Tables: the service for INT vector with AH, or for a
// vector that takes no function number (INT 11h, 12h, 19h ...) whatever AH
// is. When AH only selects a group whose members AL picks (INT 15h AH=E8h),
// the name lists the group.
func (d *DB) BIOS(vector, ah uint64) (name, detail string, ok bool) {
	if s, ok := d.LookupBIOS(vector, ah, 0, false); ok {
		return s.Title(), s.Detail(), true
	}
	if g := d.biosTab().group(vector, ah); len(g) > 0 {
		return groupTitle(vector, ah, g), "", true
	}
	return "", "", false
}
