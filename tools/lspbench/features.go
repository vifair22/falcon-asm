package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Scenarios added with the linter: the paths a user meets once rules,
// hardware hovers, register hints, code lenses, code actions, workspace
// symbols and the call hierarchy exist, and the lint CLI.

// hoverFiles are the fixtures whose every operand and mnemonic is hovered
// in the features scenario: real, protected and long mode code, with
// GDTs, BIOS calls, port I/O and MSR writes between them.
var hoverFiles = []string{"boot.S", "pmode.S", "stage2.S"}

// hoverLineBudget is the hover length the design asks for (about six
// lines); a hover with hardware text appended may go past it, but one past
// twice that is reported, because it no longer fits a hover window.
const hoverLineBudget = 12

// toggler alternates between appending a blank to one line and removing
// it, so every edit makes a new version while the text stays the fixture.
type toggler struct {
	uri     string
	version int
	line    int
	end     int // UTF-16 length of the line without the blank
	on      bool
}

func newToggler(uri, text string, version, line int) *toggler {
	doc := &docstore.Document{URI: uri, Text: text}
	l, _ := doc.Line(line)
	return &toggler{uri: uri, version: version, line: line, end: docstore.UTF16Len(l)}
}

func (t *toggler) next() map[string]any {
	t.version++
	at := protocol.Position{Line: t.line, Character: t.end}
	p := changeParams(t.uri, t.version, at, " ")
	if t.on {
		p["contentChanges"] = []map[string]any{{
			"range": protocol.Range{Start: at, End: protocol.Position{Line: t.line, Character: t.end + 1}},
			"text":  "",
		}}
	}
	t.on = !t.on
	return p
}

// codeLine is the line of the first instruction at or after the middle of
// text: an edit there is an edit to code, as typing would be.
func codeLine(text string) (int, protocol.Position) {
	toks := lexer.Tokenize(text)
	mid := strings.Count(text, "\n") / 2
	doc := &docstore.Document{Text: text}
	starts := lexLineStarts(text)
	for _, t := range toks {
		if t.Line >= mid && t.Kind == lexer.Register {
			return t.Line, tokenPos(doc, starts, t, 1)
		}
	}
	for _, t := range toks {
		if t.Kind == lexer.Register {
			return t.Line, tokenPos(doc, starts, t, 1)
		}
	}
	return mid, protocol.Position{Line: mid}
}

// diagnosticsScenario measures what the user waits for after an edit: the
// diagnostics for that version, which include every lint rule. The server
// debounces publishes; the time minus the debounce is the server's work.
// It also checks the keystroke path stays free of lint work: a completion
// sent while the debounced lint is running, and a code action and a hover
// sent right after an edit, before any lint of that version exists.
func (b *bench) diagnosticsScenario() error {
	if err := b.diagnosticsFixtures(); err != nil {
		return err
	}
	for _, n := range b.cfg.sizes {
		if err := b.diagnosticsLarge(n); err != nil {
			return err
		}
	}
	return nil
}

func (b *bench) diagnosticsFixtures() error {
	const sc = "diagnostics"
	dir, err := filepath.Abs(b.cfg.fixtures)
	if err != nil {
		return fmt.Errorf("fixtures dir: %w", err)
	}
	c, err := b.start(sc)
	if err != nil {
		return err
	}
	if _, err := c.initialize(toolchain.PathToURI(dir), map[string]any{"toolchain": false}); err != nil {
		return errors.Join(err, c.close())
	}
	for _, f := range b.fx {
		uri := toolchain.PathToURI(filepath.Join(dir, f.name))
		if err := c.notify("textDocument/didOpen", openParams(uri, f.text)); err != nil {
			return errors.Join(err, c.close())
		}
		if _, err := c.waitVersion(uri, 1, b.cfg.timeout); err != nil {
			return errors.Join(err, c.close())
		}
		line, _ := codeLine(f.text)
		if err := b.editToPublish(c, sc, newToggler(uri, f.text, 1, line), b.cfg.diagEdits); err != nil {
			return errors.Join(err, c.close())
		}
	}
	return b.finish(c, sc)
}

// editToPublish sends n single edits, each waited out until its
// diagnostics arrive.
func (b *bench) editToPublish(c *client, sc string, tg *toggler, n int) error {
	for range n {
		start := time.Now()
		if err := c.notify("textDocument/didChange", tg.next()); err != nil {
			return err
		}
		p, err := c.waitVersion(tg.uri, tg.version, b.cfg.timeout)
		if err != nil {
			return err
		}
		d := p.at.Sub(start)
		b.rec.add(sc+"/edit->publish", d, 0)
		b.rec.add(sc+"/edit->publish-minus-debounce", max(d-b.cfg.debounce, 0), 0)
	}
	return nil
}

func (b *bench) diagnosticsLarge(lines int) error {
	sc := fmt.Sprintf("diagnostics-%dk", lines/1000)
	text := genLarge(b.fx, lines)
	uri := toolchain.PathToURI(filepath.Join(b.tmp, sc+".S"))
	c, err := b.start(sc)
	if err != nil {
		return err
	}
	if _, err := c.initialize("", map[string]any{"toolchain": false}); err != nil {
		return errors.Join(err, c.close())
	}
	if err := b.diagnosticsLargeRun(c, sc, uri, text); err != nil {
		return errors.Join(err, c.close())
	}
	return b.finish(c, sc)
}

func (b *bench) diagnosticsLargeRun(c *client, sc, uri, text string) error {
	t0 := time.Now()
	if err := c.notify("textDocument/didOpen", openParams(uri, text)); err != nil {
		return err
	}
	p, err := c.waitVersion(uri, 1, b.cfg.timeout)
	if err != nil {
		return err
	}
	b.rec.add(sc+"/open->publish", p.at.Sub(t0), 0)
	line, reg := codeLine(text)
	tg := newToggler(uri, text, 1, line)
	if err := b.editToPublish(c, sc, tg, b.cfg.diagEdits); err != nil {
		return err
	}
	// A keystroke's requests while the debounced lint of the previous
	// version runs: they share the CPU with it, and must not wait for it.
	cursor := protocol.Position{Line: line, Character: tg.end}
	for range b.cfg.diagEdits {
		if err := c.notify("textDocument/didChange", tg.next()); err != nil {
			return err
		}
		time.Sleep(b.cfg.debounce + 2*time.Millisecond)
		if _, err := b.timed(c, sc+"/completion-during-lint", "textDocument/completion", posParams(uri, cursor)); err != nil {
			return err
		}
		if _, err := c.waitVersion(uri, tg.version, b.cfg.timeout); err != nil {
			return err
		}
	}
	// Right after an edit, before the debounce: the light bulb's code
	// action request VS Code sends after the cursor moved, and a hover,
	// when no lint of the version exists yet. Separate edits, so neither
	// finds what the other computed.
	for range b.cfg.diagEdits {
		if err := c.notify("textDocument/didChange", tg.next()); err != nil {
			return err
		}
		if _, err := b.timed(c, sc+"/codeAction-after-edit", "textDocument/codeAction", lightBulb(uri, cursor)); err != nil {
			return err
		}
		if _, err := c.waitVersion(uri, tg.version, b.cfg.timeout); err != nil {
			return err
		}
	}
	for range b.cfg.diagEdits {
		if err := c.notify("textDocument/didChange", tg.next()); err != nil {
			return err
		}
		if _, err := b.timed(c, sc+"/hover-after-edit", "textDocument/hover", posParams(uri, reg)); err != nil {
			return err
		}
		if _, err := c.waitVersion(uri, tg.version, b.cfg.timeout); err != nil {
			return err
		}
	}
	return nil
}

// codeActionAutomatic is LSP's CodeActionTriggerKind.Automatic: the
// editor asking on its own, as VS Code's light bulb does.
const codeActionAutomatic = 2

// lightBulb is the code action request VS Code sends when the cursor
// rests at pos on a line with no diagnostic.
func lightBulb(uri string, pos protocol.Position) map[string]any {
	ca := docParams(uri)
	ca["range"] = protocol.Range{Start: pos, End: pos}
	ca["context"] = map[string]any{"diagnostics": []any{}, "triggerKind": codeActionAutomatic}
	return ca
}

// featuresScenario covers the editor features that came with the linter
// on the fixtures, with register hints on: hover over every operand and
// mnemonic (hardware text included), code actions for every published
// finding, code lenses, inlay hints, workspace symbols and the call
// hierarchy.
func (b *bench) featuresScenario() error {
	const sc = "features"
	dir, err := filepath.Abs(b.cfg.fixtures)
	if err != nil {
		return fmt.Errorf("fixtures dir: %w", err)
	}
	c, err := b.start(sc)
	if err != nil {
		return err
	}
	if _, err := c.initialize(toolchain.PathToURI(dir), map[string]any{"toolchain": false, "registerHints": true}); err != nil {
		return errors.Join(err, c.close())
	}
	if err := b.features(c, dir); err != nil {
		return errors.Join(err, c.close())
	}
	return b.finish(c, sc)
}

func (b *bench) features(c *client, dir string) error {
	const sc = "features/"
	uris := make([]string, len(b.fx))
	for i, f := range b.fx {
		uris[i] = toolchain.PathToURI(filepath.Join(dir, f.name))
		if err := c.notify("textDocument/didOpen", openParams(uris[i], f.text)); err != nil {
			return err
		}
	}
	// Hovers and code actions come after the diagnostics, as they do for
	// a user who opens a file and then reads it.
	for _, u := range uris {
		if _, err := c.waitVersion(u, 1, b.cfg.timeout); err != nil {
			return err
		}
	}
	if err := b.hoverSweep(c, dir); err != nil {
		return err
	}
	full := protocol.Range{End: protocol.Position{Line: 1 << 20}}
	for range b.cfg.rounds {
		for i, u := range uris {
			if err := b.codeActions(c, u, b.fx[i].text); err != nil {
				return err
			}
			if _, err := b.timed(c, sc+"codeLens", "textDocument/codeLens", docParams(u)); err != nil {
				return err
			}
			p := docParams(u)
			p["range"] = full
			if _, err := b.timed(c, sc+"inlayHint-registers", "textDocument/inlayHint", p); err != nil {
				return err
			}
		}
	}
	for range b.cfg.rounds {
		for _, q := range []string{"", "a", "gdt", "start", "e820", "print", "zz_no_such_symbol"} {
			if _, err := b.timed(c, sc+"workspaceSymbol", "workspace/symbol", map[string]any{"query": q}); err != nil {
				return err
			}
		}
	}
	if err := b.callHierarchy(c, dir); err != nil {
		return err
	}
	// The first hover of a new version pays for the version's parse,
	// dataflow and hardware index unless the lint run built them first.
	for i, u := range uris {
		line, reg := codeLine(b.fx[i].text)
		tg := newToggler(u, b.fx[i].text, 1, line)
		for range 2 * b.cfg.rounds {
			if err := c.notify("textDocument/didChange", tg.next()); err != nil {
				return err
			}
			if _, err := b.timed(c, sc+"hover-after-edit", "textDocument/hover", posParams(u, reg)); err != nil {
				return err
			}
		}
		if _, err := c.waitVersion(u, tg.version, b.cfg.timeout); err != nil {
			return err
		}
	}
	return nil
}

// hoverSweep hovers every mnemonic, register, immediate, number and label
// operand of hoverFiles and records how long the hovers are.
func (b *bench) hoverSweep(c *client, dir string) error {
	longest, over, total := 0, 0, 0
	for _, name := range hoverFiles {
		i := sort.Search(len(b.fx), func(i int) bool { return b.fx[i].name >= name })
		if i == len(b.fx) || b.fx[i].name != name {
			continue
		}
		uri := toolchain.PathToURI(filepath.Join(dir, name))
		text := b.fx[i].text
		doc := &docstore.Document{URI: uri, Text: text}
		starts := lexLineStarts(text)
		for _, t := range lexer.Tokenize(text) {
			switch t.Kind {
			case lexer.Mnemonic, lexer.Register, lexer.Immediate, lexer.Number, lexer.LabelRef:
			default:
				continue
			}
			r, err := b.timed(c, "features/hover", "textDocument/hover", posParams(uri, tokenPos(doc, starts, t, min(1, t.Len-1))))
			if err != nil {
				return err
			}
			n := hoverLines(r.result)
			if n == 0 {
				continue
			}
			total++
			longest = max(longest, n)
			if n > hoverLineBudget {
				over++
			}
		}
	}
	b.rec.value("features/hovers-with-content", "hovers", float64(total))
	b.rec.value("features/hover-longest", "lines", float64(longest))
	b.rec.value(fmt.Sprintf("features/hovers-over-%d-lines", hoverLineBudget), "hovers", float64(over))
	return nil
}

// hoverLines counts the non-blank lines of a hover's markdown; 0 for none.
func hoverLines(raw json.RawMessage) int {
	var h struct {
		Contents struct {
			Value string `json:"value"`
		} `json:"contents"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(h.Contents.Value, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// codeActions asks for the actions of every finding published for uri,
// as the light bulb does, and for a cursor on every fifth line with no
// finding in the context, as cursor movement does.
func (b *bench) codeActions(c *client, uri, text string) error {
	p, ok := c.latestPublish(uri)
	if !ok {
		return fmt.Errorf("codeAction: nothing published for %s", uri)
	}
	for _, raw := range p.diags {
		var d struct {
			Range protocol.Range `json:"range"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return fmt.Errorf("codeAction: diagnostic: %w", err)
		}
		ca := docParams(uri)
		ca["range"] = d.Range
		ca["context"] = map[string]any{"diagnostics": []json.RawMessage{raw}, "triggerKind": codeActionAutomatic}
		if _, err := b.timed(c, "features/codeAction", "textDocument/codeAction", ca); err != nil {
			return err
		}
	}
	lines := strings.Count(text, "\n")
	for l := 0; l < lines; l += 5 {
		if _, err := b.timed(c, "features/codeAction-cursor", "textDocument/codeAction", lightBulb(uri, protocol.Position{Line: l})); err != nil {
			return err
		}
	}
	return nil
}

// callHierarchy prepares an item on every label definition of the hover
// files and asks for its incoming and outgoing calls.
func (b *bench) callHierarchy(c *client, dir string) error {
	for _, name := range hoverFiles {
		i := sort.Search(len(b.fx), func(i int) bool { return b.fx[i].name >= name })
		if i == len(b.fx) || b.fx[i].name != name {
			continue
		}
		uri := toolchain.PathToURI(filepath.Join(dir, name))
		text := b.fx[i].text
		doc := &docstore.Document{URI: uri, Text: text}
		starts := lexLineStarts(text)
		for _, t := range lexer.Tokenize(text) {
			if t.Kind != lexer.LabelDef {
				continue
			}
			r, err := b.timed(c, "features/prepareCallHierarchy", "textDocument/prepareCallHierarchy", posParams(uri, tokenPos(doc, starts, t, 0)))
			if err != nil {
				return err
			}
			var items []json.RawMessage
			_ = json.Unmarshal(r.result, &items)
			for _, it := range items {
				if _, err := b.timed(c, "features/incomingCalls", "callHierarchy/incomingCalls", map[string]any{"item": it}); err != nil {
					return err
				}
				if _, err := b.timed(c, "features/outgoingCalls", "callHierarchy/outgoingCalls", map[string]any{"item": it}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// cliScenario times `falcon-asm lint` as CI or a pre-commit hook runs it:
// wall time and peak RSS over the real tree (with and without the
// toolchain) when -root is given, and over a generated workspace.
func (b *bench) cliScenario() error {
	// The runs change directory, so a relative server path must be made
	// absolute first; a bare name is looked up on PATH as usual.
	if strings.ContainsRune(b.cfg.bin, filepath.Separator) {
		abs, err := filepath.Abs(b.cfg.bin)
		if err != nil {
			return fmt.Errorf("cli: %w", err)
		}
		b.cfg.bin = abs
	}
	if b.cfg.root != "" {
		root, err := filepath.Abs(b.cfg.root)
		if err != nil {
			return fmt.Errorf("cli root: %w", err)
		}
		// The toolchain is the CLI's default; the plain metric keeps
		// measuring the rules alone.
		if err := b.lintRuns("cli/lint-tree", root, "--no-toolchain"); err != nil {
			return err
		}
		if err := b.lintRuns("cli/lint-tree-toolchain", root, "--toolchain"); err != nil {
			return err
		}
	}
	dir := filepath.Join(b.tmp, "cli-ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cli workspace: %w", err)
	}
	if err := genWorkspace(dir, b.fx, b.cfg.wsFiles); err != nil {
		return err
	}
	return b.lintRuns(fmt.Sprintf("cli/lint-ws%d", b.cfg.wsFiles), dir)
}

// lintRuns runs the lint subcommand rounds times in dir (which it only
// reads) and records wall time and the largest peak RSS. Exit status 1
// (findings at or above the threshold) is a normal outcome here.
func (b *bench) lintRuns(metric, dir string, extra ...string) error {
	peak := int64(0)
	for range b.cfg.rounds {
		cmd := exec.Command(b.cfg.bin, append([]string{"lint", "--format", "json"}, extra...)...)
		cmd.Dir = dir
		var stderr strings.Builder
		cmd.Stderr = &stderr
		start := time.Now()
		out, err := cmd.Output()
		d := time.Since(start)
		var ee *exec.ExitError
		if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
			return fmt.Errorf("%s: %w; stderr:\n%s", metric, err, stderr.String())
		}
		b.rec.add(metric, d, len(out))
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			peak = max(peak, ru.Maxrss)
		}
	}
	b.rec.value(metric+"/peak-rss", "kB", float64(peak))
	return nil
}
