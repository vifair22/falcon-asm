package isa

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	stringLit    = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
	leadLabels   = regexp.MustCompile(`^\s*(?:[A-Za-z_.$0-9]+:\s*)*`)
	macroDef     = regexp.MustCompile(`(?m)^\s*\.macro\s+([A-Za-z_][A-Za-z_0-9]*)`)

	// generalInsn matches the ordinary integer instructions that tools/xedgen
	// supplies rather than system.json. They may stay unresolved until
	// xed.json is generated; everything else in mnemonic position must
	// resolve from the hand-written table today.
	generalInsn = regexp.MustCompile(`^(?:` +
		`(?:mov|add|adc|sub|sbb|xor|or|and|test|cmp|inc|dec|neg|not|shr|shl|sar|sal|rol|ror|rcl|rcr|` +
		`div|idiv|mul|imul|lea|push|pop|xchg|bt|bts|btr|btc|bsf|bsr|bswap|lds|les|lfs|lgs|lss)[bwlq]?` +
		`|j(?:n?(?:e|z|c|o|s|p|a|ae|b|be|g|ge|l|le|na|nae|nb|nbe|ng|nge|nl|nle)|pe|po)` +
		`|set(?:n?(?:e|z|c|o|s|p|a|ae|b|be|g|ge|l|le))` +
		`)$`)

	// externalMacros are invoked by the fixtures but defined in headers
	// from airies-boot that are not part of testdata.
	externalMacros = map[string]bool{"safe_mbr_hook": true}
)

// mnemonicWords is a deliberately crude statement scanner (the real lexer
// is a separate package and must not be a dependency here). It returns the
// word in mnemonic position of every instruction statement, skipping
// prefixes, plus the names of macros the files define.
func mnemonicWords(t *testing.T) (words map[string][]string, macros map[string]bool) {
	t.Helper()
	files, err := filepath.Glob("../../testdata/falcon-bios/*.S")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	words = map[string][]string{}
	macros = map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := blockComment.ReplaceAllStringFunc(string(raw), func(c string) string {
			return strings.Repeat("\n", strings.Count(c, "\n"))
		})
		for _, m := range macroDef.FindAllStringSubmatch(src, -1) {
			macros[strings.ToLower(m[1])] = true
		}
		for n, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue // cpp directive
			}
			line = stringLit.ReplaceAllString(line, `""`)
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i]
			}
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			for _, stmt := range strings.Split(line, ";") {
				fields := strings.Fields(leadLabels.ReplaceAllString(stmt, ""))
				for len(fields) > 0 && IsPrefix(fields[0]) {
					fields = fields[1:]
				}
				if len(fields) == 0 || strings.HasPrefix(fields[0], ".") || strings.HasPrefix(fields[0], `\`) {
					continue
				}
				w := strings.ToLower(fields[0])
				words[w] = append(words[w], filepath.Base(f)+":"+itoa(n+1))
			}
		}
	}
	return words, macros
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

func TestFalconBIOSMnemonicsResolve(t *testing.T) {
	words, macros := mnemonicWords(t)
	var unresolved []string
	for w, where := range words {
		if _, _, ok := Lookup(w); ok || macros[w] || externalMacros[w] || generalInsn.MatchString(w) {
			continue
		}
		unresolved = append(unresolved, w+" ("+where[0]+")")
	}
	sort.Strings(unresolved)
	if len(unresolved) > 0 {
		t.Errorf("mnemonics with no table entry:\n  %s", strings.Join(unresolved, "\n  "))
	}

	// The scanner must actually be seeing the system code; a scan that
	// silently found nothing would pass the check above.
	for _, w := range []string{"cli", "sti", "hlt", "int", "iret", "lgdt", "lgdtl", "lidt", "sgdtl",
		"smsw", "ljmp", "ljmpl", "lcall", "lret", "inb", "outb", "inl", "outl", "lodsb", "stosb",
		"stosw", "movsb", "movsw", "pushfl", "popfl", "pushfw", "popfw", "pushal", "popal", "jcxz",
		"loop", "cpuid", "movzwl", "movzbl", "cld", "nop", "call", "ret", "jmp"} {
		if len(words[w]) == 0 {
			t.Errorf("fixture scan did not find %q", w)
		}
	}
}
