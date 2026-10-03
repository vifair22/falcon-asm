package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// JSON-RPC codes the burst scenario counts rather than fails on.
const (
	codeMethodNotFound   = -32601
	codeRequestCancelled = -32800
	codeContentModified  = -32801
)

// config is everything the scenarios need from the command line.
type config struct {
	bin        string
	serverArgs []string // "{scenario}" is replaced by the scenario name
	fixtures   string
	root       string
	sizes      []int
	edits      int
	wsFiles    int
	coldRuns   int
	rounds     int
	timeout    time.Duration
	settle     time.Duration // quiet period that ends the tree scenario's open phase
	diagEdits  int           // edits per file or phase in the diagnostics scenario
	debounce   time.Duration // the server's publish debounce
	log        io.Writer
}

type bench struct {
	cfg config
	fx  []fixture
	rec *recorder
	tmp string
}

// start spawns a server for one scenario.
func (b *bench) start(scenario string) (*client, error) {
	args := make([]string, len(b.cfg.serverArgs))
	for i, a := range b.cfg.serverArgs {
		args[i] = strings.ReplaceAll(a, "{scenario}", scenario)
	}
	return spawn(b.cfg.bin, args, b.cfg.timeout)
}

// timed sends one request and records its latency. A JSON-RPC error reply
// fails the scenario: every request here is one the server should answer.
func (b *bench) timed(c *client, metric, method string, params any) (response, error) {
	r, d, err := c.call(method, params)
	if err != nil {
		return r, err
	}
	if r.err != nil {
		return r, fmt.Errorf("%s: error %d: %s", method, r.err.Code, r.err.Message)
	}
	b.rec.add(metric, d, r.size)
	return r, nil
}

// finish records peak RSS and ends the session.
func (b *bench) finish(c *client, scenario string) error {
	rss, err := c.peakRSS()
	if err == nil {
		b.rec.value(scenario+"/peak-rss", "kB", float64(rss))
	}
	return errors.Join(err, c.close())
}

func openParams(uri, text string) map[string]any {
	return map[string]any{"textDocument": map[string]any{
		"uri": uri, "languageId": "falcon-asm", "version": 1, "text": text,
	}}
}

func docParams(uri string) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": uri}}
}

func posParams(uri string, p protocol.Position) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}
}

// tokenPos converts a lexer token position (byte column) to an LSP position.
func tokenPos(doc *docstore.Document, starts []int, t lexer.Token, extra int) protocol.Position {
	return doc.Position(starts[t.Line] + t.Col + extra)
}

// lexLineStarts mirrors the lexer's line numbering ('\n' only).
func lexLineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// fixturesScenario exercises every request kind on the falcon-bios
// fixtures, the size of file the server was written for.
func (b *bench) fixturesScenario() error {
	const sc = "fixtures"
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
	uris := make([]string, len(b.fx))
	for i, f := range b.fx {
		uris[i] = toolchain.PathToURI(filepath.Join(dir, f.name))
		if err := c.notify("textDocument/didOpen", openParams(uris[i], f.text)); err != nil {
			return errors.Join(err, c.close())
		}
	}
	if err := b.fixtureQueries(c, uris); err != nil {
		return errors.Join(err, c.close())
	}
	return b.finish(c, sc)
}

func (b *bench) fixtureQueries(c *client, uris []string) error {
	const sc = "fixtures/"
	// The first request that needs the instruction tables may wait for
	// their load; cold start owns that number, so it is taken here
	// unrecorded, by a request that is sure to need them.
	if _, _, err := c.call("textDocument/semanticTokens/full", docParams(uris[0])); err != nil {
		return err
	}
	for range b.cfg.rounds {
		for _, u := range uris {
			if _, err := b.timed(c, sc+"semanticTokens/full", "textDocument/semanticTokens/full", docParams(u)); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"pmode.S", "stage2.S"} {
		i := sort.Search(len(b.fx), func(i int) bool { return b.fx[i].name >= name })
		if i == len(b.fx) || b.fx[i].name != name {
			continue
		}
		if err := b.fileQueries(c, uris[i], b.fx[i].text, name == "pmode.S"); err != nil {
			return err
		}
	}
	full := protocol.Range{End: protocol.Position{Line: 1 << 20}}
	for range b.cfg.rounds {
		for _, u := range uris {
			if _, err := b.timed(c, sc+"documentSymbol", "textDocument/documentSymbol", docParams(u)); err != nil {
				return err
			}
			if _, err := b.timed(c, sc+"foldingRange", "textDocument/foldingRange", docParams(u)); err != nil {
				return err
			}
			p := docParams(u)
			p["range"] = full
			if _, err := b.timed(c, sc+"inlayHint", "textDocument/inlayHint", p); err != nil {
				return err
			}
		}
	}
	return nil
}

// maxCompletions bounds the completion positions per file; enough for a
// stable p99 without dominating the scenario's run time.
const maxCompletions = 300

// fileQueries runs the position requests over one fixture's tokens: hover
// on every mnemonic and register (pmode.S only), completion inside them,
// definition on label references and references on label definitions.
func (b *bench) fileQueries(c *client, uri, text string, hovers bool) error {
	const sc = "fixtures/"
	doc := &docstore.Document{URI: uri, Text: text}
	starts := lexLineStarts(text)
	completions := 0
	for _, t := range lexer.Tokenize(text) {
		var err error
		switch t.Kind {
		case lexer.Mnemonic, lexer.Register:
			if hovers {
				_, err = b.timed(c, sc+"hover", "textDocument/hover", posParams(uri, tokenPos(doc, starts, t, 1)))
			}
			// One byte typed: "m" of a mnemonic, "%e" of a register.
			extra := 1
			if t.Kind == lexer.Register {
				extra = 2
			}
			if err == nil && completions < maxCompletions && t.Len >= extra {
				completions++
				_, err = b.timed(c, sc+"completion", "textDocument/completion", posParams(uri, tokenPos(doc, starts, t, extra)))
			}
		case lexer.LabelRef:
			_, err = b.timed(c, sc+"definition", "textDocument/definition", posParams(uri, tokenPos(doc, starts, t, 0)))
		case lexer.LabelDef:
			p := posParams(uri, tokenPos(doc, starts, t, 0))
			p["context"] = map[string]any{"includeDeclaration": true}
			_, err = b.timed(c, sc+"references", "textDocument/references", p)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func changeParams(uri string, version int, at protocol.Position, text string) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{
			"range": protocol.Range{Start: at, End: at},
			"text":  text,
		}},
	}
}

// typist tracks where the next typed byte goes.
type typist struct {
	uri     string
	version int
	at      protocol.Position
	n       int
}

// next returns the didChange for the next byte and advances the cursor.
func (t *typist) next() (map[string]any, byte) {
	ch := typedText[t.n%len(typedText)]
	t.n++
	t.version++
	p := changeParams(t.uri, t.version, t.at, string(ch))
	if ch == '\n' {
		t.at = protocol.Position{Line: t.at.Line + 1}
	} else {
		t.at.Character++
	}
	return p, ch
}

// completes reports whether an editor would ask for completion after ch:
// a word character (quick suggestions) or one of the server's triggers.
func completes(ch byte) bool {
	return isIdentChar(ch) || ch == '%' || ch == '$'
}

// typingScenario types into the middle of a large file the way an editor
// drives a server: every keystroke is a didChange, most are followed by a
// completion request, and highlighting, outline and folding are refreshed
// periodically. Then the same edits are sent as an unpaced burst with a
// semantic tokens request after each, every superseded one cancelled.
func (b *bench) typingScenario(lines int) error {
	sc := fmt.Sprintf("typing-%dk", lines/1000)
	text := genLarge(b.fx, lines)
	uri := toolchain.PathToURI(filepath.Join(b.tmp, sc+".S"))
	c, err := b.start(sc)
	if err != nil {
		return err
	}
	if _, err := c.initialize("", map[string]any{"toolchain": false}); err != nil {
		return errors.Join(err, c.close())
	}
	if err := b.typing(c, sc, uri, text); err != nil {
		return errors.Join(err, c.close())
	}
	return b.finish(c, sc)
}

func (b *bench) typing(c *client, sc, uri, text string) error {
	t0 := time.Now()
	if err := c.notify("textDocument/didOpen", openParams(uri, text)); err != nil {
		return err
	}
	r, _, err := c.call("textDocument/semanticTokens/full", docParams(uri))
	if err != nil {
		return err
	}
	b.rec.add(sc+"/open+semanticTokens", r.at.Sub(t0), r.size)

	resultID := resultIDOf(r.result)
	ty := &typist{uri: uri, version: 1, at: protocol.Position{Line: strings.Count(text, "\n") / 2}}
	for k := range b.cfg.edits {
		p, ch := ty.next()
		if err := c.notify("textDocument/didChange", p); err != nil {
			return err
		}
		if completes(ch) {
			if _, err := b.timed(c, sc+"/completion", "textDocument/completion", posParams(uri, ty.at)); err != nil {
				return err
			}
		}
		if k%10 == 9 {
			r, err := b.timed(c, sc+"/semanticTokens/full", "textDocument/semanticTokens/full", docParams(uri))
			if err != nil {
				return err
			}
			resultID = resultIDOf(r.result)
		}
		// Halfway between full requests, what VS Code sends when the
		// server offers deltas: the edits since the last result.
		if k%10 == 4 && c.delta && resultID != "" {
			p := docParams(uri)
			p["previousResultId"] = resultID
			r, err := b.timed(c, sc+"/semanticTokens/full/delta", "textDocument/semanticTokens/full/delta", p)
			if err != nil {
				return err
			}
			resultID = resultIDOf(r.result)
		}
		if k%25 == 24 {
			if _, err := b.timed(c, sc+"/documentSymbol", "textDocument/documentSymbol", docParams(uri)); err != nil {
				return err
			}
			if _, err := b.timed(c, sc+"/foldingRange", "textDocument/foldingRange", docParams(uri)); err != nil {
				return err
			}
		}
	}
	return b.burst(c, sc, ty)
}

// resultIDOf extracts a semantic tokens result's id; "" when it has none.
func resultIDOf(raw json.RawMessage) string {
	var r struct {
		ResultID string `json:"resultId"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.ResultID
}

// burst sends edits without waiting, each followed by a semantic tokens
// request, cancelling the previous request as VS Code does when the
// document changes under it. drain is the time from the first edit to the
// last reply: how long the editor waits for highlighting to catch up.
func (b *bench) burst(c *client, sc string, ty *typist) error {
	var reqs []*pendingReq
	start := time.Now()
	for range b.cfg.edits {
		p, _ := ty.next()
		if err := c.notify("textDocument/didChange", p); err != nil {
			return err
		}
		if n := len(reqs); n > 0 {
			if err := c.notify("$/cancelRequest", map[string]any{"id": reqs[n-1].id}); err != nil {
				return err
			}
		}
		r, err := c.send("textDocument/semanticTokens/full", docParams(ty.uri))
		if err != nil {
			return err
		}
		reqs = append(reqs, r)
	}
	cancelled, answered := 0, 0
	var last response
	var lastD time.Duration
	for _, p := range reqs {
		r, d, err := c.wait(p)
		if err != nil {
			return fmt.Errorf("burst: %w", err)
		}
		switch {
		case r.err == nil:
			answered++
		case r.err.Code == codeRequestCancelled || r.err.Code == codeContentModified:
			cancelled++
		default:
			return fmt.Errorf("burst: error %d: %s", r.err.Code, r.err.Message)
		}
		last, lastD = r, d
	}
	if last.err != nil {
		return errors.New("burst: the newest semantic tokens request was not answered")
	}
	b.rec.add(sc+"/burst-drain", last.at.Sub(start), 0)
	b.rec.add(sc+"/burst-last-semanticTokens", lastD, last.size)
	b.rec.value(sc+"/burst-answered", "requests", float64(answered))
	b.rec.value(sc+"/burst-cancelled", "requests", float64(cancelled))
	return nil
}

// workspaceScenario measures the first cross-file query, which walks and
// parses the whole workspace, and the queries after it.
func (b *bench) workspaceScenario() error {
	const sc = "workspace"
	dir := filepath.Join(b.tmp, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("workspace dir: %w", err)
	}
	if err := genWorkspace(dir, b.fx, b.cfg.wsFiles); err != nil {
		return err
	}
	c, err := b.start(sc)
	if err != nil {
		return err
	}
	if _, err := c.initialize(toolchain.PathToURI(dir), map[string]any{"toolchain": false}); err != nil {
		return errors.Join(err, c.close())
	}
	if err := b.workspace(c, dir); err != nil {
		return errors.Join(err, c.close())
	}
	return b.finish(c, sc)
}

func (b *bench) workspace(c *client, dir string) error {
	const sc = "workspace/"
	var uris [2]string
	for i := range uris {
		p := filepath.Join(dir, wsFileName(i))
		raw, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read workspace file: %w", err)
		}
		uris[i] = toolchain.PathToURI(p)
		if err := c.notify("textDocument/didOpen", openParams(uris[i], string(raw))); err != nil {
			return err
		}
	}
	refs := posParams(uris[0], protocol.Position{Line: globalDefLine, Character: 2})
	refs["context"] = map[string]any{"includeDeclaration": true}
	r, err := b.timed(c, sc+"references-first", "textDocument/references", refs)
	if err != nil {
		return err
	}
	var locs []json.RawMessage
	if err := json.Unmarshal(r.result, &locs); err != nil {
		return fmt.Errorf("references reply: %w", err)
	}
	// The definition and its .globl line, plus one call per other file;
	// anything else means the scan did not see the whole workspace and the
	// timing is meaningless.
	if len(locs) != b.cfg.wsFiles+1 {
		return fmt.Errorf("references found %d locations, want %d", len(locs), b.cfg.wsFiles+1)
	}
	call := protocol.Position{Line: globalCallLine, Character: globalCallCol + 2}
	for range 20 * b.cfg.rounds {
		if _, err := b.timed(c, sc+"references", "textDocument/references", refs); err != nil {
			return err
		}
		if _, err := b.timed(c, sc+"definition", "textDocument/definition", posParams(uris[1], call)); err != nil {
			return err
		}
		if _, err := b.timed(c, sc+"completion", "textDocument/completion", posParams(uris[1], call)); err != nil {
			return err
		}
	}
	// Diagnostics after an edit in a large workspace: the symbol rules
	// consult the whole workspace (globals, includes, C references).
	raw, err := os.ReadFile(filepath.Join(dir, wsFileName(1)))
	if err != nil {
		return fmt.Errorf("read workspace file: %w", err)
	}
	if _, err := c.waitVersion(uris[1], 1, b.cfg.timeout); err != nil {
		return err
	}
	line, _ := codeLine(string(raw))
	return b.editToPublish(c, "workspace", newToggler(uris[1], string(raw), 1, line), b.cfg.diagEdits)
}

// coldScenario measures process start to the initialize reply, and to the
// first semantic tokens of a large file, which includes the instruction
// table load and the first tokenization.
func (b *bench) coldScenario() error {
	const sc = "cold"
	text := genLarge(b.fx, 20000)
	uri := toolchain.PathToURI(filepath.Join(b.tmp, "cold.S"))
	peak := 0
	for range b.cfg.coldRuns {
		c, err := b.start(sc)
		if err != nil {
			return err
		}
		d, err := c.initialize("", map[string]any{"toolchain": false})
		if err != nil {
			return errors.Join(err, c.close())
		}
		b.rec.add(sc+"/spawn->initialize", d, 0)
		if err := c.notify("textDocument/didOpen", openParams(uri, text)); err != nil {
			return errors.Join(err, c.close())
		}
		r, _, err := c.call("textDocument/semanticTokens/full", docParams(uri))
		if err != nil {
			return errors.Join(err, c.close())
		}
		b.rec.add(sc+"/spawn->semanticTokens-20k", r.at.Sub(c.spawned), r.size)
		if rss, err := c.peakRSS(); err == nil {
			peak = max(peak, rss)
		}
		if err := c.close(); err != nil {
			return err
		}
	}
	b.rec.value(sc+"/peak-rss", "kB", float64(peak))
	return nil
}

// treeFiles lists the assembly sources the root's compile database names:
// those are the files the toolchain will actually run for.
func treeFiles(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, toolchain.DBFile))
	if err != nil {
		return nil, fmt.Errorf("tree scenario: %w", err)
	}
	var entries []struct {
		Directory string `json:"directory"`
		File      string `json:"file"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("tree scenario: decode %s: %w", toolchain.DBFile, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		ext := filepath.Ext(e.File)
		if ext != ".S" && ext != ".s" {
			continue
		}
		p := e.File
		if !filepath.IsAbs(p) {
			p = filepath.Join(e.Directory, p)
		}
		if _, err := os.Stat(p); err != nil || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("tree scenario: no assembly sources in %s", toolchain.DBFile)
	}
	return out, nil
}

// treeScenario opens a real project's assembly sources with the toolchain
// on and measures save to the toolchain's diagnostics publish. The tree is
// only read; didSave makes the server re-read the file, not write it.
func (b *bench) treeScenario() error {
	const sc = "tree"
	root, err := filepath.Abs(b.cfg.root)
	if err != nil {
		return fmt.Errorf("tree root: %w", err)
	}
	files, err := treeFiles(root)
	if err != nil {
		return err
	}
	c, err := b.start(sc)
	if err != nil {
		return err
	}
	if _, err := c.initialize(toolchain.PathToURI(root), map[string]any{"toolchain": true}); err != nil {
		return errors.Join(err, c.close())
	}
	if err := b.tree(c, files); err != nil {
		return errors.Join(err, c.close())
	}
	return b.finish(c, sc)
}

func (b *bench) tree(c *client, files []string) error {
	uris := make([]string, len(files))
	t0 := time.Now()
	for i, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		uris[i] = toolchain.PathToURI(p)
		if err := c.notify("textDocument/didOpen", openParams(uris[i], string(raw))); err != nil {
			return err
		}
	}
	// Opening starts a toolchain run per file; saves are only meaningful
	// once those have landed and the server is quiet.
	deadline := time.Now().Add(b.cfg.timeout)
	for time.Now().Before(deadline) {
		last := c.lastPublish()
		if !last.IsZero() && time.Since(last) >= b.cfg.settle {
			break
		}
		time.Sleep(b.cfg.settle / 10)
	}
	b.rec.add("tree/open-all->settled", c.lastPublish().Sub(t0), 0)
	timeouts := 0
	for range b.cfg.rounds {
		for _, u := range uris {
			start := time.Now()
			if err := c.notify("textDocument/didSave", docParams(u)); err != nil {
				return err
			}
			p, err := c.waitPublish(u, start, b.cfg.timeout)
			if errors.Is(err, errTimeout) {
				timeouts++
				continue
			}
			if err != nil {
				return err
			}
			b.rec.add("tree/didSave->diagnostics", p.at.Sub(start), 0)
			hp := docParams(u)
			hp["range"] = protocol.Range{End: protocol.Position{Line: 1 << 20}}
			if _, err := b.timed(c, "tree/inlayHint", "textDocument/inlayHint", hp); err != nil {
				return err
			}
			// Size budget lenses exist while the listing describes the
			// buffer, which a save just made true. A server without
			// lenses (MethodNotFound) is compared on the rest.
			r, d, err := c.call("textDocument/codeLens", docParams(u))
			if err != nil {
				return err
			}
			switch {
			case r.err == nil:
				b.rec.add("tree/codeLens", d, r.size)
			case r.err.Code != codeMethodNotFound:
				return fmt.Errorf("textDocument/codeLens: error %d: %s", r.err.Code, r.err.Message)
			}
		}
	}
	b.rec.value("tree/files", "files", float64(len(files)))
	b.rec.value("tree/save-timeouts", "saves", float64(timeouts))
	return nil
}
