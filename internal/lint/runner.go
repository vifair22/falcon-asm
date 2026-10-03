package lint

import (
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Input is one lint run over one program.
type Input struct {
	Prog    *asm.Program
	Lang    Lang
	Target  Target
	Listing *toolchain.AsmResult // the toolchain result describing this text; nil when none
	Tables  Tables
	Config  FileConfig // configuration resolved for the file
	// Rules restricts the run; nil means every registered rule. Severity
	// still comes from Config, so a listed rule configured off stays off.
	Rules []*Rule
	// Inline is the C document's asm statements, for LangC rules.
	Inline []inlineasm.Stmt
	// Memo is shared by the rules of the run (see Context.Memo). The
	// caller may pass one it seeded with values it already computed; nil
	// gets a fresh one.
	Memo *Memo
	// MapRange converts a range of Prog.Doc into the coordinates findings
	// are reported in (the C document, for an inline asm template). A
	// false result drops the finding. Nil is the identity.
	MapRange func(protocol.Range) (protocol.Range, bool)
	// MapEdit converts a fix's edit the same way; a false result drops the
	// whole fix. With MapRange set and MapEdit nil every fix is dropped,
	// since an edit valid in the template is not valid in the C source.
	MapEdit func(protocol.TextEdit) (protocol.TextEdit, bool)
	// Log receives one line per rule that panicked; nil discards.
	Log func(format string, args ...any)
	// Workspace is the workspace symbol view of the document (see
	// Context.Workspace); nil when there is none.
	Workspace Workspace
	// ExpandMacros also lints every invocation of an in-file gas .macro
	// (at most MaxExpansions per document): the expansion is parsed as a
	// program of its own, in the mode in effect at the call, and what the
	// rules find there is reported on the invocation, prefixed "in
	// expansion of NAME: ". Only LangAsm runs expand.
	ExpandMacros bool

	expansion bool // this run lints an expansion; it never expands again
}

// MaxExpansions caps the macro invocations linted per document, so a file
// that invokes a macro thousands of times (a generated table) costs a
// bounded amount per keystroke.
const MaxExpansions = 200

// Memo is a set of lazily computed values shared by the rules of one run,
// each computed at most once. Safe for concurrent use.
type Memo struct {
	mu sync.Mutex
	m  map[string]*memoEntry
}

type memoEntry struct {
	once sync.Once
	v    any
	// provide, when set by Provide, computes the value in place of the
	// first Get's own function.
	provide func() any
}

// NewMemo returns an empty memo.
func NewMemo() *Memo { return &Memo{m: map[string]*memoEntry{}} }

func (m *Memo) entry(key string) *memoEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]*memoEntry{}
	}
	e := m.m[key]
	if e == nil {
		e = &memoEntry{}
		m.m[key] = e
	}
	return e
}

// Get returns the value under key, calling fn to compute it on first use.
// The lock is not held while fn runs, so fn may itself use the memo. A fn
// that panics leaves the value nil for every later caller.
func (m *Memo) Get(key string, fn func() any) any {
	e := m.entry(key)
	e.once.Do(func() {
		m.mu.Lock()
		provide := e.provide
		m.mu.Unlock()
		if provide != nil {
			fn = provide
		}
		e.v = fn()
	})
	return e.v
}

// Provide registers how the caller computes the value under key, for a
// value it shares with something outside the run (the engine's per-version
// parse and dataflow, which hover also uses): the first Get calls fn
// instead of its own function, so the work is done once, by whichever
// side needs it first, and not at all when no rule asks. It has no effect
// once the key has a value or a provider; call it before the run starts.
func (m *Memo) Provide(key string, fn func() any) {
	e := m.entry(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.provide == nil {
		e.provide = fn
	}
}

// Set stores a value the caller already has, so the rules reuse it rather
// than compute it again. It has no effect once the key has a value.
func (m *Memo) Set(key string, v any) {
	e := m.entry(key)
	e.once.Do(func() { e.v = v })
}

const flowKey = "lint/flow"

// ProvideFlow makes fn the source of the run's CFG and dataflow solution
// (see Memo.Provide). fn must analyze the same program as the run's Prog
// with flow.Config{}; it may return nils when the analysis failed, which
// skips the rules that need flow, as a panic in the run's own analysis
// would.
func ProvideFlow(m *Memo, fn func() (*flow.CFG, *flow.Result)) {
	m.Provide(flowKey, func() any {
		g, r := fn()
		return flowPair{cfg: g, res: r}
	})
}

type flowPair struct {
	cfg *flow.CFG
	res *flow.Result
}

// flowOf builds the CFG and dataflow once per memo. A panic inside the flow
// package is contained here: every rule needing flow is then skipped for
// the document, rather than each one tripping over the same crash.
func flowOf(in *Input) (*flow.CFG, *flow.Result) {
	v := in.Memo.Get(flowKey, func() (out any) {
		defer func() {
			if p := recover(); p != nil {
				logPanic(in, "flow", p)
				out = flowPair{}
			}
		}()
		g := flow.Build(in.Prog)
		return flowPair{cfg: g, res: flow.Analyze(g, flow.Config{})}
	})
	fp, _ := v.(flowPair)
	return fp.cfg, fp.res
}

// panicked remembers which rules have been logged, so a rule that panics on
// every keystroke does not flood the server log.
var panicked sync.Map

func logPanic(in *Input, what string, p any) {
	if _, seen := panicked.LoadOrStore(what, true); seen || in.Log == nil {
		return
	}
	in.Log("lint: %s panicked and was skipped: %v\n%s", what, p, debug.Stack())
}

// Batch collects the raw findings of one or more runs over a document (a C
// file is one run per inline asm statement plus one for the C text) before
// suppressions, sorting and deduplication are applied once to the lot.
type Batch struct {
	raw []Finding
	ran map[string]bool
}

// Add runs the rules of in and keeps their findings.
func (b *Batch) Add(in Input) {
	if in.Prog == nil {
		return
	}
	if in.Memo == nil {
		in.Memo = NewMemo()
	}
	if b.ran == nil {
		b.ran = map[string]bool{}
	}
	rules := in.Rules
	if rules == nil {
		rules = Rules()
	}
	b.raw = append(b.raw, b.runRules(&in, rules)...)
	if in.ExpandMacros && in.Lang == LangAsm && !in.expansion && in.Prog.Doc != nil {
		b.raw = append(b.raw, expansionFindings(&in, rules)...)
	}
}

// runRules runs rules over one input and returns their mapped findings.
func (b *Batch) runRules(in *Input, rules []*Rule) []Finding {
	var all []Finding
	for _, r := range rules {
		if r.Langs&in.Lang == 0 || r.Code == UnusedSuppressionCode {
			continue
		}
		if in.expansion && (r.SkipExpansions || r.NeedsFlow || r.NeedsListing) {
			continue
		}
		sev := in.Config.SeverityOf(r)
		if sev == Off || r.NeedsListing && in.Listing == nil {
			continue
		}
		if !in.expansion {
			b.ran[r.Code] = true
		}
		c := &Context{Prog: in.Prog, Target: in.Target, Tables: in.Tables, rule: r, in: in, sev: sev}
		if r.NeedsListing {
			c.Listing = in.Listing
		}
		switch {
		case in.expansion:
			// A fragment has no control flow of its own; a rule that
			// asks gets nothing and must stay quiet.
			c.lazy = func() (*flow.CFG, *flow.Result) { return nil, nil }
		case r.NeedsFlow:
			if c.cfg, c.flowRes = flowOf(in); c.cfg == nil {
				continue
			}
		default:
			c.lazy = func() (*flow.CFG, *flow.Result) { return flowOf(in) }
		}
		var out []Finding
		c.findings = &out
		if !runRule(c, in) {
			continue
		}
		all = append(all, mapFindings(out, in)...)
	}
	return all
}

// expansionFindings lints the expansion of every in-file macro invocation
// (up to MaxExpansions) and moves what the rules find onto the invocation.
// The expansion is fully expanded already (nested invocations included)
// and is linted with expansion set, so it never expands again: that and
// the expander's own depth cap are the recursion guard.
func expansionFindings(in *Input, rules []*Rule) []Finding {
	exps := in.Prog.ExpandMacros(MaxExpansions)
	if len(exps) == 0 {
		return nil
	}
	var out []Finding
	var sub Batch
	sub.ran = map[string]bool{}
	for _, e := range exps {
		call := &in.Prog.Stmts[e.Stmt]
		if strings.TrimSpace(e.Text) == "" {
			continue
		}
		vdoc := &docstore.Document{URI: in.Prog.Doc.URI, Version: in.Prog.Doc.Version, Text: e.Text}
		prog := asm.Parse(vdoc, lexer.Tokenize(e.Text), asm.Config{InitialMode: call.Mode, Macros: in.Prog.Macros})
		tgt := in.Target
		// The rules judge the expansion in the mode in effect where it is
		// invoked, which is what gas does; the source map is copied so a
		// message can say where that mode came from.
		tgt.Mode = call.Mode
		src := make(map[string]string, len(in.Target.Sources)+1)
		for k, v := range in.Target.Sources {
			src[k] = v
		}
		src["mode"] = fmt.Sprintf("the mode at the invocation of %s on line %d", e.Macro, call.Line+1)
		tgt.Sources = src
		sin := Input{
			Prog: prog, Lang: LangAsm, Target: tgt, Tables: in.Tables, Config: in.Config,
			Memo: NewMemo(), Log: in.Log, expansion: true,
		}
		r := in.Prog.Range(call.Start, call.End)
		for _, f := range sub.runRules(&sin, rules) {
			f.Range = r
			f.Message = "in expansion of " + e.Macro + ": " + f.Message
			// Fixes and related locations are positions in the
			// expansion text, which exists nowhere the user can edit.
			f.Fixes, f.Related = nil, nil
			out = append(out, f)
		}
	}
	return out
}

// runRule calls the rule's Check, containing a panic: the rule's findings
// for this document are discarded (they may be half-built) and the rest of
// the rules still run.
func runRule(c *Context, in *Input) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			logPanic(in, "rule "+c.rule.Code, p)
			ok = false
		}
	}()
	c.rule.Check(c)
	return true
}

func mapFindings(fs []Finding, in *Input) []Finding {
	if in.MapRange == nil {
		return fs
	}
	out := fs[:0]
	for _, f := range fs {
		r, ok := in.MapRange(f.Range)
		if !ok {
			continue
		}
		f.Range = r
		f.Fixes = mapFixes(f.Fixes, in.MapEdit)
		var rel []protocol.Location
		for _, l := range f.Related {
			if l.URI == "" || in.Prog.Doc != nil && l.URI == in.Prog.Doc.URI {
				if l.Range, ok = in.MapRange(l.Range); !ok {
					continue
				}
			}
			rel = append(rel, l)
		}
		f.Related = rel
		out = append(out, f)
	}
	return out
}

func mapFixes(fixes []Fix, mapEdit func(protocol.TextEdit) (protocol.TextEdit, bool)) []Fix {
	if mapEdit == nil {
		return nil
	}
	var out []Fix
next:
	for _, fx := range fixes {
		edits := make([]protocol.TextEdit, 0, len(fx.Edits))
		for _, e := range fx.Edits {
			m, ok := mapEdit(e)
			if !ok {
				continue next
			}
			edits = append(edits, m)
		}
		out = append(out, Fix{Title: fx.Title, Edits: edits})
	}
	return out
}

// Finish applies the suppression comments to the collected findings, adds
// the unused-suppression findings, and returns everything sorted by
// position with exact duplicates removed. sup may be nil (no comments).
func (b *Batch) Finish(sup *Suppressions, cfg FileConfig) []Finding {
	out := b.raw
	if sup != nil {
		out = sup.Filter(out)
		if r, ok := Lookup(UnusedSuppressionCode); ok {
			if sev := cfg.SeverityOf(r); sev != Off {
				out = append(out, sup.Filter(sup.Unused(sev, b.ran))...)
			}
		}
	}
	return Finalize(out)
}

// Run lints one assembly program: the rules, then the suppression comments
// found in its tokens.
func Run(in Input) []Finding {
	var b Batch
	b.Add(in)
	var sup *Suppressions
	if in.Prog != nil && in.Prog.Doc != nil {
		sup = AsmSuppressions(in.Prog.Doc, in.Prog.Toks)
	}
	return b.Finish(sup, in.Config)
}

// Finalize sorts findings by position (then code and message, so the order
// is total and the output deterministic) and drops exact duplicates: two
// rules, or two passes over overlapping inline templates, saying the same
// thing at the same place.
func Finalize(fs []Finding) []Finding {
	sort.SliceStable(fs, func(i, j int) bool { return lessFinding(&fs[i], &fs[j]) })
	out := fs[:0]
	for i := range fs {
		if len(out) > 0 && sameFinding(&out[len(out)-1], &fs[i]) {
			continue
		}
		out = append(out, fs[i])
	}
	return out
}

func lessPos(a, b protocol.Position) int {
	switch {
	case a.Line != b.Line:
		return a.Line - b.Line
	default:
		return a.Character - b.Character
	}
}

func lessFinding(a, b *Finding) bool {
	if d := lessPos(a.Range.Start, b.Range.Start); d != 0 {
		return d < 0
	}
	if d := lessPos(a.Range.End, b.Range.End); d != 0 {
		return d < 0
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	return a.Message < b.Message
}

func sameFinding(a, b *Finding) bool {
	return a.Range == b.Range && a.Code == b.Code && a.Message == b.Message && a.Severity == b.Severity
}

// String renders a finding for logs and test failures.
func (f Finding) String() string {
	return fmt.Sprintf("%d:%d-%d:%d %s %s: %s", f.Range.Start.Line, f.Range.Start.Character,
		f.Range.End.Line, f.Range.End.Character, f.Severity, f.Code, f.Message)
}
