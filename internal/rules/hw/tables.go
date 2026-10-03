package hw

import (
	"encoding/json"

	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// The hardware tables reach rules through lint.Tables, built by a loader
// registered with lint. It is registered here rather than from hwdb itself:
// hwdb stays free of the lint engine (and of everything lint builds on),
// and every binary that runs rules imports this package through
// internal/rules/all anyway.
func init() {
	lint.RegisterTables(loadTables)
}

func loadTables(raw json.RawMessage) (lint.Tables, error) {
	db, err := hwdb.Load(raw)
	if err != nil {
		// A typed nil inside the interface would read as "tables present".
		return nil, err
	}
	return db, nil
}

// DB is the hardware database behind tables, or the built-in one when the
// tables are missing or not hwdb's: hover always has something to show.
func DB(t lint.Tables) *hwdb.DB {
	if db, ok := t.(*hwdb.DB); ok && db != nil {
		return db
	}
	return hwdb.Default()
}

var (
	_ lint.Tables     = (*hwdb.DB)(nil)
	_ lint.MSRChecker = (*hwdb.DB)(nil)
)
