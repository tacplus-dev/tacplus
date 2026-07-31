package node

import "testing"

// collectWatch drains a watcher interleaved with writes and returns everything
// it observed, relisting on OUT_OF_RANGE.
func collectWatch(st *StateSource, writes int) []Event {
	ws := st.NewWatchStream(0)
	var observed []Event
	drain := func() {
		for {
			evs, err := ws.Poll()
			if err != nil {
				ws.Relist()
				evs, _ = ws.Poll()
			}
			if len(evs) == 0 {
				return
			}
			observed = append(observed, evs...)
		}
	}
	drain()
	for i := 0; i < writes; i++ {
		st.Commit("k")
		drain()
	}
	drain()
	return observed
}

func TestStateSourceGaplessWhenHealthy(t *testing.T) {
	st := NewStateSource(StateConfig{ReplayDepth: 64})
	observed := collectWatch(st, 10)

	var deltas []int64
	sawDone := false
	for _, e := range observed {
		switch e.Kind {
		case SnapshotDone:
			sawDone = true
		case Delta:
			deltas = append(deltas, e.Revision)
		}
	}
	if !sawDone {
		t.Fatal("never saw snapshot_done")
	}
	if len(deltas) != 10 {
		t.Fatalf("expected 10 gapless deltas, got %d: %v", len(deltas), deltas)
	}
	for i, r := range deltas {
		if r != int64(i+1) {
			t.Fatalf("delta %d had revision %d, want %d (gap)", i, r, i+1)
		}
	}
}

func TestStateSourceDropDeltaBugLeavesGap(t *testing.T) {
	st := NewStateSource(StateConfig{ReplayDepth: 64, DropDeltaBug: true, DropEvery: 3})
	collectWatch(st, 10)
	dropped := st.DroppedRevisions()
	if len(dropped) == 0 {
		t.Fatal("expected the COLL-1 bug to drop at least one revision")
	}
	// Revision still advanced past the dropped ones.
	if st.Revision() != 10 {
		t.Fatalf("revision should still reach 10, got %d", st.Revision())
	}
}

func TestDeltasSinceOutOfRange(t *testing.T) {
	st := NewStateSource(StateConfig{ReplayDepth: 2})
	for i := 0; i < 5; i++ {
		st.Commit("k")
	}
	// Ring holds only the last 2; asking from revision 0 must OUT_OF_RANGE.
	_, err := st.DeltasSince(0)
	if _, ok := err.(*ErrOutOfRange); !ok {
		t.Fatalf("expected ErrOutOfRange, got %v", err)
	}
}
