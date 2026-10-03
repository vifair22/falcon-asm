package symbols

import (
	"path/filepath"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// MaxWorkspaceSymbols caps a workspace/symbol answer. A client asks again
// as the query grows, so the first few hundred matches are all anyone
// scrolls through, and a kernel-sized index would otherwise send megabytes
// for a one-letter query.
const MaxWorkspaceSymbols = 500

// WorkspaceSymbols answers workspace/symbol over every indexed file:
// labels, .set/.equ symbols, #define macros and .macro names. Matching is
// case-insensitive and ranked exact, prefix, substring, then subsequence
// ("s2ld" finds stage2_load); an empty query lists everything. Private
// labels are included: in assembly they are most of what there is to
// navigate to, and the container name says which file each is in.
func (ix *Index) WorkspaceSymbols(query string, limit int) []protocol.SymbolInformation {
	if limit <= 0 || limit > MaxWorkspaceSymbols {
		limit = MaxWorkspaceSymbols
	}
	ix.ensureScanned()
	v := ix.snapshot()
	q := strings.ToLower(strings.TrimSpace(query))
	type hit struct {
		rank   int
		global bool
		info   protocol.SymbolInformation
	}
	var hits []hit
	for _, f := range v.files {
		container := filepath.Base(f.key)
		seen := map[string]bool{}
		for _, d := range f.defs {
			if d.name == "" || d.name[0] >= '0' && d.name[0] <= '9' {
				continue
			}
			// A .set reassigned through the file is one symbol, not one
			// per assignment.
			if d.kind == kindEqu {
				if seen[d.name] {
					continue
				}
				seen[d.name] = true
			}
			rank, ok := matchRank(q, d.name)
			if !ok {
				continue
			}
			hits = append(hits, hit{rank: rank, global: f.globals[d.name], info: protocol.SymbolInformation{
				Name:          d.name,
				Kind:          workspaceKind(f, d),
				Location:      f.loc(d.at),
				ContainerName: container,
			}})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.global != b.global {
			return a.global
		}
		if len(a.info.Name) != len(b.info.Name) {
			return len(a.info.Name) < len(b.info.Name)
		}
		return a.info.Name < b.info.Name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]protocol.SymbolInformation, len(hits))
	for i, h := range hits {
		out[i] = h.info
	}
	return out
}

func workspaceKind(f *file, d def) protocol.SymbolKind {
	switch d.kind {
	case kindLabel:
		if f.globals[d.name] {
			return protocol.SymbolKindFunction
		}
		return protocol.SymbolKindVariable
	case kindDefine:
		return protocol.SymbolKindConstant
	case kindAsmMacro:
		return protocol.SymbolKindNamespace
	}
	return protocol.SymbolKindVariable
}

// matchRank scores name against a lowercased query: 0 exact, 1 prefix,
// 2 substring, 3 subsequence; ok is false when the query's characters do
// not all appear in order.
func matchRank(q, name string) (int, bool) {
	if q == "" {
		return 0, true
	}
	n := strings.ToLower(name)
	switch {
	case n == q:
		return 0, true
	case strings.HasPrefix(n, q):
		return 1, true
	case strings.Contains(n, q):
		return 2, true
	}
	j := 0
	for i := 0; i < len(n) && j < len(q); i++ {
		if n[i] == q[j] {
			j++
		}
	}
	return 3, j == len(q)
}
