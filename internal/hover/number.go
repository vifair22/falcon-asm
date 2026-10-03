package hover

import (
	"strconv"
	"strings"
)

// parseNumber reads a GAS integer literal: 0x/0X hex, 0b/0B binary, a
// leading 0 for octal, plain decimal, or a character constant ('c, '\n,
// '\x41, '\101, with or without the closing quote C habit adds). It is
// stricter than strconv's base 0, which accepts 0o and digit underscores
// that GAS does not. Anything else, including values past 64 bits (GAS
// bignums), is reported as not a number rather than shown wrong.
func parseNumber(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	if s[0] == '\'' {
		return parseChar(s[1:])
	}
	base, digits := 10, s
	switch {
	case len(s) > 2 && s[0] == '0' && (s[1]|0x20) == 'x':
		base, digits = 16, s[2:]
	case len(s) > 2 && s[0] == '0' && (s[1]|0x20) == 'b':
		base, digits = 2, s[2:]
	case len(s) > 1 && s[0] == '0':
		base, digits = 8, s[1:]
	}
	if strings.ContainsAny(digits, "_+-") {
		return 0, false
	}
	v, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// charEscapes are the single-letter escapes GAS understands in character
// constants.
var charEscapes = map[byte]uint64{
	'b': 8, 'f': 12, 'n': 10, 'r': 13, 't': 9, 'v': 11,
	'\\': '\\', '\'': '\'', '"': '"',
}

// parseChar decodes the body of a character constant (after the opening
// quote). Non-ASCII characters are rejected: GAS's value for a multi-byte
// UTF-8 character is the first byte, which is surprising enough that showing
// it would mislead more than help.
func parseChar(s string) (uint64, bool) {
	s = strings.TrimSuffix(s, "'")
	if s == "" {
		// A bare quote is the quote character itself ('' in GAS).
		return 0, false
	}
	if s[0] != '\\' {
		if len(s) != 1 || s[0] >= 0x80 {
			return 0, false
		}
		return uint64(s[0]), true
	}
	esc := s[1:]
	switch {
	case esc == "":
		return 0, false
	case len(esc) == 1 && charEscapes[esc[0]] != 0:
		return charEscapes[esc[0]], true
	case (esc[0]|0x20) == 'x' && len(esc) > 1:
		return parseByte(esc[1:], 16)
	case esc[0] >= '0' && esc[0] <= '7':
		return parseByte(esc, 8)
	}
	return 0, false
}

// parseByte parses an escape's digits as one byte. strconv reports the
// clamped maximum alongside a range error, so the value is dropped on any
// error rather than passed through.
func parseByte(digits string, base int) (uint64, bool) {
	v, err := strconv.ParseUint(digits, base, 8)
	if err != nil {
		return 0, false
	}
	return v, true
}
