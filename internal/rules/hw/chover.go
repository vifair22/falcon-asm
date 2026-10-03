package hw

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/constexpr"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
)

// CHover is the hardware hover for a C helper call (roadmap item 56):
// `outb(0x92, v)`, `inl(PCI_DATA)`, `rdmsr(MSR_EFER)`, `cpuid(7, r)`. The
// cursor must be on the helper's name or on the argument the helper's
// table entry names; that argument must be a constant or a macro that
// evaluates to one. Helper names and argument positions come from db
// (project overrides included). macro resolves names the document does not
// #define itself (the toolchain's table); it may be nil. Returns the
// markdown and the byte range it describes.
func CHover(text string, off int, db *hwdb.DB, macro func(string) (string, bool)) (string, int, int) {
	if off < 0 || off > len(text) {
		return "", 0, 0
	}
	call, ok := findCall(text, off, db)
	if !ok {
		return "", 0, 0
	}
	h := call.helper
	lookup := cLookup(text, macro)
	arg := func(n int) (uint64, bool) {
		if n < 0 || n >= len(call.args) {
			return 0, false
		}
		v, err := constexpr.Eval(stripCasts(call.args[n].text), lookup)
		return uint64(v), err == nil
	}
	key, ok := arg(h.Arg)
	if !ok {
		return "", 0, 0
	}
	var md string
	switch h.Kind {
	case "port":
		md = db.PortHover(key&0xFFFF, h.Width, h.Write)
		if md != "" && h.Write && h.Value != nil {
			if v, ok := arg(*h.Value); ok {
				if name, ok := db.PortIndex(key&0xFFFF, v); ok {
					md += fmt.Sprintf("\n\nwrites %#x: selects %s", v, name)
				}
			}
		}
	case "msr":
		var val *uint64
		if h.Write && h.Value != nil {
			if v, ok := arg(*h.Value); ok {
				val = &v
			}
		}
		md = db.MSRHover(key&0xFFFFFFFF, val)
	case "cpuid":
		sub := uint64(0)
		if h.Sub != nil {
			if v, ok := arg(*h.Sub); ok {
				sub = v
			}
		}
		md = db.CPUIDHover(key&0xFFFFFFFF, sub)
	}
	if md == "" {
		return "", 0, 0
	}
	return md, call.start, call.end
}

type cArg struct {
	text       string
	start, end int
}

type cCall struct {
	helper     *hwdb.Helper
	args       []cArg
	start, end int // the span the hover covers
}

// findCall finds the helper call the cursor is on: on its name, or inside
// the argument the helper's entry is keyed by.
func findCall(text string, off int, db *hwdb.DB) (cCall, bool) {
	// On a name: expand to the identifier.
	ws, we := off, off
	for ws > 0 && isCIdent(text[ws-1]) {
		ws--
	}
	for we < len(text) && isCIdent(text[we]) {
		we++
	}
	if we > ws {
		if c, ok := callAt(text, ws, we, db); ok {
			c.start, c.end = ws, we
			return c, true
		}
	}
	// Inside an argument: walk back to the unmatched '(' and its name.
	depth := 0
	for i := off - 1; i >= 0 && off-i < 512; i-- {
		switch text[i] {
		case ')':
			depth++
		case '(':
			if depth > 0 {
				depth--
				continue
			}
			ne := i
			for ne > 0 && (text[ne-1] == ' ' || text[ne-1] == '\t') {
				ne--
			}
			ns := ne
			for ns > 0 && isCIdent(text[ns-1]) {
				ns--
			}
			c, ok := callAt(text, ns, ne, db)
			if !ok || c.helper.Arg >= len(c.args) {
				return cCall{}, false
			}
			a := c.args[c.helper.Arg]
			if off < a.start || off > a.end {
				return cCall{}, false
			}
			c.start, c.end = a.start, a.end
			return c, true
		case ';', '{', '}':
			return cCall{}, false
		}
	}
	return cCall{}, false
}

// callAt parses `name(args)` with the name at [ws, we).
func callAt(text string, ws, we int, db *hwdb.DB) (cCall, bool) {
	if ws >= we || text[ws] >= '0' && text[ws] <= '9' {
		return cCall{}, false
	}
	h, ok := db.Helper(text[ws:we])
	if !ok {
		return cCall{}, false
	}
	i := we
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i >= len(text) || text[i] != '(' {
		return cCall{}, false
	}
	args, ok := splitArgs(text, i)
	if !ok {
		return cCall{}, false
	}
	return cCall{helper: h, args: args}, true
}

// splitArgs splits the argument list whose '(' is at open on top-level
// commas, skipping string and character literals.
func splitArgs(text string, open int) ([]cArg, bool) {
	var out []cArg
	depth := 0
	start := open + 1
	flush := func(end int) {
		s, e := start, end
		for s < e && (text[s] == ' ' || text[s] == '\t' || text[s] == '\n') {
			s++
		}
		for e > s && (text[e-1] == ' ' || text[e-1] == '\t' || text[e-1] == '\n') {
			e--
		}
		out = append(out, cArg{text: text[s:e], start: s, end: e})
	}
	for i := open + 1; i < len(text) && i-open < 4096; i++ {
		switch c := text[i]; c {
		case '"', '\'':
			for i++; i < len(text) && text[i] != c; i++ {
				if text[i] == '\\' {
					i++
				}
			}
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				if c != ')' {
					return nil, false
				}
				if i > open+1 || len(out) > 0 {
					flush(i)
				}
				return out, true
			}
			depth--
		case ',':
			if depth == 0 {
				flush(i)
				start = i + 1
			}
		case ';':
			return nil, false
		}
	}
	return nil, false
}

func isCIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// stripCasts removes leading C casts to integer types ("(uint16_t)0x92",
// "(unsigned long)X"), which constexpr does not read. Only casts whose
// parentheses hold identifier words are removed, so "(1 << 4)" stays.
func stripCasts(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "(") {
		end := strings.IndexByte(s, ')')
		if end < 0 {
			return s
		}
		inner := strings.TrimSpace(s[1:end])
		if inner == "" || strings.Trim(inner, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_0123456789 \t") != "" ||
			inner[0] >= '0' && inner[0] <= '9' || end == len(s)-1 {
			return s
		}
		s = strings.TrimSpace(s[end+1:])
	}
	return s
}

// cLookup resolves a name to an object-like macro body: the document's own
// #defines first (they reflect the text being edited), then macro.
func cLookup(text string, macro func(string) (string, bool)) constexpr.Lookup {
	var defs map[string]string
	return func(name string) (string, bool) {
		if defs == nil {
			defs = cDefines(text)
		}
		if b, ok := defs[name]; ok {
			return b, true
		}
		if macro != nil {
			return macro(name)
		}
		return "", false
	}
}

// cDefines collects `#define NAME value` lines (object-like, one line).
func cDefines(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimSpace(t[1:])
		rest, ok := strings.CutPrefix(t, "define")
		if !ok || rest == "" || rest[0] != ' ' && rest[0] != '\t' {
			continue
		}
		rest = strings.TrimSpace(rest)
		n := 0
		for n < len(rest) && isCIdent(rest[n]) {
			n++
		}
		if n == 0 || n < len(rest) && rest[n] == '(' {
			continue
		}
		body := strings.TrimSpace(rest[n:])
		if i := strings.Index(body, "/*"); i >= 0 {
			body = strings.TrimSpace(body[:i])
		}
		if i := strings.Index(body, "//"); i >= 0 {
			body = strings.TrimSpace(body[:i])
		}
		if strings.HasSuffix(body, "\\") {
			continue
		}
		out[rest[:n]] = body
	}
	return out
}
