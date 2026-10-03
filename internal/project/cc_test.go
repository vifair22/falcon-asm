package project

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// TestInferCC covers the 32-bit convention flags: -mregparm=N and -mrtd,
// the last of each winning as it does for gcc.
func TestInferCC(t *testing.T) {
	cases := []struct {
		args []string
		mode int
		want string
	}{
		{nil, 32, "cdecl"},
		{[]string{"-mregparm=3"}, 32, "regparm(3)"},
		{[]string{"-mregparm=2", "-mregparm=0"}, 32, "cdecl"},
		{[]string{"-mregparm=9"}, 32, "cdecl"},
		{[]string{"-mrtd"}, 32, "stdcall"},
		{[]string{"-mrtd", "-mno-rtd"}, 32, "cdecl"},
		{[]string{"-mregparm=3"}, 64, "sysv"},
		{nil, 16, ""},
	}
	for _, tc := range cases {
		if got := inferCC(toolchain.Flags{Compiler: "gcc", Mode: tc.mode, Args: tc.args}); got != tc.want {
			t.Errorf("%v %d: %q, want %q", tc.args, tc.mode, got, tc.want)
		}
	}
}
