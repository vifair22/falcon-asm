package lint

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The registry is written from init functions (one per rule package) and
// read afterwards from any goroutine. The mutex makes the rare late
// registration (tests) safe; the sorted view is rebuilt lazily.
var registry struct {
	mu     sync.RWMutex
	byCode map[string]*Rule
	sorted []*Rule
}

// Register adds a rule. It panics on an invalid code, a duplicate code, a
// section outside 1..12, a nil Check, an invalid default severity or a
// rule with no language: every one of those is a programming error in a
// rule package, caught the moment the binary starts rather than surfacing
// as a rule that silently never runs.
func Register(r *Rule) {
	if r == nil {
		panic("lint: Register(nil)")
	}
	if !ValidCode(r.Code) {
		panic(fmt.Sprintf("lint: invalid rule code %q (want kebab-case, not %q)", r.Code, CodeAll))
	}
	if r.Section < 1 || r.Section > 12 {
		panic(fmt.Sprintf("lint: rule %s: section %d outside 1..12", r.Code, r.Section))
	}
	if r.Check == nil {
		panic(fmt.Sprintf("lint: rule %s has no Check", r.Code))
	}
	if r.Default < Off || r.Default > Hint {
		panic(fmt.Sprintf("lint: rule %s: invalid default severity %d", r.Code, r.Default))
	}
	if r.Langs&(LangAsm|LangInline|LangC) == 0 {
		panic(fmt.Sprintf("lint: rule %s runs on no language", r.Code))
	}
	if strings.TrimSpace(r.Doc.Summary) == "" {
		panic(fmt.Sprintf("lint: rule %s has no Doc.Summary", r.Code))
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.byCode == nil {
		registry.byCode = map[string]*Rule{}
	}
	if _, dup := registry.byCode[r.Code]; dup {
		panic(fmt.Sprintf("lint: rule %s registered twice", r.Code))
	}
	registry.byCode[r.Code] = r
	registry.sorted = nil
}

// unregister removes a rule; tests use it to keep the registry as the
// rule packages left it.
func unregister(code string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	delete(registry.byCode, code)
	registry.sorted = nil
}

// Rules returns every registered rule ordered by roadmap section, then
// code. The slice is shared: callers must not modify it.
func Rules() []*Rule {
	registry.mu.RLock()
	s := registry.sorted
	registry.mu.RUnlock()
	if s != nil {
		return s
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.sorted == nil {
		s = make([]*Rule, 0, len(registry.byCode))
		for _, r := range registry.byCode {
			s = append(s, r)
		}
		sort.Slice(s, func(i, j int) bool {
			if s[i].Section != s[j].Section {
				return s[i].Section < s[j].Section
			}
			return s[i].Code < s[j].Code
		})
		registry.sorted = s
	}
	return registry.sorted
}

// Lookup finds a rule by code.
func Lookup(code string) (*Rule, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	r, ok := registry.byCode[code]
	return r, ok
}

// CodeAll is the suppression wildcard; no rule may take it as its code.
const CodeAll = "all"

// ValidCode reports whether code is a usable rule code: lowercase
// kebab-case words of letters and digits, starting with a letter.
func ValidCode(code string) bool {
	if code == "" || code == CodeAll || code[0] < 'a' || code[0] > 'z' {
		return false
	}
	prevDash := false
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c == '-':
			if prevDash {
				return false
			}
			prevDash = true
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			prevDash = false
		default:
			return false
		}
	}
	return !prevDash
}

var severityNames = [...]string{Off: "off", Error: "error", Warning: "warning", Info: "info", Hint: "hint"}

// String is the configuration spelling of the severity.
func (s Severity) String() string {
	if s >= Off && int(s) < len(severityNames) {
		return severityNames[s]
	}
	return fmt.Sprintf("Severity(%d)", int(s))
}

// ParseSeverity reads a configuration severity. "information" is accepted
// as a synonym of "info" because that is the LSP's own name for it.
func ParseSeverity(s string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return Off, true
	case "error":
		return Error, true
	case "warning", "warn":
		return Warning, true
	case "info", "information":
		return Info, true
	case "hint":
		return Hint, true
	}
	return Off, false
}

// AtLeast reports whether s is as severe as min or more. Off is below
// everything; Error is the most severe even though it is numerically
// smallest, which is the LSP's ordering.
func (s Severity) AtLeast(min Severity) bool {
	if s == Off {
		return false
	}
	if min == Off {
		return true
	}
	return s <= min
}

// String lists the languages, for documentation.
func (l Lang) String() string {
	var parts []string
	if l&LangAsm != 0 {
		parts = append(parts, "assembly")
	}
	if l&LangInline != 0 {
		parts = append(parts, "inline asm")
	}
	if l&LangC != 0 {
		parts = append(parts, "C")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// SectionNames are the roadmap section titles, for documentation.
var SectionNames = [...]string{
	1:  "Encoding and operand validity",
	2:  "Mode, CPU and ISA target",
	3:  "Dataflow",
	4:  "Calling conventions",
	5:  "Boot and system protocol checks",
	6:  "Memory layout and size",
	7:  "Hardware knowledge",
	8:  "Performance lints",
	9:  "Inline asm in C",
	10: "Symbols and structure",
	11: "Toolchain truth",
	12: "Lint workflow",
}
