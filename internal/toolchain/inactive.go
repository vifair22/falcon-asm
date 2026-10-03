package toolchain

import (
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// condKind classifies a preprocessor conditional directive.
type condKind int

const (
	condNone condKind = iota
	condIf            // #if, #ifdef, #ifndef
	condElse          // #elif, #elifdef, #elifndef, #else
	condEnd           // #endif
)

// directiveName returns the directive word of a cpp line, "" if the line
// is not a directive. `# 12 "x"` markers and `# text` gas comments yield ""
// for the name but ok true, since cpp passes them through in assembler mode.
func directiveName(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(s, "#") {
		return "", false
	}
	s = strings.TrimLeft(s[1:], " \t")
	n := 0
	for n < len(s) && (s[n] >= 'a' && s[n] <= 'z' || s[n] == '_') {
		n++
	}
	return s[:n], true
}

func condOf(name string) condKind {
	switch name {
	case "if", "ifdef", "ifndef":
		return condIf
	case "elif", "elifdef", "elifndef", "else":
		return condElse
	case "endif":
		return condEnd
	}
	return condNone
}

// region is one branch body of a conditional. Its segments are the body's
// line spans with nested conditionals cut out, because the directive lines
// of a nested group are not part of either body.
type region struct {
	parent   int // enclosing region; -1 for the top level
	group    int // id shared by the branches of one #if group
	evidence int // lines that would produce output if active
	present  int // evidence lines that did produce output
	segs     [][2]int
	open     int // start of the current segment, -1 when none
}

// commentState strips C comments line by line so comment-only lines are
// not counted as evidence: cpp removes comments whether or not the branch
// is active, so their absence from the output proves nothing.
type commentState struct{ inBlock bool }

func (c *commentState) strip(line string) string {
	var b strings.Builder
	inStr := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if c.inBlock {
			if ch == '*' && i+1 < len(line) && line[i+1] == '/' {
				c.inBlock = false
				i++
				b.WriteByte(' ')
			}
			continue
		}
		switch {
		case inStr:
			if ch == '\\' && i+1 < len(line) {
				b.WriteByte(ch)
				i++
				ch = line[i]
			} else if ch == '"' {
				inStr = false
			}
		case ch == '"':
			inStr = true
		case ch == '/' && i+1 < len(line) && line[i+1] == '*':
			c.inBlock = true
			i++
			continue
		case ch == '/' && i+1 < len(line) && line[i+1] == '/':
			return b.String()
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// noOutputDirectives produce no line in cpp output even when active, so
// they cannot serve as evidence either way.
var noOutputDirectives = map[string]bool{
	"error": true, "warning": true, "line": true, "assert": true,
	"unassert": true, "ident": true, "sccs": true,
}

// InactiveRanges finds the conditional bodies of the main file that the
// preprocessor removed. A body is inactive when it holds lines that would
// produce output and none did; a body with no such lines (only comments or
// #warning) is inactive when its enclosing body is, or when a sibling
// branch of the same group is known to be the active one. Directive lines
// themselves are never part of a range. present comes from
// LineMap.Present over output produced with -dD, so #define and #undef
// lines count as output.
func InactiveRanges(src string, present map[int]bool) []protocol.Range {
	lines := strings.Split(src, "\n")
	regs := []*region{{parent: -1, group: -1, open: 0}}
	cur := 0
	nextGroup := 0
	var groups [][]int // group -> branch regions
	type frame struct{ group, parent int }
	var stack []frame
	cs := &commentState{}
	closeSeg := func(r *region, endExcl int) {
		if r.open >= 0 && endExcl > r.open {
			r.segs = append(r.segs, [2]int{r.open, endExcl})
		}
		r.open = -1
	}
	startBranch := func(group, parent, at int) {
		regs = append(regs, &region{parent: parent, group: group, open: at})
		cur = len(regs) - 1
		groups[group] = append(groups[group], cur)
	}
	for i := 0; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], "\r")
		wasBlock := cs.inBlock
		code := cs.strip(raw)
		// Backslash continuation joins lines before directives are seen.
		first := i
		for strings.HasSuffix(raw, "\\") && i+1 < len(lines) {
			i++
			raw = strings.TrimRight(lines[i], "\r")
			code += cs.strip(raw)
		}
		name, isDir := "", false
		if !wasBlock {
			name, isDir = directiveName(code)
		}
		kind := condNone
		if isDir {
			kind = condOf(name)
		}
		switch kind {
		case condIf:
			closeSeg(regs[cur], first)
			groups = append(groups, nil)
			stack = append(stack, frame{group: nextGroup, parent: cur})
			startBranch(nextGroup, cur, i+1)
			nextGroup++
			continue
		case condElse:
			if len(stack) == 0 {
				continue
			}
			closeSeg(regs[cur], first)
			fr := stack[len(stack)-1]
			startBranch(fr.group, fr.parent, i+1)
			continue
		case condEnd:
			if len(stack) == 0 {
				continue
			}
			closeSeg(regs[cur], first)
			fr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cur = fr.parent
			regs[cur].open = i + 1
			continue
		}
		if strings.TrimSpace(code) == "" || isDir && noOutputDirectives[name] {
			continue
		}
		r := regs[cur]
		r.evidence++
		if present[first] {
			r.present++
		}
	}
	closeSeg(regs[cur], len(lines))

	// Resolve in creation order: a parent region is always created before
	// its children, so its verdict is ready when a child asks.
	inactive := make([]bool, len(regs))
	known := make([]bool, len(regs)) // decided by evidence alone
	for i, r := range regs {
		if r.parent < 0 {
			continue
		}
		if r.evidence > 0 {
			inactive[i], known[i] = r.present == 0, true
		}
	}
	for i, r := range regs {
		if r.parent < 0 {
			continue
		}
		if inactive[r.parent] {
			inactive[i] = true
			continue
		}
		if known[i] {
			continue
		}
		for _, sib := range groups[r.group] {
			if sib != i && known[sib] && !inactive[sib] {
				inactive[i] = true
				break
			}
		}
	}
	var out []protocol.Range
	for i, r := range regs {
		if !inactive[i] {
			continue
		}
		for _, s := range r.segs {
			out = append(out, protocol.Range{
				Start: protocol.Position{Line: s[0]},
				End:   protocol.Position{Line: s[1]},
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Line < out[j].Start.Line })
	return out
}
