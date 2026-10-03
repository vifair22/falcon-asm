package toolchain

import (
	"reflect"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func lineRange(start, endExcl int) protocol.Range {
	return protocol.Range{Start: protocol.Position{Line: start}, End: protocol.Position{Line: endExcl}}
}

func TestInactiveRanges(t *testing.T) {
	tests := []struct {
		name    string
		src     []string
		present []int
		want    []protocol.Range
	}{
		{
			name: "if 0 body",
			src: []string{
				"nop",     // 0
				"#if 0",   // 1
				"bogus",   // 2
				"/* c */", // 3
				"#endif",  // 4
				"ret",     // 5
			},
			present: []int{0, 5},
			want:    []protocol.Range{lineRange(2, 4)},
		},
		{
			name: "else branch taken",
			src: []string{
				"#ifdef A",    // 0
				"#define B 1", // 1
				"#else",       // 2
				"#define B 2", // 3
				"#endif",      // 4
			},
			present: []int{1},
			want:    []protocol.Range{lineRange(3, 4)},
		},
		{
			name: "comment-only branch decided by its sibling",
			src: []string{
				"#if X",   // 0
				"// only", // 1
				"#elif Y", // 2
				"nop",     // 3
				"#endif",  // 4
			},
			present: []int{3},
			want:    []protocol.Range{lineRange(1, 2)},
		},
		{
			name: "nested inside inactive parent",
			src: []string{
				"#if 0",      // 0
				"a",          // 1
				"#ifdef Q",   // 2
				"",           // 3
				"#warning w", // 4
				"#endif",     // 5
				"b",          // 6
				"#endif",     // 7
				"c",          // 8
			},
			present: []int{8},
			want:    []protocol.Range{lineRange(1, 2), lineRange(3, 5), lineRange(6, 7)},
		},
		{
			name: "active include and continuation",
			src: []string{
				"#if 1",               // 0
				"#include \"x.inc\"",  // 1
				"#endif",              // 2
				"#if 1",               // 3
				"#define LONG \\",     // 4
				"  value",             // 5
				"#endif",              // 6
				"  /* block",          // 7
				"#if 0 still comment", // 8
				"*/",                  // 9
			},
			present: []int{1, 4},
			want:    nil,
		},
		{
			name: "stray else and endif ignored, unterminated if",
			src: []string{
				"#else",  // 0
				"#endif", // 1
				"#if 0",  // 2
				"x",      // 3
			},
			present: nil,
			want:    []protocol.Range{lineRange(3, 4)},
		},
		{
			name: "elif chain with spaces in directives",
			src: []string{
				"  #  if A", // 0
				"one",       // 1
				"# elif B",  // 2
				"two",       // 3
				"#else",     // 4
				"three",     // 5
				"#  endif",  // 6
			},
			present: []int{3},
			want:    []protocol.Range{lineRange(1, 2), lineRange(5, 6)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := map[int]bool{}
			for _, l := range tt.present {
				p[l] = true
			}
			got := InactiveRanges(strings.Join(tt.src, "\n"), p)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCommentStrip(t *testing.T) {
	cs := &commentState{}
	if got := cs.strip(`.ascii "/* not */" // gone`); got != `.ascii "/* not */" ` {
		t.Errorf("strip = %q", got)
	}
	if got := cs.strip(`a /* start`); got != "a " || !cs.inBlock {
		t.Errorf("strip = %q block %v", got, cs.inBlock)
	}
	if got := cs.strip(`end */ b "x\"y"`); got != `  b "x\"y"` || cs.inBlock {
		t.Errorf("strip = %q block %v", got, cs.inBlock)
	}
}
