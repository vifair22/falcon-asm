package constexpr

import (
	"errors"
	"testing"
)

func TestEval(t *testing.T) {
	macros := map[string]string{
		"GDT_CODE32": "1",
		"SEL_CODE32": "(GDT_CODE32 * 8)",
		"CR0_PE":     "(1 << 0)",
		"CR0_PG":     "(1U << 31)",
		"BOTH":       "CR0_PE | CR0_PG",
		"LOOP_A":     "LOOP_B",
		"LOOP_B":     "LOOP_A + 1",
		"ZERO":       "0",
		"EMPTY":      "",
	}
	lookup := func(n string) (string, bool) {
		v, ok := macros[n]
		return v, ok
	}
	tests := []struct {
		expr string
		want int64
		err  error
	}{
		{expr: "42", want: 42},
		{expr: "0x1Fe", want: 0x1fe},
		{expr: "0b1010", want: 10},
		{expr: "017", want: 15},
		{expr: "0", want: 0},
		{expr: "10UL", want: 10},
		{expr: "'A'", want: 65},
		{expr: `'\n'`, want: 10},
		{expr: "1 + 2 * 3", want: 7},
		{expr: "(1 + 2) * 3", want: 9},
		{expr: "10 - 4 - 3", want: 3},
		{expr: "100 / 7 % 4", want: 2},
		{expr: "1 << 4 | 1", want: 17},
		{expr: "0xF0 & 0x3C ^ 0x01", want: 0x31},
		{expr: "-1", want: -1},
		{expr: "+5", want: 5},
		{expr: "~0", want: -1},
		{expr: "!0", want: 1},
		{expr: "!7", want: 0},
		{expr: "-(2 + 3)", want: -5},
		{expr: "SEL_CODE32", want: 8},
		{expr: "BOTH", want: 0x80000001},
		{expr: "1 + \\\n 2", want: 3},
		{expr: "1 >> 70", want: 0},
		{expr: "LOOP_A", err: ErrTooDeep},
		{expr: "1 / ZERO", err: ErrDivZero},
		{expr: "1 % 0", err: ErrDivZero},
		{expr: "NOPE", err: ErrUnknown},
		{expr: "EMPTY", err: ErrSyntax},
		{expr: "", err: ErrSyntax},
		{expr: "1 +", err: ErrSyntax},
		{expr: "(1", err: ErrSyntax},
		{expr: "1 2", err: ErrSyntax},
		{expr: ")", err: ErrSyntax},
		{expr: "0xZZ", err: ErrSyntax},
		{expr: "'a", err: ErrSyntax},
		{expr: `'ab'`, err: ErrSyntax},
		{expr: "1 < 2", err: ErrUnsupported},
		{expr: "a ? b : c", err: ErrUnsupported},
		{expr: "1 ? 2 : 3", err: ErrUnsupported},
		{expr: "x = 1", err: ErrUnsupported},
	}
	for _, tt := range tests {
		got, err := Eval(tt.expr, lookup)
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("Eval(%q) err = %v, want %v", tt.expr, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Eval(%q) = %d, %v; want %d", tt.expr, got, err, tt.want)
		}
	}
	if _, err := Eval("FOO", nil); !errors.Is(err, ErrUnknown) {
		t.Errorf("nil lookup: %v", err)
	}
}
