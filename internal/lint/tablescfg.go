package lint

import (
	"encoding/json"
	"strconv"
	"strings"
)

// checkTables builds the hardware tables of a configuration's "tables" value
// once while the file is parsed, so a bad entry is reported in the
// configuration file itself, at the JSON path the loader names, instead of
// only as a log line. start is the byte offset of the value in the file, ks
// the "tables" key (the fallback position when the error names no path).
// The loader's result is cached by LoadTables, so the engine and the CLI
// asking again for the same value cost nothing.
func (p *configParser) checkTables(raw json.RawMessage, start int, ks span) {
	if _, err := LoadTables(raw); err != nil {
		msg := err.Error()
		s, e, ok := errorSpan(raw, msg)
		if !ok {
			p.problemAt(ks.start, ks.end, Error, "tables: %s; the built-in tables are used", msg)
			return
		}
		p.problemAt(start+s, start+e, Error, "tables: %s; the built-in tables are used", msg)
	}
}

// errorSpan finds the value an error message points at. Loaders prefix
// their messages ("hwdb: ports[2].fields[0].bits: ..."), so each
// colon-separated part is tried as a JSON path into raw, and the first that
// resolves wins.
func errorSpan(raw []byte, msg string) (int, int, bool) {
	for _, part := range strings.Split(msg, ": ") {
		segs, ok := parsePath(strings.TrimSpace(part))
		if !ok {
			continue
		}
		if s, e, ok := pathSpan(raw, segs); ok {
			return s, e, true
		}
	}
	return 0, 0, false
}

// pathSeg is one step of a path: an object key, or an array index when key
// is "" and idx >= 0.
type pathSeg struct {
	key string
	idx int
}

// parsePath reads "ports[2].fields[0].bits". Anything else (prose, an empty
// string) is not a path.
func parsePath(s string) ([]pathSeg, bool) {
	var out []pathSeg
	i := 0
	for i < len(s) {
		switch {
		case s[i] == '[':
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return nil, false
			}
			n, err := strconv.Atoi(s[i+1 : i+j])
			if err != nil || n < 0 {
				return nil, false
			}
			out = append(out, pathSeg{idx: n})
			i += j + 1
		case s[i] == '.' && len(out) > 0:
			i++
		default:
			j := i
			for j < len(s) && (isIdent(s[j]) || s[j] >= '0' && s[j] <= '9' && j > i) {
				j++
			}
			if j == i {
				return nil, false
			}
			out = append(out, pathSeg{key: s[i:j], idx: -1})
			i = j
		}
	}
	return out, len(out) > 0
}

func isIdent(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// pathSpan is the byte range of the value at segs within the JSON text raw.
func pathSpan(raw []byte, segs []pathSeg) (int, int, bool) {
	i := skipWS(raw, 0)
	for _, sg := range segs {
		if i >= len(raw) {
			return 0, 0, false
		}
		var ok bool
		if sg.idx >= 0 {
			i, ok = arrayElem(raw, i, sg.idx)
		} else {
			i, ok = objectMember(raw, i, sg.key)
		}
		if !ok {
			return 0, 0, false
		}
	}
	end, ok := skipValue(raw, i)
	return i, end, ok
}

// objectMember is the start of the value of key in the object at i.
func objectMember(raw []byte, i int, key string) (int, bool) {
	if raw[i] != '{' {
		return 0, false
	}
	i = skipWS(raw, i+1)
	for i < len(raw) && raw[i] != '}' {
		kEnd, ok := skipValue(raw, i)
		if !ok || raw[i] != '"' {
			return 0, false
		}
		var k string
		if json.Unmarshal(raw[i:kEnd], &k) != nil {
			return 0, false
		}
		i = skipWS(raw, kEnd)
		if i >= len(raw) || raw[i] != ':' {
			return 0, false
		}
		i = skipWS(raw, i+1)
		if k == key {
			return i, true
		}
		if i, ok = skipValue(raw, i); !ok {
			return 0, false
		}
		i = skipWS(raw, i)
		if i < len(raw) && raw[i] == ',' {
			i = skipWS(raw, i+1)
		}
	}
	return 0, false
}

// arrayElem is the start of element n of the array at i.
func arrayElem(raw []byte, i, n int) (int, bool) {
	if raw[i] != '[' {
		return 0, false
	}
	i = skipWS(raw, i+1)
	for k := 0; i < len(raw) && raw[i] != ']'; k++ {
		if k == n {
			return i, true
		}
		var ok bool
		if i, ok = skipValue(raw, i); !ok {
			return 0, false
		}
		i = skipWS(raw, i)
		if i < len(raw) && raw[i] == ',' {
			i = skipWS(raw, i+1)
		}
	}
	return 0, false
}

func skipWS(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
		i++
	}
	return i
}

// skipValue returns the offset just past the JSON value starting at i. The
// text is known to be valid JSON (the decoder accepted it), so this only
// has to find the end, not validate.
func skipValue(raw []byte, i int) (int, bool) {
	if i >= len(raw) {
		return 0, false
	}
	switch raw[i] {
	case '"':
		for j := i + 1; j < len(raw); j++ {
			switch raw[j] {
			case '\\':
				j++
			case '"':
				return j + 1, true
			}
		}
		return 0, false
	case '{', '[':
		depth := 0
		for j := i; j < len(raw); j++ {
			switch raw[j] {
			case '"':
				e, ok := skipValue(raw, j)
				if !ok {
					return 0, false
				}
				j = e - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	}
	j := i
	for j < len(raw) && !strings.ContainsRune(",]} \t\r\n", rune(raw[j])) {
		j++
	}
	return j, j > i
}
