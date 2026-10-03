package hwdb

import (
	"encoding/json"
	"testing"
)

// loadAll parses and indexes every embedded table from scratch, bypassing
// the sync.Once caches: the cost a first hover of each kind pays in total.
func loadAll(b testing.TB) {
	if _, err := loadPorts(); err != nil {
		b.Fatal(err)
	}
	if _, err := loadMSRs(); err != nil {
		b.Fatal(err)
	}
	if _, err := loadBIOS(); err != nil {
		b.Fatal(err)
	}
	if _, err := loadCPUID(); err != nil {
		b.Fatal(err)
	}
	if _, err := loadRegs(); err != nil {
		b.Fatal(err)
	}
	if _, err := loadProfiles(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkLoadAll(b *testing.B) {
	for b.Loop() {
		loadAll(b)
	}
}

func BenchmarkLoadPorts(b *testing.B) {
	for b.Loop() {
		_, _ = loadPorts()
	}
}

func BenchmarkLoadBIOS(b *testing.B) {
	for b.Loop() {
		_, _ = loadBIOS()
	}
}

func BenchmarkLoadCPUID(b *testing.B) {
	for b.Loop() {
		_, _ = loadCPUID()
	}
}

func BenchmarkLoadOverrides(b *testing.B) {
	raw := json.RawMessage(`{"ports":[{"port":"0x510","name":"x"}],"msrs":[{"index":"0x10","notes":["y"]}],"bios":[{"int":"0x13","ah":"0x42","quirks":["z"]}],"helpers":[{"name":"f","kind":"port"}]}`)
	for b.Loop() {
		if _, err := Load(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPortHover(b *testing.B) {
	d := Default()
	_ = d.PortHover(0x64, 8, true)
	for b.Loop() {
		_ = d.PortHover(0x64, 8, true)
	}
}

func BenchmarkDecodeDescriptor(b *testing.B) {
	for b.Loop() {
		_ = DecodeDescriptor(0x00CF9A000000FFFF, nil, false)
	}
}

func BenchmarkFeaturesForISASet(b *testing.B) {
	_ = FeaturesForISASet("AVX2")
	for b.Loop() {
		_ = FeaturesForISASet("AVX512_VBMI2_256")
	}
}
