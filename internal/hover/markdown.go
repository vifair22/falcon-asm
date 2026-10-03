package hover

import "strings"

// mdSpecial are the characters that can start markdown syntax inside plain
// text. Source words such as __start or *ptr would otherwise render as
// emphasis.
const mdSpecial = "\\`*_[]<>#|~"

// escape makes s render literally as markdown text.
func escape(s string) string {
	if !strings.ContainsAny(s, mdSpecial) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(mdSpecial, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func bold(s string) string {
	return "**" + escape(s) + "**"
}

// code renders s as an inline code span that survives backticks in source
// text. CommonMark closes a span only on a backtick run of the same length
// as the opener, so the fence is one longer than the longest run inside;
// a space pads the content when it starts or ends with a backtick, and the
// renderer strips exactly one such space from each side. Newlines would end
// the paragraph, so they become spaces.
func code(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if longest > 0 && (s[0] == '`' || s[len(s)-1] == '`') {
		s = " " + s + " "
	}
	return fence + s + fence
}
