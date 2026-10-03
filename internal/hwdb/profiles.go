package hwdb

import (
	"fmt"
	"sort"
	"strings"
)

// Profile is a target CPU: the internal/isa ISA sets (XED ISA_SET names) it
// implements. Each profile names a base and adds to it, the way each
// generation extends the last; the JSON stays one line per generation and
// the full set is resolved at load.
//
// Only features every part of the generation has are listed, so a profile
// errs toward "unsupported". Rule 16 reports an instruction whose ISA set the
// profile lacks; it never reports one the profile has.
type Profile struct {
	Name  string   `json:"name"`
	Desc  string   `json:"desc"`
	Base  string   `json:"base,omitempty"`
	Level int      `json:"level,omitempty"` // psABI level for the x86-64-vN profiles
	Add   []string `json:"add"`
	Notes []string `json:"notes,omitempty"`

	sets     map[string]bool
	prefixes []string // from "AVX512F_*"
}

// Supports reports whether the profile implements an ISA set. Matching is
// case-insensitive. An empty set name (an instruction the tables do not
// classify) is supported: unknown never warns.
func (p *Profile) Supports(isaSet string) bool {
	s := strings.ToUpper(strings.TrimSpace(isaSet))
	if s == "" || p.sets[s] {
		return true
	}
	for _, pre := range p.prefixes {
		if strings.HasPrefix(s, pre) {
			return true
		}
	}
	return false
}

// Sets lists the profile's ISA sets, sorted; prefix patterns keep their "*".
func (p *Profile) Sets() []string {
	out := make([]string, 0, len(p.sets)+len(p.prefixes))
	for s := range p.sets {
		out = append(out, s)
	}
	for _, pre := range p.prefixes {
		out = append(out, pre+"*")
	}
	sort.Strings(out)
	return out
}

// LongMode reports whether the profile can run 64-bit code.
func (p *Profile) LongMode() bool { return p.sets["LONGMODE"] }

type profileFile struct {
	Profiles []Profile         `json:"profiles"`
	Aliases  map[string]string `json:"aliases"`
}

type profileTable struct {
	order   []string
	byName  map[string]*Profile
	aliases map[string]string
}

func loadProfiles() (*profileTable, error) {
	var f profileFile
	if err := readJSON("profiles.json", &f); err != nil {
		return &profileTable{byName: map[string]*Profile{}}, err
	}
	t, err := buildProfiles(f, "profiles.json")
	if err != nil {
		return &profileTable{byName: map[string]*Profile{}}, err
	}
	return t, nil
}

func buildProfiles(f profileFile, file string) (*profileTable, error) {
	t := &profileTable{byName: map[string]*Profile{}, aliases: map[string]string{}}
	for i := range f.Profiles {
		p := &f.Profiles[i]
		path := fmt.Sprintf("%s: profiles[%d]", file, i)
		if p.Name == "" {
			return nil, fmt.Errorf("%s.name: empty", path)
		}
		if _, dup := t.byName[p.Name]; dup {
			return nil, fmt.Errorf("%s: duplicate profile %q", path, p.Name)
		}
		p.sets = map[string]bool{}
		if p.Base != "" {
			// Bases must come earlier in the file, which also rules out
			// cycles.
			b, ok := t.byName[p.Base]
			if !ok {
				return nil, fmt.Errorf("%s.base: %q is not defined above", path, p.Base)
			}
			for s := range b.sets {
				p.sets[s] = true
			}
			p.prefixes = append(p.prefixes, b.prefixes...)
		}
		for j, s := range p.Add {
			us := strings.ToUpper(s)
			if pre, ok := strings.CutSuffix(us, "*"); ok {
				for _, have := range p.prefixes {
					if have == pre {
						return nil, fmt.Errorf("%s.add[%d]: %s already inherited", path, j, s)
					}
				}
				p.prefixes = append(p.prefixes, pre)
				continue
			}
			if p.sets[us] {
				return nil, fmt.Errorf("%s.add[%d]: %s already present", path, j, s)
			}
			p.sets[us] = true
		}
		t.byName[p.Name] = p
		t.order = append(t.order, p.Name)
	}
	for a, target := range f.Aliases {
		if _, ok := t.byName[target]; !ok {
			return nil, fmt.Errorf("%s: alias %q names unknown profile %q", file, a, target)
		}
		if _, clash := t.byName[a]; clash {
			return nil, fmt.Errorf("%s: alias %q shadows a profile", file, a)
		}
		t.aliases[a] = target
	}
	return t, nil
}

// CPUProfile resolves a profile by name or gcc -march alias ("i686",
// "corei7", "core-avx2"), case-insensitively.
func CPUProfile(name string) (*Profile, bool) {
	t := profileTab()
	n := strings.ToLower(strings.TrimSpace(name))
	if a, ok := t.aliases[n]; ok {
		n = a
	}
	p, ok := t.byName[n]
	return p, ok
}

// ProfileForMarch maps a gcc -march value to a profile name. "native" and
// unknown values map to nothing: the linter then has no CPU target.
func ProfileForMarch(march string) (string, bool) {
	p, ok := CPUProfile(march)
	if !ok {
		return "", false
	}
	return p.Name, true
}

// Profiles lists the canonical profile names, oldest first within each
// vendor line, as the table orders them.
func Profiles() []string {
	return append([]string(nil), profileTab().order...)
}

// ProfileAliases lists the alias names (gcc -march spellings), sorted.
func ProfileAliases() []string {
	t := profileTab()
	out := make([]string, 0, len(t.aliases))
	for a := range t.aliases {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
