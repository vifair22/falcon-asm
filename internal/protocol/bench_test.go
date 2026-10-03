package protocol

import (
	"encoding/json"
	"testing"
)

// BenchmarkMarshalSemanticTokens encodes a 20k-line document's token
// stream (about 94k integers).
func BenchmarkMarshalSemanticTokens(b *testing.B) {
	data := make([]uint32, 94000)
	for i := range data {
		data[i] = uint32(i % 97)
	}
	t := SemanticTokens{Data: data}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(t); err != nil {
			b.Fatal(err)
		}
	}
}
