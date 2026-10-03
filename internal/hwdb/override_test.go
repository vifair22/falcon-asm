package hwdb

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestLoadEmpty(t *testing.T) {
	for _, raw := range []string{"", "  ", "null"} {
		db, err := Load(json.RawMessage(raw))
		if err != nil || db != Default() {
			t.Errorf("Load(%q) = %v, %v", raw, db, err)
		}
	}
}

func TestLoadMerge(t *testing.T) {
	db, err := Load(json.RawMessage(`{
		"ports": [
			{"port": "0x510", "name": "fw_cfg selector (project)", "widths": [16]},
			{"port": "0x92", "notes": ["Board X: bit 0 is wired to the BMC."]},
			{"port": "0x3FD", "name": "Debug UART status", "access": "r"},
			{"port": "0x600", "last": "0x603", "name": "Board GPIO", "fields": [{"name": "LED", "bits": "0"}]}
		],
		"msrs": [
			{"index": "0xC0011029", "name": "DE_CFG", "vendor": "amd", "fields": [{"name": "LFENCE_SERIALIZE", "bits": "1"}], "reserved": "0x1"},
			{"index": "0xC0000080", "detail": "Project note on EFER.", "notes": ["Set NXE before paging."]},
			{"index": "0x26F", "fields": [{"name": "ALL", "bits": "63:0"}]}
		],
		"bios": [
			{"int": "0x15", "ah": "0xE8", "al": "0x20", "quirks": ["Board X returns ECX = 20 even when asked for 24."], "mayClobber": ["esi"]},
			{"int": "0x15", "ah": "0x5F", "name": "OEM hook", "err": "cf", "in": [["AH", "5Fh"]]}
		],
		"helpers": [
			{"name": "port_out8", "kind": "port", "write": true, "value": 1, "width": 8},
			{"name": "cpuid", "kind": "none"},
			{"name": "rdmsr", "kind": "msr", "arg": 1}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	// Added and merged ports.
	if n, _, ok := db.Port(0x510); !ok || n != "fw_cfg selector (project)" {
		t.Errorf("0x510 = %q", n)
	}
	h, _ := db.PortFor(0x92, true)
	if h.Reg.Name != "System control port A" || len(h.Reg.Fields) == 0 || !strings.Contains(strings.Join(h.Reg.Notes, " "), "BMC") {
		t.Errorf("0x92 merge = %+v", h.Reg)
	}
	// The project's 0x3FD shadows COM1 LSR.
	if n, _, ok := db.Port(0x3FD); !ok || n != "Debug UART status" {
		t.Errorf("0x3FD = %q", n)
	}
	if n, _, _ := db.Port(0x2FD); n != "COM2 LSR" {
		t.Errorf("0x2FD = %q", n)
	}
	if n, _, _ := db.Port(0x602); n != "Board GPIO" {
		t.Errorf("0x602 = %q", n)
	}
	// Default is untouched.
	if n, _, _ := Default().Port(0x3FD); n != "COM1 LSR" {
		t.Errorf("default 0x3FD = %q", n)
	}
	h, _ = Default().PortFor(0x92, true)
	if strings.Contains(strings.Join(h.Reg.Notes, " "), "BMC") {
		t.Error("override leaked into the default table")
	}

	// MSRs.
	if n, _, ok := db.MSR(0xC0011029); !ok || n != "DE_CFG" {
		t.Errorf("DE_CFG = %q", n)
	}
	if p := db.CheckMSRWrite(0xC0011029, 1); len(p) != 1 {
		t.Errorf("DE_CFG reserved: %v", p)
	}
	m, _ := db.LookupMSR(0xC0000080)
	if m.Detail != "Project note on EFER." || m.Reserved != 0x2FE || len(m.Notes) != 3 {
		t.Errorf("EFER merge = %+v", m)
	}
	m, _ = db.LookupMSR(0x26F)
	if len(m.Fields) != 1 {
		t.Errorf("0x26F fields = %v", m.Fields)
	}
	if _, _, ok := Default().MSR(0xC0011029); ok {
		t.Error("override leaked into the default MSRs")
	}

	// BIOS.
	s, _ := db.LookupBIOS(0x15, 0xE8, 0x20, true)
	if !strings.Contains(strings.Join(s.Quirks, " "), "Board X") || s.Name != "Query system address map" {
		t.Errorf("E820 merge = %+v", s)
	}
	if !strings.Contains(strings.Join(s.MayClobber, ","), "esi") {
		t.Error("mayClobber not appended")
	}
	if n, _, ok := db.BIOS(0x15, 0x5F); !ok || !strings.Contains(n, "OEM hook") {
		t.Errorf("OEM hook = %q", n)
	}
	s, _ = Default().LookupBIOS(0x15, 0xE8, 0x20, true)
	if strings.Contains(strings.Join(s.Quirks, " "), "Board X") {
		t.Error("override leaked into the default BIOS table")
	}

	// Helpers.
	hp, ok := db.Helper("port_out8")
	if !ok || hp.Kind != "port" || *hp.Value != 1 {
		t.Errorf("port_out8 = %+v", hp)
	}
	if _, ok := db.Helper("cpuid"); ok {
		t.Error("cpuid not removed")
	}
	if hp, _ := db.Helper("rdmsr"); hp.Arg != 1 {
		t.Error("rdmsr not replaced")
	}
	if _, ok := Default().Helper("cpuid"); !ok {
		t.Error("default cpuid helper gone")
	}
	if len(db.Helpers()) != len(Default().Helpers()) {
		t.Errorf("helpers: %d vs %d", len(db.Helpers()), len(Default().Helpers()))
	}
	// Untouched sections fall back to the built-ins.
	db2, err := Load(json.RawMessage(`{"helpers": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if n, _, _ := db2.Port(0x92); n != "System control port A" {
		t.Error("ports fallback")
	}
	if n, _, _ := db2.MSR(0x10); n != "IA32_TIME_STAMP_COUNTER" {
		t.Error("msrs fallback")
	}
	if n, _, _ := db2.BIOS(0x12, 0); n == "" {
		t.Error("bios fallback")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		raw, want string
	}{
		{`[1]`, "hwdb:"},
		{`{"portz": []}`, `unknown section "portz"`},
		{`{"ports": {}}`, "ports: want an array"},
		{`{"ports": [{"port": "0x10", "nmae": "x"}]}`, `ports[0]: json: unknown field "nmae"`},
		{`{"ports": [{"port": "zz", "name": "x"}]}`, "ports[0]: bad number"},
		{`{"ports": [{"port": "0x700"}]}`, "ports[0].name: empty"},
		{`{"ports": [{"port": "0x700", "name": "x", "fields": [{"name": "A", "bits": "9"}]}]}`, "ports[0].fields[0].bits"},
		{`{"ports": [{"port": "0x92", "fields": [{"name": "A", "bits": "x"}]}]}`, "ports[0].fields[0].bits"},
		{`{"ports": [{"port": "0x700", "name": "a"}, {"port": "0x700", "name": "b", "access": "r"}]}`, "overlaps"},
		{`{"msrs": [{"index": "0x5"}]}`, "msrs[0].name: empty"},
		{`{"msrs": [{"index": "0x5", "name": "x", "like": "0x268"}]}`, "msrs[0].like"},
		{`{"msrs": [{"index": "0xC0000080", "reserved": "0x1"}]}`, "msrs[0].reserved"},
		{`{"msrs": [{"index": "0x5", "name": "x", "fields": [{"name": "A", "bits": "64"}]}]}`, "msrs[0].fields[0].bits"},
		{`{"msrs": 3}`, "msrs: want an array"},
		{`{"msrs": [{"bogus": 1}]}`, "msrs[0]"},
		{`{"bios": [{"int": "0x15", "ah": "0x5F"}]}`, "bios[0].name: empty"},
		{`{"bios": [{"int": "0x13", "ah": "0x42", "err": "sometimes"}]}`, "bios[0].err"},
		{`{"bios": [{"int": "0x13", "name": "x", "struct": ["nope"]}]}`, "bios[0].struct"},
		{`{"bios": "x"}`, "bios: want an array"},
		{`{"bios": [{"bogus": 1}]}`, "bios[0]"},
		{`{"helpers": [{"name": "f", "kind": "dma"}]}`, "helpers[0].kind"},
		{`{"helpers": [{"kind": "port"}]}`, "helpers[0].name: empty"},
		{`{"helpers": [{"name": "f", "kind": "port", "arg": -1}]}`, "start at 0"},
		{`{"helpers": [{"name": "f", "kind": "port", "width": 64}]}`, "helpers[0].width"},
		{`{"helpers": [{"name": "f", "kind": "port", "x": 1}]}`, "helpers[0]"},
		{`{"helpers": 1}`, "helpers: want an array"},
	}
	for _, tt := range tests {
		_, err := Load(json.RawMessage(tt.raw))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Load(%s) err = %v; want %q", tt.raw, err, tt.want)
		}
	}
}

// Loading overrides while other goroutines read the default tables must
// not race (run with -race).
func TestLoadConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = Load(json.RawMessage(`{"ports":[{"port":"0x92","notes":["x"]}],"msrs":[{"index":"0x10","notes":["y"]}],"bios":[{"int":"0x13","ah":"0x42","quirks":["z"]}]}`))
		}()
		go func() {
			defer wg.Done()
			d := Default()
			_ = d.PortHover(0x92, 8, true)
			_ = d.MSRHover(0x10, nil)
			_ = d.BIOSHover(0x13, 0x42, 0, false)
		}()
	}
	wg.Wait()
}
