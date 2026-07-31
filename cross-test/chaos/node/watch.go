package node

import "fmt"

// WatchStream is the in-process analogue of the generated WATCH wrapper's
// server-streaming output: a snapshot phase (snapshot_item* then snapshot_done)
// followed by a live delta phase. A watcher consumes it and the WATCH oracle
// asserts the gapless/ordered/no-duplicate contract over the emitted events.
type WatchStream struct {
	src          *StateSource
	cursor       int64 // highest revision delivered to the client so far
	snapshotSent bool

	// partition, when true, simulates a network blackhole to the state store:
	// delta polls observe nothing even though revisions advance. Used by the
	// network-partition fault; the stream must resume gapless once healed.
	partition bool
}

// NewWatchStream opens a WATCH at fromRevision. fromRevision < 0 means "relist
// from the current snapshot"; otherwise it resumes from the replay ring.
func (s *StateSource) NewWatchStream(fromRevision int64) *WatchStream {
	return &WatchStream{src: s, cursor: fromRevision}
}

// SetPartitioned toggles the simulated blackhole between this watcher and the
// state store.
func (w *WatchStream) SetPartitioned(p bool) { w.partition = p }

// Poll advances the stream and returns any newly available envelope entries in
// order. The first successful Poll emits the snapshot phase; subsequent polls
// emit deltas. If the resume point predates the replay ring it returns
// *ErrOutOfRange (the client should relist via Relist).
func (w *WatchStream) Poll() ([]Event, error) {
	if w.partition {
		return nil, nil
	}

	var out []Event
	if !w.snapshotSent {
		items, done := w.src.Snapshot()
		out = append(out, items...)
		out = append(out, Event{Kind: SnapshotDone, Revision: done})
		w.snapshotSent = true
		if w.cursor < 0 || w.cursor > done {
			w.cursor = done
		}
		return out, nil
	}

	deltas, err := w.src.DeltasSince(w.cursor)
	if err != nil {
		return nil, err
	}
	for _, e := range deltas {
		out = append(out, e)
		if e.Revision > w.cursor {
			w.cursor = e.Revision
		}
	}
	return out, nil
}

// Relist resets the stream to re-issue a snapshot from the current state, as the
// client must do after OUT_OF_RANGE.
func (w *WatchStream) Relist() {
	w.snapshotSent = false
	w.cursor = -1
}

// Cursor reports the highest revision delivered to the client.
func (w *WatchStream) Cursor() int64 { return w.cursor }

// String renders the stream state for diagnostics.
func (w *WatchStream) String() string {
	return fmt.Sprintf("WatchStream(cursor=%d, snapshotSent=%v, partition=%v)", w.cursor, w.snapshotSent, w.partition)
}
