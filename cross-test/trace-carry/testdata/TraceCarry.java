// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tracecarry;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;

import com.gitlab.example.tacplus.trace.tracecarry.model.chassis.diskstore.RedoLog;
import com.gitlab.example.tacplus.trace.tracecarry.model.chassis.diskstore.Snapshot;
import com.gitlab.example.tacplus.trace.tracecarry.model.chassis.diskstore.StorePath;
import com.gitlab.example.tacplus.trace.tracecarry.model.chassis.diskstore.stores.DiskStores;
import com.gitlab.example.tacplus.trace.tracecarry.model.types.trace.tracecarry.ReadingProjections;
import com.gitlab.example.tacplus.trace.tracecarry.model.types.trace.tracecarry.Tracecarry.Note;
import com.gitlab.example.tacplus.trace.tracecarry.model.types.trace.tracecarry.Tracecarry.Reading;
import com.gitlab.example.tacplus.trace.tracecarry.model.types.trace.tracecarry.Tracecarry.ReadingRow;

/**
 * Java leg of the trace-carry gate (tacplus#313). Drives the GENERATED
 * extraction, record dispatch and disk helpers:
 * <ul>
 *   <li>a projection carries its source's trace_id;</li>
 *   <li>the row the dispatch hands its store has field 200 empty, read back raw
 *       from the bytes the store would hold;</li>
 *   <li>a write through the generated disk helpers (snapshot and redo record),
 *       then a raw read of what the store holds, carries no trace.</li>
 * </ul>
 * Prints one line {@code trace-carry java: <n> cases}.
 */
public final class TraceCarry {
    private static final String TRACE_ID = "trace-carry-313";

    private TraceCarry() {}

    private static void check(boolean ok, String msg) {
        if (!ok) {
            System.err.println("trace-carry java: " + msg);
            System.exit(1);
        }
    }

    /** Fails when raw stored JSON names a trace or holds the trace value. */
    private static void assertNoTrace(String what, String raw) {
        check(!raw.contains("traceId") && !raw.contains("trace_id") && !raw.contains(TRACE_ID),
                what + " carries the trace: " + raw);
    }

    /** True when raw wire bytes hold field 200 (tag 200 << 3 | wire type). */
    private static boolean hasField200(byte[] b) throws Exception {
        com.google.protobuf.CodedInputStream in = com.google.protobuf.CodedInputStream.newInstance(b);
        while (!in.isAtEnd()) {
            int tag = in.readTag();
            if (com.google.protobuf.WireFormat.getTagFieldNumber(tag) == 200) {
                return true;
            }
            in.skipField(tag);
        }
        return false;
    }

    public static void main(String[] args) throws Exception {
        Reading src = Reading.newBuilder()
                .setReadingId("r-1").setObservedAtMs(1700000000000L).setValue(2.5).setTraceId(TRACE_ID).build();

        ReadingRow row = ReadingProjections.extractReadingRowFromReading(src);
        check(TRACE_ID.equals(row.getTraceId()),
                "projection trace_id = \"" + row.getTraceId() + "\", want the source's");
        check(TRACE_ID.equals(ReadingProjections.readingToProjections(src).readingRow.getTraceId()),
                "readingToProjections dropped the trace");

        List<byte[]> stored = new ArrayList<>();
        ReadingProjections.handleReading(src, r -> stored.add(r.toByteArray()));
        check(stored.size() == 1, "dispatch stored " + stored.size() + " rows, want 1");
        check(!hasField200(stored.get(0)), "the stored row carries field 200");
        check(TRACE_ID.equals(src.getTraceId()), "storing a projection must not clear the source's trace");

        Path base = Files.createTempDirectory("trace-carry-java");
        Note note = Note.newBuilder().setNoteId("n-1").setBody("kept").setTraceId(TRACE_ID).build();
        DiskStores.saveNote(base, "ns", note, Snapshot.Opts.defaults());
        String raw = new String(Files.readAllBytes(StorePath.storePath(base, "ns", "note", ".snapshot")),
                StandardCharsets.UTF_8);
        assertNoTrace("the snapshot", raw);
        Note loaded = DiskStores.loadNote(base, "ns");
        check(loaded != null && loaded.getTraceId().isEmpty() && loaded.getBody().equals("kept"),
                "loadNote = " + loaded + "; want the note back without a trace");
        RedoLog.Record rec = DiskStores.recordNote(note);
        assertNoTrace("the redo record", rec.payload);
        check(TRACE_ID.equals(note.getTraceId()), "storing a value must not clear the caller's trace");

        System.out.println("trace-carry java: 4 cases");
    }
}
