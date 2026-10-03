package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// On-type toolchain runs (roadmap item 79). Off by default: running cpp
// and gas while typing costs a process pair per pause, and save already
// gives the same answers. When on, a pause in typing assembles the unsaved
// buffer and its diagnostics and listing apply to the version typed.
//
// The buffer is written next to the source, under a hidden name, rather
// than into a temp dir: `#include "x.h"` resolves relative to the including
// file's directory, so a copy anywhere else would fail on the first quoted
// include. The name starts with a dot so shell and make wildcards (*.S)
// do not pick it up, and carries the pid and a sequence number so two
// servers, or two runs of one, never share a file. It is removed as soon as
// the run ends, whether it finished, failed or was cancelled.

// DefaultOnTypeDelay is the pause after the last keystroke before an
// on-type run starts: long enough that typing a word does not start one
// per character, short enough to feel live next to the 150 ms analysis.
const DefaultOnTypeDelay = 400 * time.Millisecond

// TempPrefix starts the name of every buffer copy an on-type run writes.
const TempPrefix = ".falcon-asm-"

// onTypeState is guarded by Engine.mu.
type onTypeState struct {
	on   bool
	runs map[string]*onTypeRun // by URI
}

type onTypeRun struct {
	timer  *time.Timer
	cancel context.CancelFunc // the run in flight, if any
}

var tempSeq atomic.Uint64

func (e *Engine) onTypeDelay() time.Duration {
	if e.cfg.OnTypeDelay > 0 {
		return e.cfg.OnTypeDelay
	}
	return DefaultOnTypeDelay
}

// onTypeChangedLocked (re)starts st's on-type debounce after a change.
func (e *Engine) onTypeChangedLocked(st *docState) {
	if !e.onType.on || e.closed || !e.toolchainOn || st.lang != langAsm || st.path == "" || !st.hasFlags {
		return
	}
	if e.onType.runs == nil {
		e.onType.runs = map[string]*onTypeRun{}
	}
	r := e.onType.runs[st.uri]
	if r == nil {
		r = &onTypeRun{}
		e.onType.runs[st.uri] = r
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	uri := st.uri
	r.timer = time.AfterFunc(e.onTypeDelay(), func() { e.startOnType(uri) })
}

// onTypeStopLocked drops uri's pending and running on-type work: a save
// starts a run from disk that supersedes it, and a closed document needs
// none.
func (e *Engine) onTypeStopLocked(uri string) {
	r := e.onType.runs[uri]
	if r == nil {
		return
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	if r.cancel != nil {
		r.cancel()
	}
	delete(e.onType.runs, uri)
}

func (e *Engine) onTypeStopAllLocked() {
	for uri := range e.onType.runs {
		e.onTypeStopLocked(uri)
	}
}

func (e *Engine) startOnType(uri string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, r := e.docs[uri], e.onType.runs[uri]
	if e.closed || st == nil || r == nil {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	ctx, cancel := context.WithCancel(e.ctx)
	r.cancel = cancel
	st.runSeq++
	rn := run{st: st, seq: st.runSeq, path: st.path, flags: st.flags, version: st.doc.Version, text: st.doc.Text}
	e.wg.Add(1)
	go e.executeOnType(ctx, cancel, rn)
}

// tempPath is the hidden copy's path for source.
func tempPath(source string) string {
	n := tempSeq.Add(1)
	return filepath.Join(filepath.Dir(source),
		TempPrefix+strconv.Itoa(os.Getpid())+"-"+strconv.FormatUint(n, 10)+"-"+filepath.Base(source))
}

// writeTemp creates the copy exclusively, so an unlucky name clash fails
// instead of overwriting someone's file.
func writeTemp(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("on-type copy: %w", err)
	}
	_, werr := f.WriteString(text)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("on-type copy %s: %w", path, firstErr(werr, cerr))
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) executeOnType(ctx context.Context, cancel context.CancelFunc, r run) {
	defer e.wg.Done()
	defer cancel()
	tmp := tempPath(r.path)
	if err := writeTemp(tmp, r.text); err != nil {
		// A read-only source directory is a fact about the tree, not news.
		e.logOnce("ontype:"+filepath.Dir(r.path), "on-type assembly off for %s: %v", filepath.Dir(r.path), err)
		return
	}
	res, err := e.tc.Assemble(ctx, tmp, r.flags)
	_ = os.Remove(tmp)
	if err != nil {
		if ctx.Err() == nil {
			e.quiet(err, r.path)
		}
		return
	}
	remapTemp(res, tmp, r.path)

	e.mu.Lock()
	st := r.st
	if e.closed || e.docs[st.uri] != st || st.runSeq != r.seq {
		e.mu.Unlock()
		return
	}
	st.gen++
	// The listing was made from exactly this version's text.
	st.asm, st.asmVersion = res, r.version
	ext := map[string][]protocol.Diagnostic{}
	for path, diags := range res.Diagnostics {
		if path != st.path {
			ext[toolchain.PathToURI(path)] = diags
		}
	}
	targets := e.setExtLocked(st.uri, ext)
	e.mu.Unlock()

	e.publish(st.uri)
	for _, t := range targets {
		if t != st.uri {
			e.publish(t)
		}
	}
}

// remapTemp files the copy's diagnostics under the real source, where the
// editor shows them.
func remapTemp(res *toolchain.AsmResult, tmp, source string) {
	if res == nil || res.Diagnostics == nil {
		return
	}
	if d, ok := res.Diagnostics[tmp]; ok {
		delete(res.Diagnostics, tmp)
		res.Diagnostics[source] = append(res.Diagnostics[source], d...)
	}
}
