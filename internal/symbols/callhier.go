package symbols

import (
	"path/filepath"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// The call hierarchy (roadmap item 85) treats a label as a function when
// something calls it or it is .globl: assembly has no function syntax, and
// those are the two signs that a label is an entry point rather than a
// loop head. Calls are `call` and `lcall` statements; a `jmp` or `ljmp` to
// another function is a tail call and is listed too, marked as one.
// Indirect calls have no target to show and are left out.

// callSite is one call or tail call in a file.
type callSite struct {
	target string
	at     span // the target name
	tail   bool // a jmp rather than a call
	tok    int  // index of the mnemonic token, for ordering
	caller string
	from   span // the caller's label; zero when caller is ""
}

// callGraph is the call structure of one index view, built per request:
// call hierarchy queries are rare, and the files are immutable, so there is
// nothing to invalidate.
type callGraph struct {
	funcs map[string]bool
	sites map[*file][]callSite
	defs  map[*file][]span // function labels of each file, in order
}

// rawCalls finds the call and jump statements of f with a direct target.
func rawCalls(f *file) []callSite {
	var out []callSite
	toks := f.toks
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.Kind != lexer.Mnemonic {
			continue
		}
		inst, _, ok := isa.Lookup(t.Text)
		if !ok {
			continue
		}
		tail := false
		switch inst.Name {
		case "call", "lcall":
		case "jmp", "ljmp":
			tail = true
		default:
			continue
		}
		var target lexer.Token
		found, indirect := false, false
		for j := i + 1; j < len(toks) && toks[j].Line == t.Line; j++ {
			o := toks[j]
			if o.Kind == lexer.Punct && o.Text == ";" {
				break
			}
			switch {
			case o.Kind == lexer.Punct && o.Text == "*":
				indirect = true
			case o.Kind == lexer.LabelRef && !found:
				target, found = o, true
			case o.Kind == lexer.Immediate && isSymbolName(o.Text[1:]):
				// Far forms (`ljmp $SEL, $entry`) name the target last.
				target, found = o, true
			}
		}
		if !found || indirect {
			continue
		}
		s := callSite{target: target.Text, at: tokSpan(target), tail: tail, tok: i}
		if target.Kind == lexer.Immediate {
			s.target = target.Text[1:]
			s.at = span{target.Line, target.Col + 1, target.Len - 1}
		}
		out = append(out, s)
	}
	return out
}

func (v *view) callGraph() *callGraph {
	g := &callGraph{funcs: map[string]bool{}, sites: map[*file][]callSite{}, defs: map[*file][]span{}}
	raw := make(map[*file][]callSite, len(v.files))
	for _, f := range v.files {
		if f.cHeader {
			continue
		}
		raw[f] = rawCalls(f)
		for name := range f.globals {
			g.funcs[name] = true
		}
		for _, s := range raw[f] {
			if !s.tail {
				g.funcs[s.target] = true
			}
		}
	}
	for _, f := range v.files {
		sites := raw[f]
		k := 0
		caller, from := "", span{}
		toks := f.toks
		for i := 0; i < len(toks); i++ {
			t := toks[i]
			if t.Kind == lexer.LabelDef && g.funcs[t.Text] && i+1 < len(toks) && toks[i+1].Line == t.Line && toks[i+1].Text == ":" {
				caller, from = t.Text, tokSpan(t)
				g.defs[f] = append(g.defs[f], from)
			}
			for k < len(sites) && sites[k].tok == i {
				s := sites[k]
				s.caller, s.from = caller, from
				k++
				// A jump to a label that is not a function, or back to
				// the start of the current one, is control flow inside
				// a function, not a call.
				if s.tail && (!g.funcs[s.target] || s.target == caller) {
					continue
				}
				g.sites[f] = append(g.sites[f], s)
			}
		}
	}
	return g
}

// item builds the hierarchy item for function name defined at def in f;
// its range runs to the line before the file's next function label.
func (g *callGraph) item(f *file, name string, def span) protocol.CallHierarchyItem {
	end := f.lineCount() - 1
	for _, d := range g.defs[f] {
		if d.line > def.line {
			end = d.line - 1
			break
		}
	}
	sym := f.symbol(name, "", protocol.SymbolKindFunction, def, end)
	detail := filepath.Base(f.key)
	if f.globals[name] {
		detail += " .globl"
	}
	return protocol.CallHierarchyItem{
		Name: name, Kind: protocol.SymbolKindFunction, Detail: detail,
		URI: f.uri, Range: sym.Range, SelectionRange: sym.SelectionRange,
	}
}

// fileItem stands for a file's code before its first function label.
func fileItem(f *file) protocol.CallHierarchyItem {
	r := protocol.Range{}
	return protocol.CallHierarchyItem{
		Name: filepath.Base(f.key), Kind: protocol.SymbolKindFile, Detail: "top-level code",
		URI: f.uri, Range: r, SelectionRange: r,
	}
}

// labelDefAt finds a label definition of name in f on line.
func labelDefAt(f *file, name string, line int) (span, bool) {
	for _, i := range f.byName[name] {
		if d := f.defs[i]; d.kind == kindLabel && d.at.line == line {
			return d.at, true
		}
	}
	return span{}, false
}

// PrepareCallHierarchy returns the function at pos: the label named there
// when it is a function, else the function whose body holds pos.
func (ix *Index) PrepareCallHierarchy(uri string, pos protocol.Position) []protocol.CallHierarchyItem {
	if v, _, t, _, ok := ix.resolve(uri, pos); ok && !t.local && t.kind == kindLabel && len(t.defs) > 0 {
		g := v.callGraph()
		if g.funcs[t.name] {
			if hf, ok := v.byKey[uriKey(t.defs[0].URI)]; ok {
				if at, ok := labelDefAt(hf, t.name, t.defs[0].Range.Start.Line); ok {
					return []protocol.CallHierarchyItem{g.item(hf, t.name, at)}
				}
			}
		}
	}
	ix.ensureScanned()
	v := ix.snapshot()
	f, ok := v.byKey[uriKey(uri)]
	if !ok {
		return nil
	}
	line, _, ok := f.byteCol(pos)
	if !ok {
		return nil
	}
	g := v.callGraph()
	var enclosing *span
	for _, d := range g.defs[f] {
		if d.line > line {
			break
		}
		d := d
		enclosing = &d
	}
	if enclosing == nil {
		return nil
	}
	name := f.toks[tokenAt(f.toks, enclosing.line, enclosing.col)].Text
	return []protocol.CallHierarchyItem{g.item(f, name, *enclosing)}
}

// IncomingCalls lists the callers of item, grouped by calling function,
// across the index. A call counts only when its name resolves, from the
// calling file, to item's own definition, so two private labels that
// share a name in different files are not confused.
func (ix *Index) IncomingCalls(item protocol.CallHierarchyItem) []protocol.CallHierarchyIncomingCall {
	ix.ensureScanned()
	v := ix.snapshot()
	key, line := uriKey(item.URI), item.SelectionRange.Start.Line
	g := v.callGraph()
	type group struct {
		call  protocol.CallHierarchyIncomingCall
		calls int
		tails int
	}
	var groups []*group
	byCaller := map[string]*group{}
	for _, f := range v.files {
		resolved, checked := false, false
		for _, s := range g.sites[f] {
			if s.target != item.Name {
				continue
			}
			if !checked {
				checked = true
				if t, ok := v.lookup(f, item.Name); ok {
					for _, d := range t.defs {
						if uriKey(d.URI) == key && d.Range.Start.Line == line {
							resolved = true
						}
					}
				}
			}
			if !resolved {
				break
			}
			k := f.key + "\x00" + s.caller
			gr := byCaller[k]
			if gr == nil {
				gr = &group{}
				if s.caller == "" {
					gr.call.From = fileItem(f)
				} else {
					gr.call.From = g.item(f, s.caller, s.from)
				}
				byCaller[k] = gr
				groups = append(groups, gr)
			}
			gr.call.FromRanges = append(gr.call.FromRanges, f.rng(s.at))
			if s.tail {
				gr.tails++
			} else {
				gr.calls++
			}
		}
	}
	out := make([]protocol.CallHierarchyIncomingCall, 0, len(groups))
	for _, gr := range groups {
		gr.call.From.Detail += tailNote(gr.calls, gr.tails)
		out = append(out, gr.call)
	}
	return out
}

// OutgoingCalls lists what item calls, grouped by callee, in the order of
// the first call to each. Callees the index cannot resolve (a C function,
// an external symbol) have no location to show and are left out.
func (ix *Index) OutgoingCalls(item protocol.CallHierarchyItem) []protocol.CallHierarchyOutgoingCall {
	ix.ensureScanned()
	v := ix.snapshot()
	f, ok := v.byKey[uriKey(item.URI)]
	if !ok {
		return nil
	}
	g := v.callGraph()
	line := item.SelectionRange.Start.Line
	type group struct {
		call  protocol.CallHierarchyOutgoingCall
		calls int
		tails int
	}
	var groups []*group
	byTarget := map[string]*group{}
	for _, s := range g.sites[f] {
		if s.caller != item.Name || s.from.line != line {
			continue
		}
		gr, seen := byTarget[s.target]
		if !seen {
			byTarget[s.target] = nil
			t, ok := v.lookup(f, s.target)
			if !ok || len(t.defs) == 0 {
				continue
			}
			hf, ok := v.byKey[uriKey(t.defs[0].URI)]
			if !ok {
				continue
			}
			at, ok := labelDefAt(hf, s.target, t.defs[0].Range.Start.Line)
			if !ok {
				continue
			}
			gr = &group{call: protocol.CallHierarchyOutgoingCall{To: g.item(hf, s.target, at)}}
			byTarget[s.target] = gr
			groups = append(groups, gr)
		}
		if gr == nil {
			continue
		}
		gr.call.FromRanges = append(gr.call.FromRanges, f.rng(s.at))
		if s.tail {
			gr.tails++
		} else {
			gr.calls++
		}
	}
	out := make([]protocol.CallHierarchyOutgoingCall, 0, len(groups))
	for _, gr := range groups {
		gr.call.To.Detail += tailNote(gr.calls, gr.tails)
		out = append(out, gr.call)
	}
	return out
}

// tailNote marks a group whose calls are tail calls (jmp), so the tree
// says which edges return and which do not.
func tailNote(calls, tails int) string {
	switch {
	case tails == 0:
		return ""
	case calls == 0:
		return " - tail call (jmp)"
	}
	return " - call and tail call (jmp)"
}
