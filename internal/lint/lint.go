// Package lint is the rule engine: a registry of rules with stable codes,
// per-project configuration (.falcon-asm.json), in-source suppression
// comments, and the runner that turns rule findings into diagnostics and
// quick fixes. Rules live in internal/rules/<area> and register themselves
// from init(); internal/rules/all imports them all.
package lint

import (
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Severity is the LSP severity; Off disables a rule.
type Severity int

// Severities.
const (
	Off     Severity = 0
	Error   Severity = 1
	Warning Severity = 2
	Info    Severity = 3
	Hint    Severity = 4
)

// Lang says which documents a rule runs on.
type Lang int

// Languages.
const (
	LangAsm    Lang = 1 << iota // .S / .s documents
	LangInline                  // asm statements inside C/C++ (template as its own program)
	LangC                       // the C document itself (helper calls, inline asm operand lists)
)

// Doc is a rule's documentation; docs/RULES.md is generated from it.
type Doc struct {
	Summary string // one line
	Why     string // the reasoning, a paragraph
	Bad     string // a minimal source example that triggers the rule
	Good    string // the fixed version
}

// Rule is one check.
type Rule struct {
	Code    string // stable, kebab-case, e.g. "operand-no-form"
	Section int    // roadmap section number (1..12)
	Default Severity
	Langs   Lang
	Doc     Doc
	// NeedsFlow requests the CFG and dataflow (computed once per document,
	// shared by all rules).
	NeedsFlow bool
	// NeedsListing requests the last toolchain result; the rule is skipped
	// when there is none.
	NeedsListing bool
	// SkipExpansions keeps the rule off the synthetic programs built from
	// in-file macro invocations (Input.ExpandMacros). Rules that judge a
	// document as a whole, such as the symbol rules, would see a fragment
	// whose labels are referenced from outside it and report nonsense.
	// NeedsFlow and NeedsListing rules are never run on expansions either:
	// an expansion has no listing, and a fragment has no control flow of
	// its own.
	SkipExpansions bool
	Check          func(c *Context)
}

// Fix is a quick fix offered with a finding.
type Fix struct {
	Title string
	Edits []protocol.TextEdit // in the document's coordinates
}

// Finding is one problem a rule reported.
type Finding struct {
	Code     string
	Severity Severity
	Range    protocol.Range
	Message  string
	Fixes    []Fix
	Related  []protocol.Location // optional, e.g. the GDT entry a selector names
}

// Target is the resolved build target for a document.
type Target struct {
	Mode    asm.Mode          // initial mode
	Level   int               // x86-64 level 0..4
	CPU     string            // target CPU profile, "" = none
	CC      string            // default calling convention, "" = none
	Sources map[string]string // where each value came from, for messages
}

// Context is what a rule's Check sees. Report is the only way to emit.
type Context struct {
	Prog    *asm.Program
	Target  Target
	Listing *toolchain.AsmResult // nil unless the rule NeedsListing and one exists
	Tables  Tables               // hardware tables with project overrides merged

	rule     *Rule
	cfg      *flow.CFG
	flowRes  *flow.Result
	findings *[]Finding
	lazy     func() (*flow.CFG, *flow.Result)

	in  *Input   // the run's input: language, config, memo, inline statements
	sev Severity // the severity this rule reports at for this file
}

// Workspace is what a rule may learn about the rest of the project from
// the workspace symbol index, as seen from the document being linted: the
// files it includes, the files that include it, and the global symbols of
// every other assembly source, C source and linker script under the
// workspace roots. internal/symbols implements it; the caller builds one
// per document and the index behind it is shared, so asking costs a map
// lookup rather than a rescan. Everything excludes the document itself,
// whose own symbols the rule reads from Prog.
type Workspace interface {
	// Defined reports whether name is defined where the document can see
	// it: in a file it includes or that includes it, as a global label of
	// any assembly source, or anywhere in a C source or linker script
	// (which are not parsed, so any mention counts).
	Defined(name string) bool
	// Referenced reports whether name is used outside the document by a
	// file that sees the document's definition (an includer or an
	// include), or is named in a linker script.
	Referenced(name string) bool
	// GlobalDefinitions returns where name is defined as a label in a file
	// assembled into the same program as the document: one it includes,
	// or another assembled source that is linked with it (one of the two
	// references a global only the other defines).
	GlobalDefinitions(name string) []protocol.Location
	// Complete reports whether every #include and .include of the
	// document was found; when one was not, an unknown name may be
	// defined there and rules about undefined names stay quiet.
	Complete() bool
}

// Tables is the hardware knowledge rules may consult; implemented by
// internal/hwdb. Kept as an interface so lint does not depend on hwdb's
// data loading.
type Tables interface {
	Port(port uint64) (name, detail string, ok bool)
	MSR(index uint64) (name, detail string, ok bool)
	BIOS(vector, ah uint64) (name, detail string, ok bool)
}
