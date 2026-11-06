// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The go leg of the trace-carry gate (issue #313). Copied into the generated
// tracecarry package by cross-test/trace-carry/run.sh, so it drives the
// GENERATED extraction and record dispatch:
//   - a projection carries its source's trace_id;
//   - the row the dispatch hands its store has field 200 empty, read back
//     raw from the bytes the store would hold.
package tracecarry

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

const traceID = "trace-carry-313"

// hasField200 reads raw wire bytes for field 200, whatever its type.
func hasField200(t *testing.T, b []byte) bool {
	t.Helper()
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatalf("bad tag: %v", protowire.ParseError(n))
		}
		if num == 200 {
			return true
		}
		b = b[n:]
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			t.Fatalf("bad field %d: %v", num, protowire.ParseError(m))
		}
		b = b[m:]
	}
	return false
}

func TestTraceCarry(t *testing.T) {
	src := &Reading{ReadingId: "r-1", ObservedAtMs: 1700000000000, Value: 2.5, TraceId: traceID}

	row := ExtractReadingRowFromReading(src)
	if row.GetTraceId() != traceID {
		t.Fatalf("projection trace_id = %q, want the source's %q", row.GetTraceId(), traceID)
	}
	if p := ReadingToProjections(src); p.ReadingRow.GetTraceId() != traceID {
		t.Fatalf("ReadingToProjections trace_id = %q, want %q", p.ReadingRow.GetTraceId(), traceID)
	}

	var stored [][]byte
	err := HandleReading(context.Background(), src, func(_ context.Context, r *ReadingRow) error {
		b, err := proto.Marshal(r)
		stored = append(stored, b)
		return err
	})
	if err != nil {
		t.Fatalf("HandleReading: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("dispatch stored %d rows, want 1", len(stored))
	}
	if hasField200(t, stored[0]) {
		t.Fatal("the stored row carries field 200: a store path must clear the trace")
	}
	if src.GetTraceId() != traceID {
		t.Fatal("storing a projection must not clear the source's trace")
	}
	fmt.Println("trace-carry go: 2 cases")
}
