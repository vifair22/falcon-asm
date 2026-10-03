package engine

import (
	"errors"
	"os"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// inactiveMessage labels the hint diagnostics that fade #if 0 bodies; the
// Unnecessary tag does the fading, the text is what a problems list shows.
const inactiveMessage = "inactive preprocessor region"

// schedulePublishLocked (re)starts st's debounce timer. Every keystroke
// resets it, so a burst of changes publishes once, for the newest version.
func (e *Engine) schedulePublishLocked(st *docState) {
	if e.closed {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	uri := st.uri
	st.timer = time.AfterFunc(e.cfg.Debounce, func() { e.publish(uri) })
}

// publish sends the complete diagnostic set for uri: falcon-asm's own
// analysis when the document is open, the toolchain's findings for it, and
// gas diagnostics other open files' runs attributed to it (an error inside
// an included header).
//
// The analysis is computed before any lock is taken. The send happens under
// pubMu after re-checking that the snapshot is still current, so a slow
// publish for an old version can never overwrite a newer one: whoever made
// the state newer also scheduled a publish that will run after this one.
func (e *Engine) publish(uri string) {
	e.mu.Lock()
	st := e.docs[uri]
	var snap *snapshot
	if st != nil {
		snap = e.snapshotLocked(st)
	}
	e.mu.Unlock()
	var own []protocol.Diagnostic
	if snap != nil {
		own = snap.diagnostics()
	}

	e.pubMu.Lock()
	defer e.pubMu.Unlock()
	e.mu.Lock()
	if e.closed || e.publishFn == nil || e.docs[uri] != st || st != nil && e.snapshotLocked(st) != snap {
		e.mu.Unlock()
		return
	}
	params := protocol.PublishDiagnosticsParams{URI: uri, Diagnostics: append([]protocol.Diagnostic{}, own...)}
	if st != nil {
		v := st.doc.Version
		params.Version = &v
		if st.asm != nil {
			params.Diagnostics = append(params.Diagnostics, st.asm.Diagnostics[st.path]...)
			for _, r := range st.asm.Inactive {
				params.Diagnostics = append(params.Diagnostics, protocol.Diagnostic{
					Range:    r,
					Severity: protocol.SeverityHint,
					Source:   "falcon-asm",
					Message:  inactiveMessage,
					Tags:     []protocol.DiagnosticTag{protocol.TagUnnecessary},
				})
			}
		}
	}
	owners := e.ext[uri]
	for _, owner := range sortedKeys(owners) {
		params.Diagnostics = append(params.Diagnostics, owners[owner]...)
	}
	fn := e.publishFn
	e.mu.Unlock()
	fn(params)
}

// setExtLocked replaces the gas diagnostics owner attributes to other
// files, returning every target URI whose set changed (old and new), which
// the caller republishes.
func (e *Engine) setExtLocked(owner string, byURI map[string][]protocol.Diagnostic) []string {
	changed := map[string]bool{}
	for target, owners := range e.ext {
		if _, ok := owners[owner]; ok {
			delete(owners, owner)
			if len(owners) == 0 {
				delete(e.ext, target)
			}
			changed[target] = true
		}
	}
	for target, diags := range byURI {
		if e.ext[target] == nil {
			e.ext[target] = map[string][]protocol.Diagnostic{}
		}
		e.ext[target][owner] = diags
		changed[target] = true
	}
	return sortedKeys(changed)
}

// startRunLocked launches a toolchain run for st when it can have one: an
// assembly file on disk with compile flags, toolchain enabled, engine live.
// The run is asynchronous; results publish when it finishes.
func (e *Engine) startRunLocked(st *docState) {
	if e.closed || !e.toolchainOn || st.lang != langAsm || st.path == "" || !st.hasFlags {
		return
	}
	st.runSeq++
	r := run{st: st, seq: st.runSeq, path: st.path, flags: st.flags, version: st.doc.Version, text: st.doc.Text}
	e.wg.Add(1)
	go e.execute(r)
}

type run struct {
	st      *docState
	seq     int
	path    string
	flags   toolchain.Flags
	version int
	text    string
}

func (e *Engine) execute(r run) {
	defer e.wg.Done()
	macros, err := e.tc.Macros(e.ctx, r.path, r.flags)
	if err != nil {
		if e.quiet(err, r.path) {
			return
		}
		macros = nil
	}
	res, err := e.tc.Assemble(e.ctx, r.path, r.flags)
	if err != nil {
		if e.quiet(err, r.path) {
			return
		}
		res = nil
	}
	// The listing describes the file on disk. It matches the editor only
	// when the buffer was not dirty at save (or at open, where a client
	// may restore unsaved text).
	asmVersion := -1
	if disk, err := e.cfg.ReadFile(r.path); err == nil && string(disk) == r.text {
		asmVersion = r.version
	}

	e.mu.Lock()
	st := r.st
	if e.closed || e.docs[st.uri] != st || st.runSeq != r.seq {
		e.mu.Unlock()
		return
	}
	if macros != nil {
		st.macros = macros
		st.gen++
	}
	var targets []string
	if res != nil {
		st.asm, st.asmVersion = res, asmVersion
		ext := map[string][]protocol.Diagnostic{}
		for path, diags := range res.Diagnostics {
			if path != st.path {
				ext[toolchain.PathToURI(path)] = diags
			}
		}
		targets = e.setExtLocked(st.uri, ext)
	}
	e.mu.Unlock()

	e.publish(st.uri)
	for _, t := range targets {
		if t != st.uri {
			e.publish(t)
		}
	}
}

// quiet decides what a toolchain error means. A superseded or cancelled run
// is dropped silently (true), and so is a missing compiler after one log
// line. A file that is not an assembly source keeps what the other step
// produced without a log line (false).
// Anything else is logged once per message and the run carries on with what
// it has (false), since one failing step does not spoil the other.
func (e *Engine) quiet(err error, path string) bool {
	switch {
	case errors.Is(err, toolchain.ErrSuperseded), e.ctx.Err() != nil:
		return true
	case errors.Is(err, toolchain.ErrNotAssembly):
		// A header or .inc borrows flags from the database, which is how
		// it gets macros; it cannot be assembled on its own, and that is
		// not news worth a log line.
		return false
	case errors.Is(err, toolchain.ErrNoToolchain):
		e.logOnce("tc:"+err.Error(), "toolchain unavailable (%v); assembler diagnostics and listing hints are off", err)
		return true
	}
	e.logOnce("err:"+err.Error(), "toolchain run for %s: %v", path, err)
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
