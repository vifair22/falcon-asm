package jsonrpc

import (
	"encoding/json"
	"io"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// BenchmarkReplyLarge sends a reply the size of a 20k-line document's
// semantic tokens (about 94k integers), the largest message the server
// writes.
func BenchmarkReplyLarge(b *testing.B) {
	data := make([]uint32, 94000)
	for i := range data {
		data[i] = uint32(i % 97)
	}
	result := struct {
		Data []uint32 `json:"data"`
	}{data}
	id := json.RawMessage("7")
	c := NewConn(nil, io.Discard)
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Reply(&id, result); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReplySemanticTokens is the same reply through the protocol type,
// which encodes itself.
func BenchmarkReplySemanticTokens(b *testing.B) {
	data := make([]uint32, 94000)
	for i := range data {
		data[i] = uint32(i % 97)
	}
	id := json.RawMessage("7")
	c := NewConn(nil, io.Discard)
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Reply(&id, protocol.SemanticTokens{Data: data}); err != nil {
			b.Fatal(err)
		}
	}
}
