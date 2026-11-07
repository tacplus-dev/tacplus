// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The go leg of the trace-carry gate (issue #313), store half. Copied into the
// generated s2sm/diskstore package by cross-test/trace-carry/run.sh: a write
// through the GENERATED disk helpers (snapshot and redo record), then a raw read
// of what the store holds, which must carry no trace.
package diskstores

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	diskstore "gitlab.com/example/tacplus/trace/tracecarry/model/chassis/diskstore"
	tracecarry_pb "gitlab.com/example/tacplus/trace/tracecarry/model/types/trace/tracecarry"
)

const traceID = "trace-carry-313"

// assertNoTrace fails when raw stored JSON has a trace key or the trace value.
func assertNoTrace(t *testing.T, what string, raw []byte) {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s: not JSON: %v: %s", what, err, raw)
	}
	for _, k := range []string{"traceId", "trace_id"} {
		if _, ok := obj[k]; ok {
			t.Fatalf("%s holds %s: %s", what, k, raw)
		}
	}
	if strings.Contains(string(raw), traceID) {
		t.Fatalf("%s holds the trace: %s", what, raw)
	}
}

func TestTraceStore(t *testing.T) {
	base := t.TempDir()
	note := &tracecarry_pb.Note{NoteId: "n-1", Body: "kept", TraceId: traceID}

	if err := SaveNote(base, "ns", note, diskstore.SnapshotOpts{}); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	raw, err := os.ReadFile(diskstore.StorePath(base, "ns", "note", ".snapshot"))
	if err != nil {
		t.Fatalf("raw read of the snapshot: %v", err)
	}
	assertNoTrace(t, "the snapshot", raw)
	loaded, ok, err := LoadNote(base, "ns")
	if err != nil || !ok || loaded.GetTraceId() != "" || loaded.GetBody() != "kept" {
		t.Fatalf("LoadNote = %v, %v, %v; want the note back without a trace", loaded, ok, err)
	}

	rec, err := RecordNote(note)
	if err != nil {
		t.Fatalf("RecordNote: %v", err)
	}
	assertNoTrace(t, "the redo record", rec.Payload)

	if note.GetTraceId() != traceID {
		t.Fatal("storing a value must not clear the caller's trace")
	}
	fmt.Println("trace-carry go store: 2 cases")
}
