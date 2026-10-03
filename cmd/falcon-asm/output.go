package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// byteColumn is the 1-based byte column of a position, which is what gcc
// prints and what editors' problem matchers expect from file:line:col.
func byteColumn(doc *docstore.Document, p protocol.Position) int {
	off, err := doc.Offset(p)
	if err != nil {
		return p.Character + 1
	}
	start, err := doc.Offset(protocol.Position{Line: p.Line})
	if err != nil {
		return p.Character + 1
	}
	return off - start + 1
}

// skipSummary totals the files whose listing rules did not run.
type skipSummary struct {
	files    int
	rules    int // distinct rule codes skipped somewhere
	byReason [numSkipReasons]int
	names    []string // the files, as displayed, in output order
}

func summarizeSkips(skipped []skippedFile) skipSummary {
	var s skipSummary
	codes := map[string]bool{}
	for _, sk := range skipped {
		s.files++
		s.byReason[sk.reason]++
		s.names = append(s.names, sk.file.display)
		for _, c := range sk.rules {
			codes[c] = true
		}
	}
	s.rules = len(codes)
	return s
}

// short is the qualifier the summary line carries: "7 listing rules
// skipped on 3 files".
func (s skipSummary) short() string {
	return fmt.Sprintf("%d listing rule%s skipped on %d file%s", s.rules, plural(s.rules, "", "s"), s.files, plural(s.files, "", "s"))
}

// maxNamed is how many skipped files the note names before "N more".
const maxNamed = 5

// note is the stderr line that says which rules did not run, why, and what
// to do about it. A clean result has to mean clean: without this line a
// run that never had a listing would read the same as one that passed.
func (s skipSummary) note() string {
	var why []string
	for r := skipReason(0); r < numSkipReasons; r++ {
		if n := s.byReason[r]; n > 0 {
			why = append(why, fmt.Sprintf("%d %s", n, skipPhrases[r]))
		}
	}
	names := s.names
	more := ""
	if len(names) > maxNamed {
		more = fmt.Sprintf(" and %d more", len(names)-maxNamed)
		names = names[:maxNamed]
	}
	var hint []string
	if s.byReason[skipNoEntry] > 0 {
		hint = append(hint, "give them a compile_commands.json entry (--compile-commands)")
	}
	if s.byReason[skipDisabled] > 0 {
		hint = append(hint, "drop --no-toolchain to run them")
	}
	if s.byReason[skipNoCompiler] > 0 {
		hint = append(hint, "install the compiler their entry names")
	}
	if s.byReason[skipFailed] > 0 {
		hint = append(hint, "fix the assembly errors")
	}
	hint = append(hint, "see falcon-asm lint --help")
	return fmt.Sprintf("note: %s (no assembler listing: %s): %s%s; %s", s.short(), strings.Join(why, ", "),
		strings.Join(names, ", "), more, strings.Join(hint, "; "))
}

// writeText prints gcc-style lines on stdout and, on stderr, the skipped
// listing rules note (when there is one) and a one-line summary, so stdout
// stays exactly one problem per line for tools.
func writeText(stdout, stderr io.Writer, items []item, files int, skip skipSummary) error {
	counts := map[lint.Severity]int{}
	for _, it := range items {
		counts[it.Severity]++
		if _, err := fmt.Fprintf(stdout, "%s:%d:%d: %s: %s [%s]\n", it.file.display, it.Range.Start.Line+1,
			byteColumn(it.doc, it.Range.Start), it.Severity, it.Message, it.Code); err != nil {
			return err
		}
	}
	qualifier := ""
	if skip.files > 0 {
		fmt.Fprintln(stderr, skip.note())
		qualifier = " (" + skip.short() + ")"
	}
	if len(items) == 0 {
		fmt.Fprintf(stderr, "%d file%s linted, no problems%s\n", files, plural(files, "", "s"), qualifier)
		return nil
	}
	var parts []string
	for _, s := range []lint.Severity{lint.Error, lint.Warning, lint.Info, lint.Hint} {
		if n := counts[s]; n > 0 {
			name := s.String()
			if s != lint.Info {
				name += plural(n, "", "s")
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, name))
		}
	}
	fmt.Fprintf(stderr, "%d problem%s (%s) in %d file%s linted%s\n", len(items), plural(len(items), "", "s"),
		strings.Join(parts, ", "), files, plural(files, "", "s"), qualifier)
	return nil
}

type jsonFix struct {
	Title string              `json:"title"`
	Edits []protocol.TextEdit `json:"edits"`
}

type jsonItem struct {
	File     string         `json:"file"`
	Line     int            `json:"line"`   // 1-based
	Column   int            `json:"column"` // 1-based, bytes
	Range    protocol.Range `json:"range"`  // LSP: 0-based, UTF-16
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Source   string         `json:"source"`
	Message  string         `json:"message"`
	Fixes    []jsonFix      `json:"fixes,omitempty"`
}

func writeJSON(w io.Writer, items []item) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonItems(items))
}

// jsonSkip is a file whose listing rules did not run, in json-full.
type jsonSkip struct {
	File   string   `json:"file"`
	Reason string   `json:"reason"` // one of skipKeys
	Rules  []string `json:"rules"`
}

// jsonFull is --format json-full: the findings plus what the plain array
// cannot carry, so a tool can tell a clean run from one that skipped rules.
type jsonFull struct {
	Findings []jsonItem `json:"findings"`
	Skipped  []jsonSkip `json:"skipped"`
	Files    int        `json:"files"`
}

func writeJSONFull(w io.Writer, items []item, skipped []skippedFile, files int) error {
	out := jsonFull{Findings: jsonItems(items), Skipped: []jsonSkip{}, Files: files}
	for _, sk := range skipped {
		out.Skipped = append(out.Skipped, jsonSkip{File: sk.file.display, Reason: skipKeys[sk.reason], Rules: sk.rules})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func jsonItems(items []item) []jsonItem {
	out := make([]jsonItem, 0, len(items))
	for _, it := range items {
		ji := jsonItem{
			File: it.file.display, Line: it.Range.Start.Line + 1, Column: byteColumn(it.doc, it.Range.Start),
			Range: it.Range, Code: it.Code, Severity: it.Severity.String(), Source: it.source, Message: it.Message,
		}
		for _, f := range it.Fixes {
			ji.Fixes = append(ji.Fixes, jsonFix{Title: f.Title, Edits: f.Edits})
		}
		out = append(out, ji)
	}
	return out
}

// SARIF 2.1.0, the subset code-scanning services read: the tool with its
// rule metadata, and results with locations and fixes.
type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool               sarifTool                   `json:"tool"`
	OriginalURIBaseIDs map[string]sarifArtifactLoc `json:"originalUriBaseIds,omitempty"`
	ColumnKind         string                      `json:"columnKind"`
	Results            []sarifResult               `json:"results"`
	Invocations        []sarifInvocation           `json:"invocations"`
}

// sarifInvocation reports skipped listing rules as tool execution
// notifications, one per file, which code-scanning services show beside
// the results.
type sarifInvocation struct {
	ExecutionSuccessful        bool                `json:"executionSuccessful"`
	ToolExecutionNotifications []sarifNotification `json:"toolExecutionNotifications,omitempty"`
}

type sarifNotification struct {
	Descriptor sarifDescriptorRef      `json:"descriptor"`
	Level      string                  `json:"level"`
	Message    sarifText               `json:"message"`
	Locations  []sarifArtifactLocation `json:"locations"`
	Properties map[string]any          `json:"properties,omitempty"`
}

type sarifDescriptorRef struct {
	ID string `json:"id"`
}

// sarifArtifactLocation is a location without a region: a notification
// is about a whole file.
type sarifArtifactLocation struct {
	PhysicalLocation struct {
		ArtifactLocation sarifArtifactLoc `json:"artifactLocation"`
	} `json:"physicalLocation"`
}

// sarifSkippedID is the notification descriptor of a skipped file.
const sarifSkippedID = "listing-rules-skipped"

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifText struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown,omitempty"`
}

type sarifRule struct {
	ID                   string        `json:"id"`
	ShortDescription     sarifText     `json:"shortDescription"`
	FullDescription      *sarifText    `json:"fullDescription,omitempty"`
	Help                 *sarifText    `json:"help,omitempty"`
	HelpURI              string        `json:"helpUri,omitempty"`
	DefaultConfiguration sarifRuleConf `json:"defaultConfiguration"`
}

type sarifRuleConf struct {
	Level   string `json:"level"`
	Enabled bool   `json:"enabled"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	RuleIndex int             `json:"ruleIndex"`
	Level     string          `json:"level"`
	Message   sarifText       `json:"message"`
	Locations []sarifLocation `json:"locations"`
	Fixes     []sarifFix      `json:"fixes,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifactLoc `json:"artifactLocation"`
	Region           sarifRegion      `json:"region"`
}

type sarifArtifactLoc struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

type sarifFix struct {
	Description     sarifText             `json:"description"`
	ArtifactChanges []sarifArtifactChange `json:"artifactChanges"`
}

type sarifArtifactChange struct {
	ArtifactLocation sarifArtifactLoc   `json:"artifactLocation"`
	Replacements     []sarifReplacement `json:"replacements"`
}

type sarifReplacement struct {
	DeletedRegion   sarifRegion `json:"deletedRegion"`
	InsertedContent sarifText   `json:"insertedContent"`
}

func sarifLevel(s lint.Severity) string {
	switch s {
	case lint.Error:
		return "error"
	case lint.Warning:
		return "warning"
	case lint.Off:
		return "none"
	}
	return "note"
}

// sarifRegionOf converts an LSP range; columns stay UTF-16 code units, as
// the run's columnKind declares.
func sarifRegionOf(r protocol.Range) sarifRegion {
	return sarifRegion{StartLine: r.Start.Line + 1, StartColumn: r.Start.Character + 1, EndLine: r.End.Line + 1, EndColumn: r.End.Character + 1}
}

func writeSARIF(w io.Writer, items []item, skipped []skippedFile, ok bool) error {
	driver := sarifDriver{Name: "falcon-asm", Version: version, InformationURI: "https://git.airies.net/vifair22/falcon-asm", Rules: []sarifRule{}}
	index := map[string]int{}
	for _, r := range lint.Rules() {
		index[r.Code] = len(driver.Rules)
		help := r.Text()
		driver.Rules = append(driver.Rules, sarifRule{
			ID:                   r.Code,
			ShortDescription:     sarifText{Text: r.Doc.Summary},
			FullDescription:      &sarifText{Text: r.Doc.Why},
			Help:                 &sarifText{Text: help},
			HelpURI:              lint.DocURL(r.Code),
			DefaultConfiguration: sarifRuleConf{Level: sarifLevel(r.Default), Enabled: r.Default != lint.Off},
		})
	}
	ruleIndex := func(code string) int {
		if i, ok := index[code]; ok {
			return i
		}
		// gas diagnostics: one rule for the assembler's own errors.
		index[code] = len(driver.Rules)
		driver.Rules = append(driver.Rules, sarifRule{
			ID:                   code,
			ShortDescription:     sarifText{Text: "Diagnostic reported by the GNU assembler (" + toolchain.Source + ")."},
			DefaultConfiguration: sarifRuleConf{Level: "error", Enabled: true},
		})
		return index[code]
	}
	wd, _ := filepath.Abs(".")
	results := []sarifResult{}
	for _, it := range items {
		loc := artifact(it.file, wd)
		res := sarifResult{
			RuleID: it.Code, RuleIndex: ruleIndex(it.Code), Level: sarifLevel(it.Severity),
			Message:   sarifText{Text: it.Message},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysical{ArtifactLocation: loc, Region: sarifRegionOf(it.Range)}}},
		}
		for _, f := range it.Fixes {
			ch := sarifArtifactChange{ArtifactLocation: loc}
			for _, e := range f.Edits {
				ch.Replacements = append(ch.Replacements, sarifReplacement{DeletedRegion: sarifRegionOf(e.Range), InsertedContent: sarifText{Text: e.NewText}})
			}
			res.Fixes = append(res.Fixes, sarifFix{Description: sarifText{Text: f.Title}, ArtifactChanges: []sarifArtifactChange{ch}})
		}
		results = append(results, res)
	}
	inv := sarifInvocation{ExecutionSuccessful: ok}
	for _, sk := range skipped {
		var loc sarifArtifactLocation
		loc.PhysicalLocation.ArtifactLocation = artifact(sk.file, wd)
		inv.ToolExecutionNotifications = append(inv.ToolExecutionNotifications, sarifNotification{
			Descriptor: sarifDescriptorRef{ID: sarifSkippedID},
			Level:      "note",
			Message: sarifText{Text: fmt.Sprintf("%d listing rule%s skipped (no assembler listing: %s): %s",
				len(sk.rules), plural(len(sk.rules), "", "s"), skipKeys[sk.reason], strings.Join(sk.rules, ", "))},
			Locations:  []sarifArtifactLocation{loc},
			Properties: map[string]any{"reason": skipKeys[sk.reason], "rules": sk.rules},
		})
	}
	log := sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool:               sarifTool{Driver: driver},
			OriginalURIBaseIDs: map[string]sarifArtifactLoc{"SRCROOT": {URI: toolchain.PathToURI(wd) + "/"}},
			ColumnKind:         "utf16CodeUnits",
			Results:            results,
			Invocations:        []sarifInvocation{inv},
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

// artifact names a file relative to SRCROOT (the working directory) when it
// lies below it, so a code-scanning upload maps results onto the repo.
func artifact(f *lintFile, wd string) sarifArtifactLoc {
	if rel, err := filepath.Rel(wd, f.abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return sarifArtifactLoc{URI: filepath.ToSlash(rel), URIBaseID: "SRCROOT"}
	}
	return sarifArtifactLoc{URI: toolchain.PathToURI(f.abs)}
}
