// Package node models a synthetic tacplus-generated "model" node for the
// Profile A chaos harness. It is consumer-agnostic: it stands in the minimal
// rivets the generated model requires (an in-memory monotonic state source and
// a test token/JWK signer) rather than any concrete consumer backing service.
//
// The point of the harness is that the *generated model machinery* (the WATCH
// replay ring, snapshot->delta framing, relist, auth) does the work; the
// business logic here is a thin deterministic stub. This package therefore
// re-implements the observable contract of that machinery in-process so the
// oracles have something real to verify and the COLL-1 dropped-delta bug can be
// re-introduced behind a flag (see StateConfig.DropDeltaBug).
package node

import (
	"fmt"
	"sort"
	"sync"
)

// EventKind classifies a WATCH envelope entry, mirroring the generated WATCH
// contract documented in docs/API.md:
//
//   - SnapshotItem entries establish the initial state;
//   - SnapshotDone carries the revision at which that snapshot is complete;
//   - Delta entries carry later changes.
type EventKind int

const (
	// SnapshotItem is one item of the initial materialized snapshot.
	SnapshotItem EventKind = iota
	// SnapshotDone marks the revision at which the snapshot is complete.
	SnapshotDone
	// Delta is a post-snapshot change at a specific revision.
	Delta
)

func (k EventKind) String() string {
	switch k {
	case SnapshotItem:
		return "snapshot_item"
	case SnapshotDone:
		return "snapshot_done"
	case Delta:
		return "delta"
	default:
		return fmt.Sprintf("EventKind(%d)", int(k))
	}
}

// Event is a single entry in a WATCH stream envelope.
type Event struct {
	Kind     EventKind
	Revision int64  // monotonic per-scope revision; 0 for snapshot items
	Key      string // collection key the change applies to
	Value    string // deterministic value (echo-with-transform of revision+key)
}

// ErrOutOfRange is the in-process analogue of the gRPC OUT_OF_RANGE status the
// generated WATCH wrapper returns when from_revision predates the replay ring.
// It prompts the client to relist (re-issue a snapshot read).
type ErrOutOfRange struct {
	FromRevision int64
	Floor        int64
}

func (e *ErrOutOfRange) Error() string {
	return fmt.Sprintf("OUT_OF_RANGE: from_revision %d predates replay ring floor %d", e.FromRevision, e.Floor)
}

// ringEntry is one durable change in the per-scope replay ring.
type ringEntry struct {
	revision int64
	key      string
	value    string
}

// StateConfig parameterizes the in-memory state source / collection runtime.
type StateConfig struct {
	// ReplayDepth bounds the per-scope replay ring (replay_depth in the model).
	ReplayDepth int

	// DropDeltaBug re-introduces the COLL-1 dropped-delta defect: when set, the
	// state source silently fails to append every Nth change to the replay ring
	// while still advancing the revision counter. A correct WATCH stream is
	// gapless; this produces a gap that the WATCH oracle must catch. This is the
	// Phase-1 acceptance lever (docs/MODEL_STRESS_TEST_PLAN.md §9.1): with the
	// bug on, the harness must go RED.
	DropDeltaBug bool

	// DropEvery selects which change is dropped when DropDeltaBug is set. The
	// Nth committed change (1-based) is dropped from the ring. Defaults to 3.
	DropEvery int
}

func (c StateConfig) withDefaults() StateConfig {
	if c.ReplayDepth <= 0 {
		c.ReplayDepth = 1024
	}
	if c.DropEvery <= 0 {
		c.DropEvery = 3
	}
	return c
}

// StateSource is the in-memory, deterministic state machine that backs the
// WATCH/SNAPSHOT/PAGE collection. It owns a monotonic per-scope revision log
// and a bounded replay ring, mirroring the generated collection runtime. It is
// safe for concurrent use.
type StateSource struct {
	cfg StateConfig

	mu        sync.RWMutex
	revision  int64              // latest committed revision (monotonic)
	committed int64              // count of Commit calls (for DropEvery)
	current   map[string]string  // materialized current state (key -> value)
	ring      []ringEntry        // replay ring, oldest-first, bounded
	dropped   map[int64]struct{} // revisions intentionally dropped (bug telemetry)
}

// NewStateSource builds a state source from the given config.
func NewStateSource(cfg StateConfig) *StateSource {
	cfg = cfg.withDefaults()
	return &StateSource{
		cfg:     cfg,
		current: make(map[string]string),
		dropped: make(map[int64]struct{}),
	}
}

// transform is the deterministic echo-with-transform used everywhere so the
// oracle can recompute the expected value from (revision, key) alone. Catches
// validation drift / wrong-bytes binding (docs/MODEL_STRESS_TEST_PLAN.md §2).
func transform(revision int64, key string) string {
	return fmt.Sprintf("%s@r%d", key, revision)
}

// Commit advances the revision log by writing a change for key. It returns the
// new revision. Under the COLL-1 bug, the corresponding entry may be silently
// omitted from the replay ring while the revision still advances.
func (s *StateSource) Commit(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.revision++
	s.committed++
	rev := s.revision
	val := transform(rev, key)
	s.current[key] = val

	drop := s.cfg.DropDeltaBug && s.committed%int64(s.cfg.DropEvery) == 0
	if drop {
		// COLL-1: revision advanced, but the change never lands in the ring.
		s.dropped[rev] = struct{}{}
		return rev
	}

	s.ring = append(s.ring, ringEntry{revision: rev, key: key, value: val})
	if len(s.ring) > s.cfg.ReplayDepth {
		s.ring = s.ring[len(s.ring)-s.cfg.ReplayDepth:]
	}
	return rev
}

// Revision returns the latest committed revision.
func (s *StateSource) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

// DroppedRevisions returns the set of revisions the bug silently dropped. Test
// telemetry only; a real client cannot see this.
func (s *StateSource) DroppedRevisions() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]int64, 0, len(s.dropped))
	for r := range s.dropped {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Snapshot returns the materialized current state plus the revision at which it
// is complete (the snapshot_done revision). This is the SNAPSHOT read and the
// initial phase of a WATCH stream.
func (s *StateSource) Snapshot() (items []Event, doneRevision int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.current))
	for k := range s.current {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	items = make([]Event, 0, len(keys))
	for _, k := range keys {
		items = append(items, Event{Kind: SnapshotItem, Key: k, Value: s.current[k]})
	}
	return items, s.revision
}

// DeltasSince returns the ordered replay-ring changes strictly after
// fromRevision. If fromRevision predates the ring floor it returns
// *ErrOutOfRange so the client relists, exactly like the generated wrapper.
func (s *StateSource) DeltasSince(fromRevision int64) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.ring) > 0 {
		floor := s.ring[0].revision - 1
		if fromRevision < floor {
			return nil, &ErrOutOfRange{FromRevision: fromRevision, Floor: floor}
		}
	}
	out := make([]Event, 0)
	for _, e := range s.ring {
		if e.revision > fromRevision {
			out = append(out, Event{Kind: Delta, Revision: e.revision, Key: e.key, Value: e.value})
		}
	}
	return out, nil
}
