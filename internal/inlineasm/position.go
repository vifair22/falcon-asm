package inlineasm

import (
	"sort"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// posConv converts byte offsets to LSP positions with one line index for the
// whole file. docstore.Document.Position would rebuild that index per call
// when used on a literal Document, which is quadratic over a file with many
// asm statements. Line terminators follow the LSP (and docstore) rules: \n,
// \r\n and a lone \r.
type posConv struct {
	src    string
	starts []int
}

func newPosConv(src string) posConv {
	starts := []int{0}
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\n':
			starts = append(starts, i+1)
		case '\r':
			if i+1 < len(src) && src[i+1] == '\n' {
				i++
			}
			starts = append(starts, i+1)
		}
	}
	return posConv{src: src, starts: starts}
}

func (c posConv) position(off int) protocol.Position {
	if off < 0 {
		off = 0
	}
	if off > len(c.src) {
		off = len(c.src)
	}
	line := sort.SearchInts(c.starts, off+1) - 1
	return protocol.Position{Line: line, Character: docstore.UTF16Len(c.src[c.starts[line]:off])}
}
