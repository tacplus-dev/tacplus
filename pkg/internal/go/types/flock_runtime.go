// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

// goFlockRuntime is the schema-independent half of flock state (tacplus#206)
// for Go: the hybrid clock, the deletion mark order, the op sets of SET_*
// fields and the canonical element encoding. flock_gen.go emits it once into
// every package with a flock entry, beside that package's per-entry merge.
// The Rust and Java emitters carry the same algorithm, line for line; change
// one and change them all (cross-test/flock checks they agree).
const goFlockRuntime = `
// Flock timing constants (tacplus#206), the same in every language.
const (
	// FlockMaxSkewMs: a remote clock time further ahead of local wall time
	// than this never moves the local clock; the write is held.
	FlockMaxSkewMs int64 = %[1]d
	// FlockRebuildAfterMs: a copy absent longer than this is wiped and rebuilt
	// from its peers, never merged.
	FlockRebuildAfterMs int64 = %[2]d
	// FlockMaxMessageAgeMs: replicated state older than this is dropped, and a
	// mark is purged only this long after its last acknowledgement.
	FlockMaxMessageAgeMs int64 = %[3]d
)

// FlockOverlap is the verdict of the unique check on two live entries that
// share an element of a (spec.unique) set.
type FlockOverlap int

const (
	// FlockOverlapQuarantine: refuse to use the element and alert; a person
	// resolves it with proof.
	FlockOverlapQuarantine FlockOverlap = iota
	// FlockOverlapAutoMerge: the partition race (both entries were created with
	// the element, within the window): merge them with FlockMergeEntries.
	FlockOverlapAutoMerge
)

// FlockClock is a copy's hybrid logical clock.
type FlockClock struct {
	node string
	now  func() int64
	last *flock.Hlc
}

// NewFlockClock returns the clock of copy node; now returns wall-clock Unix
// milliseconds (nil: the system clock).
func NewFlockClock(node string, now func() int64) *FlockClock {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &FlockClock{node: node, now: now, last: &flock.Hlc{}}
}

// Now returns a time later than every time this clock has made or observed.
func (c *FlockClock) Now() *flock.Hlc {
	wall := c.now()
	if wall > c.last.GetWallMs() {
		c.last = &flock.Hlc{WallMs: wall, Node: c.node}
	} else {
		c.last = &flock.Hlc{WallMs: c.last.GetWallMs(), Counter: c.last.GetCounter() + 1, Node: c.node}
	}
	return proto.Clone(c.last).(*flock.Hlc)
}

// Observe moves the clock past a remote time, so this copy's next write is
// later than it. A remote time more than FlockMaxSkewMs ahead of local wall
// time is refused (false): the caller holds that write until local time
// catches up, and the clock does not move.
func (c *FlockClock) Observe(remote *flock.Hlc) bool {
	if remote.GetWallMs() > c.now()+FlockMaxSkewMs {
		return false
	}
	if FlockHlcCompare(remote, c.last) > 0 {
		c.last = &flock.Hlc{WallMs: remote.GetWallMs(), Counter: remote.GetCounter(), Node: c.node}
	}
	return true
}

// FlockHlcCompare orders two clock times by (wall_ms, counter, node); an
// unset time is the smallest.
func FlockHlcCompare(a, b *flock.Hlc) int {
	switch {
	case a.GetWallMs() != b.GetWallMs():
		return flockCmp(a.GetWallMs(), b.GetWallMs())
	case a.GetCounter() != b.GetCounter():
		return flockCmp(a.GetCounter(), b.GetCounter())
	}
	return strings.Compare(a.GetNode(), b.GetNode())
}

func flockHlcZero(a *flock.Hlc) bool {
	return a.GetWallMs() == 0 && a.GetCounter() == 0 && a.GetNode() == ""
}

func flockHlcMax(a, b *flock.Hlc) *flock.Hlc {
	if FlockHlcCompare(a, b) >= 0 {
		return flockHlcClone(a)
	}
	return flockHlcClone(b)
}

func flockHlcClone(a *flock.Hlc) *flock.Hlc {
	if flockHlcZero(a) {
		return nil
	}
	return proto.Clone(a).(*flock.Hlc)
}

func flockCmp[T int64 | uint64 | uint32 | int32 | string](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// FlockNewKey returns a new random entry key (128 bits, hex). flock creates
// every key; a caller never chooses one.
func FlockNewKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", protoerrors.NewErrGeneral(err, "flock: no randomness for a new key")
	}
	return hex.EncodeToString(b[:]), nil
}

// FlockMustRebuild reports whether a copy last heard from at lastSeenMs is
// absent too long to merge: it is wiped and rebuilt from its peers.
func FlockMustRebuild(lastSeenMs, nowMs int64) bool {
	return nowMs-lastSeenMs > FlockRebuildAfterMs
}

// FlockMessageTooOld reports whether replicated state sent at sent is too old
// to apply: a mark may already be purged everywhere.
func FlockMessageTooOld(sent *flock.Hlc, nowMs int64) bool {
	return nowMs-sent.GetWallMs() > FlockMaxMessageAgeMs
}

func flockReasonRank(r flock.DeleteReason) int {
	switch r {
	case flock.DeleteReason_DELETE_REASON_USER:
		return 3
	case flock.DeleteReason_DELETE_REASON_MERGED:
		return 2
	case flock.DeleteReason_DELETE_REASON_EXPIRED:
		return 1
	}
	return 0
}

func flockMarked(m *flock.DeletionMark) bool {
	return flockReasonRank(m.GetReason()) > 0
}

// flockMarkCompare orders two marks, acks aside: > 0 when a wins. USER over
// MERGED over EXPIRED; for MERGED the oldest target, then the smaller target
// key; then the earliest mark.
func flockMarkCompare(a, b *flock.DeletionMark) int {
	if c := flockCmp(int64(flockReasonRank(a.GetReason())), int64(flockReasonRank(b.GetReason()))); c != 0 {
		return c
	}
	if c := FlockHlcCompare(b.GetMergedIntoCreated(), a.GetMergedIntoCreated()); c != 0 {
		return c
	}
	if c := strings.Compare(b.GetMergedInto(), a.GetMergedInto()); c != 0 {
		return c
	}
	return FlockHlcCompare(b.GetAt(), a.GetAt())
}

// flockMergeMark joins two marks: a mark wins over none, the larger mark over
// the smaller, and the acks of equal marks are united (each copy's earliest).
func flockMergeMark(a, b *flock.DeletionMark) *flock.DeletionMark {
	switch {
	case !flockMarked(a) && !flockMarked(b):
		return nil
	case !flockMarked(b):
		return flockMarkNorm(a, nil)
	case !flockMarked(a):
		return flockMarkNorm(b, nil)
	}
	switch c := flockMarkCompare(a, b); {
	case c > 0:
		return flockMarkNorm(a, nil)
	case c < 0:
		return flockMarkNorm(b, nil)
	}
	return flockMarkNorm(a, b.GetAcks())
}

func flockMarkNorm(m *flock.DeletionMark, more []*flock.MarkAck) *flock.DeletionMark {
	out := proto.Clone(m).(*flock.DeletionMark)
	byCopy := map[string]*flock.Hlc{}
	for _, a := range append(append([]*flock.MarkAck(nil), m.GetAcks()...), more...) {
		if at, ok := byCopy[a.GetCopy()]; !ok || FlockHlcCompare(a.GetAt(), at) < 0 {
			byCopy[a.GetCopy()] = a.GetAt()
		}
	}
	out.Acks = nil
	for c, at := range byCopy {
		out.Acks = append(out.Acks, &flock.MarkAck{Copy: c, At: flockHlcClone(at)})
	}
	sort.Slice(out.Acks, func(i, j int) bool { return out.Acks[i].GetCopy() < out.Acks[j].GetCopy() })
	return out
}

func flockMarkError(what, key string, m *flock.DeletionMark) error {
	if m.GetReason() == flock.DeleteReason_DELETE_REASON_MERGED {
		return protoerrors.NewErrNotFound("%%s %%s is merged into %%s", what, key, m.GetMergedInto())
	}
	return protoerrors.NewErrNotFound("%%s %%s is deleted (%%s)", what, key, m.GetReason())
}

func flockPurgeReady(m *flock.DeletionMark, members []string, nowMs int64) bool {
	if !flockMarked(m) {
		return false
	}
	acked := map[string]int64{}
	for _, a := range m.GetAcks() {
		acked[a.GetCopy()] = a.GetAt().GetWallMs()
	}
	var last int64
	for _, c := range members {
		at, ok := acked[c]
		if !ok {
			return false
		}
		if at > last {
			last = at
		}
	}
	return nowMs-last >= FlockMaxMessageAgeMs
}

func flockAck(m *flock.DeletionMark, copy string, at *flock.Hlc) *flock.DeletionMark {
	if !flockMarked(m) {
		return m
	}
	return flockMarkNorm(m, []*flock.MarkAck{{Copy: copy, At: flockHlcClone(at)}})
}

// flockMeta is an entry's merge metadata, sorted and safe to read when nil.
func flockMetaClone(m *flock.Meta) *flock.Meta {
	if m == nil {
		return &flock.Meta{}
	}
	return proto.Clone(m).(*flock.Meta)
}

func flockStamp(m *flock.Meta, path string) *flock.Hlc {
	for _, s := range m.GetStamps() {
		if s.GetPath() == path {
			return s.GetAt()
		}
	}
	return nil
}

func flockSetStamp(m *flock.Meta, path string, at *flock.Hlc) {
	for _, s := range m.Stamps {
		if s.GetPath() == path {
			s.At = flockHlcMax(s.GetAt(), at)
			return
		}
	}
	m.Stamps = append(m.Stamps, &flock.FieldStamp{Path: path, At: flockHlcClone(at)})
	sort.Slice(m.Stamps, func(i, j int) bool { return m.Stamps[i].GetPath() < m.Stamps[j].GetPath() })
}

func flockAddConflict(m *flock.Meta, path string) {
	for _, c := range m.Conflicts {
		if c == path {
			return
		}
	}
	m.Conflicts = append(m.Conflicts, path)
	sort.Strings(m.Conflicts)
}

func flockOps(m *flock.Meta, path string) []*flock.SetOp {
	for _, s := range m.GetSets() {
		if s.GetPath() == path {
			return s.GetOps()
		}
	}
	return nil
}

func flockSortHlcs(h []*flock.Hlc) {
	sort.Slice(h, func(i, j int) bool { return FlockHlcCompare(h[i], h[j]) < 0 })
}

// flockPutOp adds op to path's op set, once per dot.
func flockPutOp(m *flock.Meta, path string, op *flock.SetOp) {
	var set *flock.FieldOps
	for _, s := range m.Sets {
		if s.GetPath() == path {
			set = s
		}
	}
	if set == nil {
		set = &flock.FieldOps{Path: path}
		m.Sets = append(m.Sets, set)
		sort.Slice(m.Sets, func(i, j int) bool { return m.Sets[i].GetPath() < m.Sets[j].GetPath() })
	}
	for _, o := range set.Ops {
		if FlockHlcCompare(o.GetDot(), op.GetDot()) == 0 {
			return
		}
	}
	op = proto.Clone(op).(*flock.SetOp)
	flockSortHlcs(op.Observed)
	set.Ops = append(set.Ops, op)
	sort.Slice(set.Ops, func(i, j int) bool { return FlockHlcCompare(set.Ops[i].GetDot(), set.Ops[j].GetDot()) < 0 })
}

// flockMergeMeta joins two entries' metadata: each stamp the newer, the op
// sets united by dot, the conflicts united.
func flockMergeMeta(a, b *flock.Meta) *flock.Meta {
	out := &flock.Meta{}
	for _, s := range a.GetStamps() {
		flockSetStamp(out, s.GetPath(), s.GetAt())
	}
	for _, s := range a.GetSets() {
		for _, op := range s.GetOps() {
			flockPutOp(out, s.GetPath(), op)
		}
	}
	for _, c := range a.GetConflicts() {
		flockAddConflict(out, c)
	}
	for _, s := range b.GetStamps() {
		flockSetStamp(out, s.GetPath(), s.GetAt())
	}
	for _, s := range b.GetSets() {
		for _, op := range s.GetOps() {
			flockPutOp(out, s.GetPath(), op)
		}
	}
	for _, c := range b.GetConflicts() {
		flockAddConflict(out, c)
	}
	return out
}

// flockLive returns the live add ops of path, by element, each list in dot
// order. An add is live under ADD_WINS unless a remove observed it; under
// REMOVE_WINS only if it observed every remove of its element.
func flockLive(m *flock.Meta, path string, removeWins bool) (map[string][]*flock.SetOp, []string) {
	ops := flockOps(m, path)
	removes := map[string][]*flock.SetOp{}
	for _, op := range ops {
		if op.GetRemove() {
			removes[op.GetElement()] = append(removes[op.GetElement()], op)
		}
	}
	live := map[string][]*flock.SetOp{}
	for _, op := range ops {
		if op.GetRemove() {
			continue
		}
		alive := true
		for _, r := range removes[op.GetElement()] {
			if removeWins && !flockSaw(op, r.GetDot()) || !removeWins && flockSaw(r, op.GetDot()) {
				alive = false
				break
			}
		}
		if alive {
			live[op.GetElement()] = append(live[op.GetElement()], op)
		}
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return live, ids
}

func flockSaw(op *flock.SetOp, dot *flock.Hlc) bool {
	for _, o := range op.GetObserved() {
		if FlockHlcCompare(o, dot) == 0 {
			return true
		}
	}
	return false
}

// flockAddOp records an add of element at at: it observes every remove of the
// element so far.
func flockAddOp(m *flock.Meta, path, element string, payload []byte, at *flock.Hlc) {
	op := &flock.SetOp{Element: element, Dot: flockHlcClone(at), Payload: payload}
	for _, o := range flockOps(m, path) {
		if o.GetRemove() && o.GetElement() == element {
			op.Observed = append(op.Observed, flockHlcClone(o.GetDot()))
		}
	}
	flockPutOp(m, path, op)
}

// flockRemoveOp records a remove of element at at: it observes every add of
// the element so far.
func flockRemoveOp(m *flock.Meta, path, element string, at *flock.Hlc) {
	op := &flock.SetOp{Element: element, Remove: true, Dot: flockHlcClone(at)}
	for _, o := range flockOps(m, path) {
		if !o.GetRemove() && o.GetElement() == element {
			op.Observed = append(op.Observed, flockHlcClone(o.GetDot()))
		}
	}
	flockPutOp(m, path, op)
}

// flockBornWith reports whether path holds an add of element made at the
// entry's creation.
func flockBornWith(m *flock.Meta, path, element string, created *flock.Hlc) bool {
	for _, o := range flockOps(m, path) {
		if !o.GetRemove() && o.GetElement() == element && FlockHlcCompare(o.GetDot(), created) == 0 {
			return true
		}
	}
	return false
}

// The canonical element encoding: each identity value as
// "<length>:<text>", where text is a string as is, bytes in lowercase hex,
// an integer or enum number in decimal, a bool "true" or "false".
func flockIDString(s string) string { return strconv.Itoa(len(s)) + ":" + s }
func flockIDBytes(b []byte) string  { return flockIDString(hex.EncodeToString(b)) }
func flockIDInt(v int64) string     { return flockIDString(strconv.FormatInt(v, 10)) }
func flockIDUint(v uint64) string   { return flockIDString(strconv.FormatUint(v, 10)) }
func flockIDBool(v bool) string     { return flockIDString(strconv.FormatBool(v)) }

func flockMarshal(m proto.Message) []byte {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func flockMinNonZero[T int64 | uint64 | int32 | uint32 | string](a, b T) T {
	var zero T
	switch {
	case a == zero:
		return b
	case b == zero:
		return a
	case b < a:
		return b
	}
	return a
}

func flockMaxNonZero[T int64 | uint64 | int32 | uint32 | string](a, b T) T {
	var zero T
	switch {
	case a == zero:
		return b
	case b == zero:
		return a
	case b > a:
		return b
	}
	return a
}

func flockDigest(m proto.Message) []byte {
	sum := sha256.Sum256(flockMarshal(m))
	return sum[:]
}
`
