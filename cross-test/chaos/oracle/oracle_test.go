package oracle

import (
	"testing"

	"github.com/tacplus-dev/tacplus/cross-test/chaos/node"
)

func TestWatchOraclePassesOnGaplessStream(t *testing.T) {
	observed := []node.Event{
		{Kind: node.SnapshotItem, Key: "a", Value: "a@r0"},
		{Kind: node.SnapshotDone, Revision: 0},
		{Kind: node.Delta, Revision: 1, Key: "a"},
		{Kind: node.Delta, Revision: 2, Key: "b"},
		{Kind: node.Delta, Revision: 3, Key: "a"},
	}
	v := WatchOracle{HighestCommitted: 3}.Evaluate(observed)
	if !v.Pass {
		t.Fatalf("expected PASS, got violations: %v", v.Violations)
	}
}

func TestWatchOracleCatchesDroppedDelta(t *testing.T) {
	// Revision 2 was committed but never delivered: a COLL-1 gap.
	observed := []node.Event{
		{Kind: node.SnapshotDone, Revision: 0},
		{Kind: node.Delta, Revision: 1},
		{Kind: node.Delta, Revision: 3},
	}
	v := WatchOracle{HighestCommitted: 3}.Evaluate(observed)
	if v.Pass {
		t.Fatal("expected FAIL on dropped delta")
	}
}

func TestWatchOracleCatchesDuplicateAndOutOfOrder(t *testing.T) {
	dup := []node.Event{
		{Kind: node.SnapshotDone, Revision: 0},
		{Kind: node.Delta, Revision: 1},
		{Kind: node.Delta, Revision: 1},
	}
	if (WatchOracle{HighestCommitted: 1}).Evaluate(dup).Pass {
		t.Fatal("expected FAIL on duplicate delta")
	}
}

func TestAuthOracleCatchesFailOpen(t *testing.T) {
	cases := []AuthCase{
		{Name: "wrong-ns", Denied: true},
		{Name: "expired", Denied: false}, // fail-open!
	}
	if (AuthOracle{}).Evaluate(cases).Pass {
		t.Fatal("expected FAIL when an expected-deny case succeeded")
	}
	if !(AuthOracle{}).Evaluate([]AuthCase{{Name: "x", Denied: true}}).Pass {
		t.Fatal("expected PASS when all deny cases denied")
	}
}

func TestLeakOracleCatchesCreep(t *testing.T) {
	base := node.Resources{Goroutines: 0, OpenFDs: 0}
	trend := []node.Resources{{Goroutines: 0}, {Goroutines: 1}, {Goroutines: 2}}
	if (LeakOracle{Baseline: base}).Evaluate(trend).Pass {
		t.Fatal("expected FAIL on resource creep")
	}
	flat := []node.Resources{{}, {}, {}}
	if !(LeakOracle{Baseline: base}).Evaluate(flat).Pass {
		t.Fatal("expected PASS on flat resources")
	}
}
