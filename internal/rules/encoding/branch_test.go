package encoding

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// listing builds a toolchain result placing each line at an offset; a
// negative offset is a line that emits nothing.
func listing(section string, offs []int64, sizes []int) *toolchain.AsmResult {
	r := &toolchain.AsmResult{}
	for i, o := range offs {
		l := toolchain.ListingLine{Line: i, Section: section}
		if o >= 0 {
			l.Offset, l.HasOffset = uint64(o), true
			l.Bytes = make([]byte, sizes[i])
		}
		r.Listing = append(r.Listing, l)
	}
	return r
}

func TestBranchRange(t *testing.T) {
	far := "1:\n\tnop\n\t.fill 200, 1, 0x90\n\tloop 1b\n"
	cases := []struct {
		name string
		text string
		lst  *toolchain.AsmResult
		want string
	}{
		{"far back", far, listing(".text", []int64{-1, 0, 1, 201}, []int{0, 1, 200, 2}), CodeBranchRange + "@3"},
		{"near back", far, listing(".text", []int64{-1, 0, 1, 21}, []int{0, 1, 20, 2}), ""},
		{"label shares its line", "1:\tnop\n\t.fill 200, 1, 0x90\n\tjecxz 1b\n",
			listing(".text", []int64{0, 1, 201}, []int{1, 200, 2}), CodeBranchRange + "@2"},
		{"forward", "\tloop 2f\n\t.fill 300, 1, 0x90\n2:\n\tret\n",
			listing(".text", []int64{0, 2, -1, 302}, []int{2, 300, 0, 1}), CodeBranchRange + "@0"},
		{"other section", "\tloop 2f\n\t.fill 300, 1, 0x90\n2:\n\tret\n",
			func() *toolchain.AsmResult {
				r := listing(".text", []int64{0, 2, -1, 302}, []int{2, 300, 0, 1})
				r.Listing[3].Section = ".data"
				return r
			}(), ""},
		{"location move between", "\tloop 2f\n2:\n\t.org 0x400\n\tret\n",
			listing(".text", []int64{0, -1, -1, 0x400}, []int{2, 0, 0, 1}), ""},
		{"unknown addresses", far, func() *toolchain.AsmResult {
			r := listing(".text", []int64{-1, 0, 1, 201}, []int{0, 1, 200, 2})
			r.Listing[3].Unknown = true
			return r
		}(), ""},
		{"two statements on the line", "1:\n\tnop\n\t.fill 200, 1, 0x90\n\tdec %ecx; loop 1b\n",
			listing(".text", []int64{-1, 0, 1, 201}, []int{0, 1, 200, 3}), ""},
		{"jmp relaxes", "1:\n\tnop\n\t.fill 200, 1, 0x90\n\tjmp 1b\n",
			listing(".text", []int64{-1, 0, 1, 201}, []int{0, 1, 200, 5}), ""},
		{"external target", "\tloop elsewhere\n", listing(".text", []int64{0}, []int{2}), ""},
		{"no listing", far, nil, ""},
	}
	for _, tc := range cases {
		got := lintText(t, tc.text, asm.Mode32, lint.LangAsm, tc.lst)
		var codes []string
		for _, f := range got {
			if f.Code == CodeBranchRange {
				codes = append(codes, summary([]lint.Finding{f}))
			}
		}
		if s := strings.Join(codes, " "); s != tc.want {
			t.Errorf("%s: got %q, want %q (%v)", tc.name, s, tc.want, got)
		}
		if tc.want != "" && !strings.Contains(got[len(got)-1].Message, "-128..+127") {
			t.Errorf("%s: message %q", tc.name, got[len(got)-1].Message)
		}
	}
}
