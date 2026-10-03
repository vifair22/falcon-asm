package lint

import (
	"encoding/json"
	"sync"
)

// The hardware tables live in internal/hwdb, which depends on lint for the
// Tables interface. Rather than lint importing hwdb (a cycle) or every
// caller wiring the two, hwdb registers a loader from its init, and the
// engine and CLI ask lint for the tables of a configuration.
var tables struct {
	mu     sync.Mutex
	load   func(project json.RawMessage) (Tables, error)
	cached map[string]tablesResult
}

type tablesResult struct {
	t   Tables
	err error
}

// RegisterTables installs the loader that builds the hardware tables with a
// project's "tables" overrides (nil for none) merged over the built-in
// ones. The last registration wins.
func RegisterTables(load func(project json.RawMessage) (Tables, error)) {
	tables.mu.Lock()
	defer tables.mu.Unlock()
	tables.load = load
	tables.cached = nil
}

// LoadTables returns the tables for a configuration's raw "tables" value,
// built once per distinct value. With no loader registered it returns nil,
// and rules that consult tables must treat nil as "nothing known".
func LoadTables(project json.RawMessage) (Tables, error) {
	tables.mu.Lock()
	defer tables.mu.Unlock()
	if tables.load == nil {
		return nil, nil
	}
	key := string(project)
	if r, ok := tables.cached[key]; ok {
		return r.t, r.err
	}
	t, err := tables.load(project)
	if tables.cached == nil {
		tables.cached = map[string]tablesResult{}
	}
	tables.cached[key] = tablesResult{t, err}
	return t, err
}

// MSRChecker is the optional part of Tables that validates a value written
// with wrmsr. It is separate from Tables so a test fake (or a loader that
// knows nothing about MSRs) need not implement it; rules type-assert and stay
// quiet when it is missing. internal/hwdb's DB implements it.
type MSRChecker interface {
	// CheckMSRWrite lists the problems with writing value to the MSR at
	// index (reserved bits, a read-only register, a reserved memory type).
	CheckMSRWrite(index, value uint64) []string
	// MSRReserved is the reserved-bit mask of the MSR at index, and whether
	// the register is known at all.
	MSRReserved(index uint64) (uint64, bool)
}
