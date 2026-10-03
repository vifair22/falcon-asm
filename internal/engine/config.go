package engine

import (
	"path/filepath"

	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// fileConfig resolves a file's lint configuration and hardware tables from
// the nearest .falcon-asm.json above it. The search and the read happen
// outside the lock, like flagsFor's, and the result is cached per
// directory. A configuration seen for the first time has its problems
// published on its own URI.
func (e *Engine) fileConfig(path string) (lint.FileConfig, lint.Tables) {
	c := e.configFor(filepath.Dir(path))
	fc := c.For(path)
	t, err := lint.LoadTables(fc.Tables)
	if err != nil {
		e.logOnce("tables:"+err.Error(), "hardware tables: %v; built-in tables only", err)
		t, _ = lint.LoadTables(nil)
	}
	return fc, t
}

func (e *Engine) configFor(dir string) *lint.Config {
	e.mu.Lock()
	cp, cached := e.cfgDir[dir]
	c := e.configs[cp]
	e.mu.Unlock()
	if cached {
		return c
	}
	cp, found := e.cfg.FindConfig(dir)
	var loaded *lint.Config
	if found {
		e.mu.Lock()
		loaded = e.configs[cp]
		e.mu.Unlock()
		if loaded == nil {
			data, err := e.cfg.ReadFile(cp)
			if err != nil {
				e.logOnce("cfg:"+cp, "%s: %v; using defaults", cp, err)
				found = false
			} else {
				loaded = lint.ParseConfig(cp, data)
			}
		}
	}
	fresh := false
	e.mu.Lock()
	switch {
	case !found:
		e.cfgDir[dir] = ""
		loaded = nil
	case e.configs[cp] != nil:
		// Another open loaded it first; share that one.
		loaded = e.configs[cp]
		e.cfgDir[dir] = cp
	default:
		e.configs[cp] = loaded
		e.cfgDir[dir] = cp
		e.cfgDiags[toolchain.PathToURI(cp)] = problemDiagnostics(loaded)
		fresh = true
	}
	e.mu.Unlock()
	if fresh && len(loaded.Problems) > 0 {
		e.publish(toolchain.PathToURI(cp))
	}
	return loaded
}

// applyConfig replaces the configuration at path (data nil: the file is
// gone) and re-resolves every open document, since any of them may lie
// under it, or under a directory that now finds it first.
func (e *Engine) applyConfig(path, uri string, data []byte) {
	var c *lint.Config
	if data != nil {
		c = lint.ParseConfig(path, data)
	}
	e.mu.Lock()
	if c != nil {
		e.configs[path] = c
		e.cfgDiags[uri] = problemDiagnostics(c)
	} else {
		delete(e.configs, path)
		delete(e.cfgDiags, uri)
	}
	// A new or removed file changes which configuration directories find.
	e.cfgDir = map[string]string{}
	var docs []*docState
	for _, st := range e.docs {
		if st.lang != langConfig && st.path != "" {
			docs = append(docs, st)
		}
	}
	e.mu.Unlock()

	type resolved struct {
		fc lint.FileConfig
		t  lint.Tables
	}
	res := make([]resolved, len(docs))
	for i, st := range docs {
		res[i].fc, res[i].t = e.fileConfig(st.path)
	}
	e.mu.Lock()
	for i, st := range docs {
		if e.docs[st.uri] != st {
			continue
		}
		st.fileCfg, st.tables = res[i].fc, res[i].t
		st.gen++
		e.schedulePublishLocked(st)
	}
	e.mu.Unlock()
	e.publish(uri)
}

// problemDiagnostics turns configuration problems into diagnostics on the
// configuration file. A non-nil slice, so a fixed file publishes a clear.
func problemDiagnostics(c *lint.Config) []protocol.Diagnostic {
	out := []protocol.Diagnostic{}
	for _, p := range c.Problems {
		out = append(out, protocol.Diagnostic{
			Range:    p.Range,
			Severity: protocol.DiagnosticSeverity(p.Severity),
			Code:     "config",
			Source:   lint.Source,
			Message:  p.Message,
		})
	}
	return out
}
