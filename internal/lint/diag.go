package lint

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Source is the Diagnostic.Source of every finding.
const Source = "falcon-asm"

// DocsBase is where docs/RULES.md is browsable; a rule's anchor is its
// code, so a client can link each diagnostic to its documentation.
const DocsBase = "https://git.airies.net/vifair22/falcon-asm/-/blob/master/docs/RULES.md"

// DocURL is the documentation link of a rule.
func DocURL(code string) string { return DocsBase + "#" + code }

// Diagnostic converts a finding for the wire.
func (f Finding) Diagnostic() protocol.Diagnostic {
	d := protocol.Diagnostic{
		Range:           f.Range,
		Severity:        protocol.DiagnosticSeverity(f.Severity),
		Code:            f.Code,
		CodeDescription: &protocol.CodeDescription{Href: DocURL(f.Code)},
		Source:          Source,
		Message:         f.Message,
	}
	for _, l := range f.Related {
		d.RelatedInformation = append(d.RelatedInformation, protocol.DiagnosticRelatedInformation{Location: l, Message: "related"})
	}
	return d
}

// Overlaps reports whether the finding's range touches r. Both ends are
// inclusive, so an empty range (a cursor) at either edge of a finding
// counts, which is where an editor asks for code actions.
func (f Finding) Overlaps(r protocol.Range) bool {
	return lessPos(f.Range.Start, r.End) <= 0 && lessPos(r.Start, f.Range.End) <= 0
}

// Markdown renders docs/RULES.md from the registry.
func Markdown() string {
	var b strings.Builder
	b.WriteString("# falcon-asm rules\n\n")
	b.WriteString("Generated from the rule registry by `make rules-doc`; do not edit by hand.\n")
	b.WriteString("A test fails when this file and the registry disagree.\n\n")
	b.WriteString("Every rule can be configured per project and per path in\n")
	b.WriteString("`.falcon-asm.json` (`\"rules\": {\"<code>\": \"off\"}`) and suppressed in\n")
	b.WriteString("the source with a comment: `falcon-asm: ignore=<code>` on the line,\n")
	b.WriteString("`ignore-next=<code>` for the next code line, `ignore-file=<code>`, or an\n")
	b.WriteString("`ignore-begin=<code>` ... `ignore-end=<code>` region. `all` matches every\n")
	b.WriteString("rule. See docs/DESIGN.md (Lint engine) for the full reference.\n\n")
	b.WriteString("| Code | Section | Default | Summary |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	rules := Rules()
	for _, r := range rules {
		fmt.Fprintf(&b, "| [%s](#%s) | %d | %s | %s |\n", r.Code, r.Code, r.Section, r.Default, mdCell(r.Doc.Summary))
	}
	for _, r := range rules {
		fmt.Fprintf(&b, "\n## %s\n\n", r.Code)
		fmt.Fprintf(&b, "- Section: %d (%s)\n", r.Section, SectionNames[r.Section])
		fmt.Fprintf(&b, "- Default severity: %s\n", r.Default)
		fmt.Fprintf(&b, "- Runs on: %s\n", r.Langs)
		var needs []string
		if r.NeedsFlow {
			needs = append(needs, "dataflow")
		}
		if r.NeedsListing {
			needs = append(needs, "the assembler listing (runs only after a toolchain run)")
		}
		if len(needs) > 0 {
			fmt.Fprintf(&b, "- Needs: %s\n", strings.Join(needs, ", "))
		}
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(r.Doc.Summary))
		if why := strings.TrimSpace(r.Doc.Why); why != "" {
			fmt.Fprintf(&b, "\n%s\n", why)
		}
		if r.Doc.Bad != "" {
			fmt.Fprintf(&b, "\nBad:\n\n```asm\n%s\n```\n", strings.TrimRight(r.Doc.Bad, "\n"))
		}
		if r.Doc.Good != "" {
			fmt.Fprintf(&b, "\nGood:\n\n```asm\n%s\n```\n", strings.TrimRight(r.Doc.Good, "\n"))
		}
	}
	return b.String()
}

// mdCell keeps a summary inside its table cell.
func mdCell(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "|", "\\|")
}

// Text renders one rule's documentation for a terminal.
func (r *Rule) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (section %d, %s; default %s; runs on %s)\n\n", r.Code, r.Section, SectionNames[r.Section], r.Default, r.Langs)
	fmt.Fprintf(&b, "%s\n", strings.TrimSpace(r.Doc.Summary))
	if why := strings.TrimSpace(r.Doc.Why); why != "" {
		fmt.Fprintf(&b, "\n%s\n", why)
	}
	if r.Doc.Bad != "" {
		fmt.Fprintf(&b, "\nBad:\n%s\n", indent(r.Doc.Bad))
	}
	if r.Doc.Good != "" {
		fmt.Fprintf(&b, "\nGood:\n%s\n", indent(r.Doc.Good))
	}
	fmt.Fprintf(&b, "\n%s\n", DocURL(r.Code))
	return b.String()
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}
