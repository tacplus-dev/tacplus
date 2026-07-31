// Package oracle holds the deterministic invariant oracles that drive the
// harness verdict (archive/MODEL_STRESS_TEST_PLAN.md §2). Randomness drives the
// fault schedule; these oracles decide pass/fail. A pure "it returned and
// didn't error" check would pass green even with dropped deltas or fail-open
// auth — these oracles exist precisely to catch those.
package oracle

import (
	"fmt"

	"gitlab.com/secondstarcavalier/schematic/tacplus/cross-test/chaos/node"
)

// Verdict is the result of an oracle evaluation.
type Verdict struct {
	Name       string
	Pass       bool
	Violations []string
}

// Fail records a violation and marks the verdict red.
func (v *Verdict) Fail(format string, args ...any) {
	v.Pass = false
	v.Violations = append(v.Violations, fmt.Sprintf(format, args...))
}

// WatchOracle verifies the highest-value invariant: a WATCH stream's observed
// revisions must be gapless, strictly ordered, and duplicate-free, with correct
// snapshot->delta framing (docs/API.md WATCH contract). This is the oracle the
// COLL-1 dropped-delta bug must trip.
//
// It consumes the events a watcher actually observed plus the authoritative
// highest committed revision (from the state source) and the snapshot-done
// revision, and asserts:
//
//   - framing: snapshot_item* then exactly one snapshot_done, then delta*;
//   - ordering: delta revisions strictly increase;
//   - no-dup: no revision delivered twice;
//   - gapless: every revision in (snapshotDone, highestCommitted] is delivered.
type WatchOracle struct {
	HighestCommitted int64
}

// Evaluate runs the WATCH invariant over the observed event stream.
func (o WatchOracle) Evaluate(observed []node.Event) Verdict {
	v := Verdict{Name: "watch_gapless_ordered_no_dup", Pass: true}

	var snapshotDone int64 = -1
	sawDone := false
	inDeltaPhase := false
	var lastDelta int64
	seen := make(map[int64]bool)
	var maxDelivered int64

	for i, e := range observed {
		switch e.Kind {
		case node.SnapshotItem:
			if sawDone {
				v.Fail("snapshot_item at index %d after snapshot_done (bad framing)", i)
			}
		case node.SnapshotDone:
			if sawDone {
				v.Fail("duplicate snapshot_done at index %d", i)
			}
			sawDone = true
			snapshotDone = e.Revision
			maxDelivered = e.Revision
			inDeltaPhase = true
		case node.Delta:
			if !inDeltaPhase {
				v.Fail("delta at index %d before snapshot_done (bad framing)", i)
			}
			if seen[e.Revision] {
				v.Fail("duplicate delta revision %d at index %d", e.Revision, i)
			}
			seen[e.Revision] = true
			if e.Revision <= lastDelta {
				v.Fail("out-of-order delta revision %d (previous %d) at index %d", e.Revision, lastDelta, i)
			}
			lastDelta = e.Revision
			if e.Revision > maxDelivered {
				maxDelivered = e.Revision
			}
		}
	}

	if !sawDone {
		v.Fail("stream never emitted snapshot_done")
		return v
	}

	// Gapless: every revision after the snapshot up to the highest committed
	// must have been delivered. A silently dropped delta (COLL-1) leaves a hole.
	for r := snapshotDone + 1; r <= o.HighestCommitted; r++ {
		if !seen[r] {
			v.Fail("missing (dropped) delta revision %d: stream is not gapless", r)
		}
	}

	return v
}

// AuthOracle is the negative oracle: callers that MUST be denied. Fail-open is
// caught only as an expected-deny that succeeded (§2). Each Case records the
// outcome of attempting a call that should be rejected.
type AuthOracle struct{}

// AuthCase is one negative attempt and whether it was (correctly) denied.
type AuthCase struct {
	Name   string
	Denied bool // true if the call was rejected as expected
}

// Evaluate fails if any expected-deny case was allowed (fail-open).
func (AuthOracle) Evaluate(cases []AuthCase) Verdict {
	v := Verdict{Name: "auth_fail_closed", Pass: true}
	if len(cases) == 0 {
		v.Fail("no negative auth cases exercised: fail-open cannot be detected")
		return v
	}
	for _, c := range cases {
		if !c.Denied {
			v.Fail("expected-deny case %q succeeded (FAIL-OPEN)", c.Name)
		}
	}
	return v
}

// LeakOracle asserts resource counters stay flat across soak cycles. A leak
// shows only as a trend, never a snapshot (§5.3): it compares the post-recovery
// resource scrape of each cycle against the baseline.
type LeakOracle struct {
	Baseline node.Resources
}

// Evaluate fails if any cycle's post-recovery scrape exceeds the baseline.
func (o LeakOracle) Evaluate(perCycle []node.Resources) Verdict {
	v := Verdict{Name: "no_resource_leak", Pass: true}
	for i, r := range perCycle {
		if r.Goroutines > o.Baseline.Goroutines {
			v.Fail("cycle %d: goroutines %d exceed baseline %d (leak)", i, r.Goroutines, o.Baseline.Goroutines)
		}
		if r.OpenFDs > o.Baseline.OpenFDs {
			v.Fail("cycle %d: open FDs %d exceed baseline %d (leak)", i, r.OpenFDs, o.Baseline.OpenFDs)
		}
	}
	return v
}

// UnaryOracle verifies the echo-with-transform contract for S2S/C2S: each
// response must equal the value recomputed from the request seed/seq.
type UnaryOracle struct{}

// UnaryCase records one unary call's observed vs expected result.
type UnaryCase struct {
	Pair     string // "go->rust" etc.
	Seq      int64
	Expected string
	Observed string
	Err      error
}

// Evaluate fails on any mismatch or unexpected error.
func (UnaryOracle) Evaluate(cases []UnaryCase) Verdict {
	v := Verdict{Name: "unary_interop_echo_transform", Pass: true}
	for _, c := range cases {
		if c.Err != nil {
			v.Fail("%s seq=%d: unexpected error: %v", c.Pair, c.Seq, c.Err)
			continue
		}
		if c.Observed != c.Expected {
			v.Fail("%s seq=%d: got %q want %q (validation/binding drift)", c.Pair, c.Seq, c.Observed, c.Expected)
		}
	}
	return v
}
