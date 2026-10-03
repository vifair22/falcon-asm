package callconv

import (
	"fmt"
	"strconv"
	"strings"
)

// Annotation is what one `falcon-asm:` comment says about conventions
// and handlers. Items this package does not own (ignore=, level=,
// budget=) are skipped, and parsing stops at the first bare word that is
// not "handler", which is how free text after the items is allowed, as
// the suppression comments allow it.
type Annotation struct {
	// CC is the convention name as written ("" when the comment has no
	// cc= item); Spec holds a custom convention's lists.
	CC   string
	Spec CustomSpec
	// HasSpec reports a clobbers/preserves/args/returns item.
	HasSpec bool

	// Handler marks an interrupt or exception handler; HandlerKind is
	// what follows "handler=" ("errcode", "noerrcode", "service" or "").
	Handler     bool
	HandlerKind string
	// Vector is the interrupt vector from "vector=N", -1 when absent.
	Vector int

	// Problems are items that could not be understood, for the
	// annotation rule to report.
	Problems []string
}

// Marker introduces every falcon-asm comment directive.
const Marker = "falcon-asm:"

// ParseComment reads the annotation in a comment's text (delimiters
// included or not). ok is false when the comment holds no falcon-asm
// marker or none of this package's items.
func ParseComment(text string) (Annotation, bool) {
	a := Annotation{Vector: -1}
	i := strings.Index(strings.ToLower(text), Marker)
	if i < 0 {
		return a, false
	}
	rest := text[i+len(Marker):]
	if j := strings.Index(rest, "*/"); j >= 0 {
		rest = rest[:j]
	}
	found := false
	for _, item := range strings.Fields(rest) {
		key, val, hasVal := strings.Cut(item, "=")
		key = strings.ToLower(key)
		if !hasVal {
			if key != "handler" {
				break
			}
			a.Handler, found = true, true
			continue
		}
		list := func() []string {
			a.HasSpec, found = true, true
			return strings.Split(val, ",")
		}
		switch key {
		case "cc":
			a.CC, found = strings.ToLower(val), true
		case "clobbers":
			a.Spec.Clobbers = list()
		case "preserves":
			a.Spec.Preserves = list()
		case "args":
			a.Spec.Args = list()
		case "returns":
			a.Spec.Returns = list()
		case "handler":
			a.Handler, found = true, true
			a.HandlerKind = strings.ToLower(val)
			switch a.HandlerKind {
			case "errcode", "noerrcode", "service":
			default:
				a.Problems = append(a.Problems, fmt.Sprintf("unknown handler kind %q (known: errcode, noerrcode, service)", val))
				a.HandlerKind = ""
			}
		case "vector":
			found = true
			v, err := strconv.ParseUint(val, 0, 16)
			if err != nil || v > 255 {
				a.Problems = append(a.Problems, fmt.Sprintf("invalid vector %q (want 0..255)", val))
				continue
			}
			a.Vector = int(v)
		}
	}
	return a, found
}

// Conv resolves the annotation's convention: nil when it names none (it
// may still be a handler annotation). A custom convention is built from
// the lists. Lists without any cc= item imply cc=custom: no other
// convention takes them, so requiring the item would only make a helper
// that forgot it lose its checks for no gain. Lists next to a cc= naming
// another convention are a problem, since one of the two is a mistake.
func (a *Annotation) Conv() (*Conv, []string) {
	var problems []string
	name := a.CC
	if name == "" {
		if !a.HasSpec {
			return nil, nil
		}
		name = "custom"
	}
	c, ok := Lookup(name)
	if !ok {
		return nil, append(problems, fmt.Sprintf("unknown calling convention %q (known: %s)", a.CC, Names))
	}
	if c.Kind == Custom {
		cc, err := NewCustom(a.Spec)
		if err != nil {
			problems = append(problems, err.Error())
		}
		if !a.HasSpec {
			problems = append(problems, "cc=custom declares nothing without clobbers=, preserves=, args= or returns=")
		}
		return cc, problems
	}
	if a.HasSpec {
		problems = append(problems, fmt.Sprintf("clobbers/preserves/args/returns apply only to cc=custom, not %s: "+
			"drop cc=%s (lists alone mean cc=custom) or drop the lists", c.Name, a.CC))
	}
	return c, problems
}

// errcodeVectors are the exceptions for which a 32- or 64-bit processor
// pushes an error code: #DF, #TS, #NP, #SS, #GP, #PF, #AC, #CP, #VC, #SX.
var errcodeVectors = map[int]bool{8: true, 10: true, 11: true, 12: true, 13: true, 14: true, 17: true,
	21: true, 29: true, 30: true}

// ErrorCode reports whether the processor pushes an error code for vector
// v in protected or long mode. Real mode never does.
func ErrorCode(v int) bool { return errcodeVectors[v] }
