package isa

import "testing"

// BenchmarkLoad is the first-use cost of the instruction tables: parsing
// both embedded JSON files and building the name index. load() runs it once
// per process behind a sync.Once; this calls the steps directly so every
// iteration is cold.
func BenchmarkLoad(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sys, err := readTable(dataFS, systemFile)
		if err != nil {
			b.Fatal(err)
		}
		xed, err := readTable(dataFS, xedFile)
		if err != nil {
			b.Fatal(err)
		}
		buildIndex(xed, sys)
	}
}

// BenchmarkLookup resolves a mix of exact, suffixed and unknown words.
func BenchmarkLookup(b *testing.B) {
	words := []string{"movl", "lgdt", "setb", "vpaddd", "cwtl", "notaninsn", "rep", "pushw", "iretq"}
	load()
	b.ReportAllocs()
	for b.Loop() {
		for _, w := range words {
			Lookup(w)
		}
	}
}
