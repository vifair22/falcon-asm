package toolchain

import (
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		err  bool
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		{in: "gcc -c a.S", want: []string{"gcc", "-c", "a.S"}},
		{in: "gcc\t-m32  \n -c", want: []string{"gcc", "-m32", "-c"}},
		{in: `gcc -DNAME="a b" x`, want: []string{"gcc", "-DNAME=a b", "x"}},
		{in: `gcc '-DX=it''s'`, want: []string{"gcc", "-DX=its"}},
		{in: `gcc '-DX="q"'`, want: []string{"gcc", `-DX="q"`}},
		{in: `gcc -DS=\"str\"`, want: []string{"gcc", `-DS="str"`}},
		{in: `gcc "a\"b" "c\\d" "e\$f" "g\h" "i\` + "`" + `j"`, want: []string{"gcc", `a"b`, `c\d`, `e$f`, `g\h`, "i`j"}},
		{in: `gcc a\ b`, want: []string{"gcc", "a b"}},
		{in: "gcc a\\\nb", want: []string{"gcc", "ab"}},
		{in: "gcc \"a\\\nb\"", want: []string{"gcc", "ab"}},
		{in: `gcc "" ''`, want: []string{"gcc", "", ""}},
		{in: `gcc "unterminated`, err: true},
		{in: `gcc 'unterminated`, err: true},
		{in: `gcc trailing\`, err: true},
		{in: `gcc "trailing\`, err: true},
	}
	for _, tt := range tests {
		got, err := SplitCommand(tt.in)
		if (err != nil) != tt.err {
			t.Errorf("SplitCommand(%q) err = %v, want err %v", tt.in, err, tt.err)
			continue
		}
		if !tt.err && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
