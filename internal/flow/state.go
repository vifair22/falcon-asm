package flow

import "math/bits"

// widthMask is the low w bits set (w in 1..64).
func widthMask(w int) uint64 {
	if w >= 64 {
		return ^uint64(0)
	}
	if w <= 0 {
		return 0
	}
	return uint64(1)<<uint(w) - 1
}

// bitsMask is bits lo..hi set.
func bitsMask(lo, hi int) uint64 { return widthMask(hi-lo+1) << uint(lo) }

// Known reports whether every bit in m is known.
func (v Value) Known(m uint64) bool { return v.Mask&m == m }

// konst is a fully known w-bit value.
func konst(x uint64, w int) Value {
	m := widthMask(w)
	return Value{V: x & m, Mask: m}
}

func (s *State) get(r Reg) Value {
	x := s.regs[r]
	return Value{V: x.v, Mask: x.mask, Def: int(s.defs[r])}
}

func (s *State) set(r Reg, v Value) {
	s.regs[r] = rv{v.V & v.Mask, v.Mask}
	s.defs[r] = int32(v.Def)
}

// Get returns the family's value, unknown (Mask 0, Def DefEntry) for a
// family without one.
func (s *State) Get(r Reg) Value {
	if !r.HasValue() {
		return Value{Def: DefEntry}
	}
	return s.get(r)
}

// Reg returns the family's value; ok is false when no bit is known or the
// family carries no value (mask and vector registers).
func (s *State) Reg(r Reg) (Value, bool) {
	if !r.HasValue() {
		return Value{Def: DefEntry}, false
	}
	v := s.get(r)
	return v, v.Mask != 0
}

// SetReg replaces the family's value. Ignored for families without one.
func (s *State) SetReg(r Reg, v Value) {
	if r.HasValue() {
		v.V &= v.Mask
		s.set(r, v)
	}
}

// Clobber makes the family unknown, written by statement def.
func (s *State) Clobber(r Reg, def int) {
	switch {
	case r.HasValue():
		s.set(r, Value{Def: def})
		if r == RBP {
			s.FrameKnown = false
		}
		if r == RSP {
			s.unknownSP()
		}
	case r.IsVector():
		s.vecKnown &^= 1 << uint(r-XMM0)
	}
}

// Part returns bits lo..hi of the family's value shifted down to bit 0.
func (s *State) Part(r Reg, lo, hi int) (v, mask uint64) {
	if !r.HasValue() {
		return 0, 0
	}
	x := s.get(r)
	m := widthMask(hi - lo + 1)
	return (x.V >> uint(lo)) & m, (x.Mask >> uint(lo)) & m
}

// UpperDirty reports whether the upper bits (above 127) of vector family
// r may be non-zero: Set after a VEX.256 or EVEX.512 write, Clear after a
// VEX.128 write or vzeroupper, Unknown at entry or after an unknown call.
// Legacy SSE writes leave it as it was.
func (s *State) UpperDirty(r Reg) Tri {
	if !r.IsVector() {
		return Unknown
	}
	b := uint32(1) << uint(r-XMM0)
	switch {
	case s.vecKnown&b == 0:
		return Unknown
	case s.vecDirty&b != 0:
		return Set
	}
	return Clear
}

// AnyUpperDirty is Set when some vector family is known dirty, Clear when
// every family is known clean, Unknown otherwise.
func (s *State) AnyUpperDirty() Tri {
	switch {
	case s.vecKnown&s.vecDirty != 0:
		return Set
	case s.vecKnown == ^uint32(0):
		return Clear
	}
	return Unknown
}

func (s *State) setUpper(r Reg, t Tri) {
	b := uint32(1) << uint(r-XMM0)
	switch t {
	case Unknown:
		s.vecKnown &^= b
	case Set:
		s.vecKnown |= b
		s.vecDirty |= b
	case Clear:
		s.vecKnown |= b
		s.vecDirty &^= b
	}
}

// Slots returns the abstract stack, oldest entry first. Only the most
// recent pushes are kept (up to ten); older ones fall off, and every slot
// is dropped when the depth becomes unknown.
func (s *State) Slots() []Slot {
	out := make([]Slot, s.nslot)
	for k := range out {
		out[k] = s.slots[k].export()
	}
	return out
}

// Top returns the most recent stack slot.
func (s *State) Top() (Slot, bool) {
	if s.nslot == 0 {
		return Slot{}, false
	}
	return s.slots[s.nslot-1].export(), true
}

// Pushed reports the depth at which family r was most recently pushed and
// is still on the stack (the slot's Depth), for callee-saved checks.
func (s *State) Pushed(r Reg) (depth int, ok bool) {
	for i := int(s.nslot) - 1; i >= 0; i-- {
		if Reg(s.slots[i].src) == r {
			return int(s.slots[i].depth), true
		}
	}
	return 0, false
}

// loseStack forgets the depth and everything derived from it.
func (s *State) loseStack() {
	s.StackKnown = false
	s.Stack = 0
	s.nslot = 0
	s.FrameKnown = false
	s.spSave = 0
}

// unknownSP is a write of an untracked value to the stack pointer
// (and $-16, a load of a fresh stack). The depth is lost, but the frame
// pointer and a parked stack pointer still lead back to known depths, so
// the slots at or below the deepest of those stay: "mov %ebp, %esp" or
// "mov saved, %esp" restores the depth and pops then find what was pushed.
// Code running on the other stack is assumed not to overwrite them.
func (s *State) unknownSP() {
	keep := -1
	if s.FrameKnown {
		keep = s.FrameDepth
	}
	if s.spSave != 0 && s.spDepth > keep {
		keep = s.spDepth
	}
	for s.nslot > 0 && int(s.slots[s.nslot-1].depth) > keep {
		s.nslot--
	}
	s.StackKnown = false
	s.Stack = 0
}

// push records n bytes pushed holding val.
func (s *State) push(n int, val Value, src Reg, flags bool) {
	if !s.StackKnown {
		return
	}
	s.Stack += n
	if s.Stack > 1<<15-1 || s.Stack < -1<<15 {
		return
	}
	if s.nslot == maxSlots {
		copy(s.slots[:], s.slots[1:])
		s.nslot--
	}
	w := uint8(n)
	if flags {
		w |= slotFlags
	}
	s.slots[s.nslot] = slot{v: val.V & val.Mask, mask: val.Mask, def: int32(val.Def), depth: int16(s.Stack),
		width: w, src: int8(src)}
	s.nslot++
}

// pop removes n bytes and returns what the abstract stack knows of them.
func (s *State) pop(n int) (Slot, bool) {
	if !s.StackKnown {
		return Slot{}, false
	}
	var top Slot
	ok := false
	if s.nslot > 0 {
		t := s.slots[s.nslot-1]
		if int(t.depth) == s.Stack && int(t.width&^slotFlags) == n {
			top, ok = t.export(), true
		}
	}
	s.adjust(-n)
	return top, ok
}

// adjust moves the depth by delta bytes (positive grows the stack) and
// drops slots no longer on it. A slot that is only partly popped is
// dropped too: its value is no longer what a later pop would read.
func (s *State) adjust(delta int) {
	if !s.StackKnown {
		return
	}
	s.Stack += delta
	for s.nslot > 0 && int(s.slots[s.nslot-1].depth) > s.Stack {
		s.nslot--
	}
	if s.FrameKnown && s.FrameDepth > s.Stack {
		// The frame pointer now points into popped stack; leave would
		// not restore a meaningful depth.
		s.FrameKnown = false
	}
}

// setDepth makes the depth known as d (a frame or parked stack pointer
// restored), dropping slots above it.
func (s *State) setDepth(d int) {
	if s.StackKnown {
		s.adjust(d - s.Stack)
		return
	}
	s.Stack, s.StackKnown = d, true
	for s.nslot > 0 && int(s.slots[s.nslot-1].depth) > d {
		s.nslot--
	}
}

// joinValue meets two values: bits known in both with equal values stay
// known; Def survives only when both agree.
func joinValue(a, b Value) Value {
	m := a.Mask & b.Mask &^ (a.V ^ b.V)
	d := a.Def
	if a.Def != b.Def {
		d = DefMerged
	}
	return Value{V: a.V & m, Mask: m, Def: d}
}

func joinTri(a, b Tri) Tri {
	if a == b {
		return a
	}
	return Unknown
}

// join meets src into s and reports whether s changed. widen makes
// every field that changes collapse straight to its unknown value, which
// bounds how many times a block in a loop can be revisited.
func (s *State) join(src *State, widen bool) bool {
	if !src.Reachable {
		return false
	}
	if !s.Reachable {
		*s = *src
		return true
	}
	changed := false
	for i := range s.regs {
		a, b := s.regs[i], src.regs[i]
		da, db := s.defs[i], src.defs[i]
		if a == b && da == db {
			continue
		}
		m := a.mask & b.mask &^ (a.v ^ b.v)
		if widen && m != a.mask {
			m = 0
		}
		d := da
		if da != db {
			d = DefMerged
		}
		if m != a.mask || d != da {
			s.regs[i] = rv{a.v & m, m}
			s.defs[i] = d
			changed = true
		}
	}
	for i := range s.FlagDef {
		if s.FlagDef[i] != src.FlagDef[i] && s.FlagDef[i] != DefMerged {
			s.FlagDef[i] = DefMerged
			changed = true
		}
	}
	if t := joinTri(s.DF, src.DF); t != s.DF {
		s.DF, changed = t, true
	}
	if t := joinTri(s.IF, src.IF); t != s.IF {
		s.IF, changed = t, true
	}
	if s.StackKnown && (!src.StackKnown || src.Stack != s.Stack) {
		s.StackKnown = false
		s.Stack = 0
		changed = true
	}
	if s.joinSlots(src) {
		changed = true
	}
	if s.FrameKnown && (!src.FrameKnown || src.FrameDepth != s.FrameDepth) {
		s.FrameKnown = false
		changed = true
	}
	if s.spSave != 0 && (s.spSave != src.spSave || s.spDepth != src.spDepth) {
		s.spSave = 0
		changed = true
	}
	known := s.vecKnown & src.vecKnown &^ (s.vecDirty ^ src.vecDirty)
	if known != s.vecKnown {
		s.vecKnown = known
		s.vecDirty &= known
		changed = true
	}
	return changed
}

// joinSlots keeps the slots both states agree on, matched from the top by
// depth and width, with their values met.
func (s *State) joinSlots(src *State) bool {
	changed := false
	// Align from the top: the newest slots are the ones a pop reads.
	n := int(s.nslot)
	if int(src.nslot) < n {
		n = int(src.nslot)
	}
	keep := 0
	for k := 1; k <= n; k++ {
		a := &s.slots[int(s.nslot)-k]
		b := &src.slots[int(src.nslot)-k]
		if a.depth != b.depth || a.width != b.width {
			break
		}
		keep = k
	}
	if keep != int(s.nslot) {
		copy(s.slots[:], s.slots[int(s.nslot)-keep:s.nslot])
		s.nslot = uint8(keep)
		changed = true
	}
	off := int(src.nslot) - keep
	for k := 0; k < keep; k++ {
		a := &s.slots[k]
		b := &src.slots[off+k]
		if a.src != b.src && a.src != int8(NoReg) {
			a.src = int8(NoReg)
			changed = true
		}
		if a.v != b.v || a.mask != b.mask || a.def != b.def {
			j := joinValue(Value{V: a.v, Mask: a.mask, Def: int(a.def)}, Value{V: b.v, Mask: b.mask, Def: int(b.def)})
			a.v, a.mask, a.def = j.V, j.Mask, int32(j.Def)
			changed = true
		}
	}
	return changed
}

// --- value arithmetic on partially known values (width w, bits above w
// ignored and returned zero/unknown) ---

func valAnd(a, b Value, w int) Value {
	m := widthMask(w)
	zeroA := a.Mask &^ a.V
	zeroB := b.Mask &^ b.V
	known := (a.Mask&b.Mask | zeroA | zeroB) & m
	return Value{V: a.V & b.V & known, Mask: known}
}

func valOr(a, b Value, w int) Value {
	m := widthMask(w)
	oneA := a.Mask & a.V
	oneB := b.Mask & b.V
	known := (a.Mask&b.Mask | oneA | oneB) & m
	return Value{V: (a.V | b.V) & known, Mask: known}
}

func valXor(a, b Value, w int) Value {
	known := a.Mask & b.Mask & widthMask(w)
	return Value{V: (a.V ^ b.V) & known, Mask: known}
}

func valNot(a Value, w int) Value {
	known := a.Mask & widthMask(w)
	return Value{V: ^a.V & known, Mask: known}
}

// valAdd knows the low bits up to the first bit unknown in either input:
// a carry out of an unknown bit can change every bit above it.
func valAdd(a, b Value, w int) Value {
	m := widthMask(w)
	unknown := (^a.Mask | ^b.Mask) & m
	known := m
	if unknown != 0 {
		known = widthMask(bits.TrailingZeros64(unknown))
	}
	return Value{V: (a.V + b.V) & known, Mask: known}
}

func valNeg(a Value, w int) Value {
	return valAdd(valNot(a, w), konst(1, w), w)
}

func valSub(a, b Value, w int) Value {
	return valAdd(a, valNeg(b, w), w)
}

func valShl(a Value, n uint, w int) Value {
	m := widthMask(w)
	if n >= uint(w) {
		return konst(0, w)
	}
	return Value{V: (a.V << n) & m, Mask: (a.Mask<<n | widthMask(int(n))) & m}
}

func valShr(a Value, n uint, w int) Value {
	m := widthMask(w)
	if n >= uint(w) {
		return konst(0, w)
	}
	top := m &^ (m >> n)
	return Value{V: (a.V & m) >> n, Mask: (a.Mask&m)>>n | top}
}

func valSar(a Value, n uint, w int) Value {
	m := widthMask(w)
	if n >= uint(w) {
		n = uint(w) - 1
	}
	sign := uint64(1) << uint(w-1)
	r := valShr(a, n, w)
	top := m &^ (m >> n)
	r.Mask &^= top
	if a.Mask&sign != 0 {
		r.Mask |= top
		if a.V&sign != 0 {
			r.V |= top
		}
	}
	return r
}

func rotl(x uint64, n uint, w int) uint64 {
	m := widthMask(w)
	x &= m
	n %= uint(w)
	if n == 0 {
		return x
	}
	return (x<<n | x>>(uint(w)-n)) & m
}

func valRol(a Value, n uint, w int) Value {
	return Value{V: rotl(a.V, n, w), Mask: rotl(a.Mask, n, w)}
}

func valRor(a Value, n uint, w int) Value {
	n %= uint(w)
	return valRol(a, uint(w)-n, w)
}

// extend widens a from bit width from to width to, zero- or sign-filling.
func extend(a Value, from, to int, signed bool) Value {
	fm := widthMask(from)
	r := Value{V: a.V & fm, Mask: a.Mask & fm}
	upper := widthMask(to) &^ fm
	if !signed {
		r.Mask |= upper
		return r
	}
	sign := uint64(1) << uint(from-1)
	if a.Mask&sign != 0 {
		r.Mask |= upper
		if a.V&sign != 0 {
			r.V |= upper
		}
	}
	return r
}
