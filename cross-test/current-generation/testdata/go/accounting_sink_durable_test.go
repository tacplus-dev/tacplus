// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the durable accounting spool (issue #70). NOT generated:
// run.sh copies this file into the generated chassis/am package before `go test
// ./...`.
//
// What it pins: with grpcconfig.disk_persistence set, the spool is an append-only
// log on disk. Records the publisher has not acknowledged survive a restart and
// replay in order; records it has acknowledged are not replayed; records emitted
// during a bus outage replay in order once it returns; and the log is bounded on
// disk, dropping and counting only past the bound.

package am

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	accountingrecord_pb "gitlab.com/example/dummy/agentsensor/tacplus/model/types/vservices/accountingrecord"
)

// durableSink opens a durable sink over dir with the given publisher queue.
func durableSink(t *testing.T, tx chan *accountingrecord_pb.AccountingRecord, stop <-chan struct{}, dir string) *EventBusSink {
	t.Helper()
	sink, err := NewDurableEventBusSink(tx, "test-durable", stop, dir)
	if err != nil {
		t.Fatalf("NewDurableEventBusSink: %v", err)
	}
	return sink
}

// receiveRequestIDs reads n records from tx, acknowledging each as a publisher
// does, and returns their request IDs in arrival order.
func receiveRequestIDs(t *testing.T, sink *EventBusSink, tx <-chan *accountingrecord_pb.AccountingRecord, n int) []string {
	t.Helper()
	var got []string
	for i := 0; i < n; i++ {
		select {
		case rec := <-tx:
			got = append(got, rec.RequestId)
			sink.Ack()
		case <-time.After(emitBound):
			t.Fatalf("record %d of %d did not reach the publisher; got %v", i, n, got)
		}
	}
	return got
}

// expectNoMore fails if a record arrives on tx within the bound.
func expectNoMore(t *testing.T, tx <-chan *accountingrecord_pb.AccountingRecord) {
	t.Helper()
	select {
	case rec := <-tx:
		t.Fatalf("record %q was replayed after it was acknowledged", rec.RequestId)
	case <-time.After(200 * time.Millisecond):
	}
}

// waitForCursor polls the spool cursor until it reads want. A sink writes its
// cursor on shutdown and at intervals, so a restart must wait for it.
func waitForCursor(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(emitBound)
	for {
		raw, _ := os.ReadFile(filepath.Join(dir, "accounting-spool", "cursor"))
		if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spool cursor did not reach %d (last read %q)", want, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDurableSpoolReplaysUnacknowledgedAfterRestart: the publisher acknowledges
// three of five records, then the process stops. A new sink over the same dir
// replays exactly the two unacknowledged records, in order.
func TestDurableSpoolReplaysUnacknowledgedAfterRestart(t *testing.T) {
	dir := t.TempDir()

	stop1 := make(chan struct{})
	tx1 := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink1 := durableSink(t, tx1, stop1, dir)
	emitAll(t, sink1, 5)
	if got := receiveRequestIDs(t, sink1, tx1, 3); strings.Join(got, ",") != "r0,r1,r2" {
		t.Fatalf("first run delivered %v, want r0,r1,r2", got)
	}
	close(stop1)
	waitForCursor(t, dir, 3)

	stop2 := make(chan struct{})
	defer close(stop2)
	tx2 := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink2 := durableSink(t, tx2, stop2, dir)
	if got := receiveRequestIDs(t, sink2, tx2, 2); strings.Join(got, ",") != "r3,r4" {
		t.Fatalf("restart replayed %v, want r3,r4 in order", got)
	}
	expectNoMore(t, tx2)
}

// TestDurableSpoolHoldsRPCWhenPublisherBlocked: nothing reads tx, so the
// publisher is blocked. Fifty completions must all return within the bound, as
// the memory spool guarantees, while the records go to disk.
func TestDurableSpoolHoldsRPCWhenPublisherBlocked(t *testing.T) {
	dir := t.TempDir()
	stop := make(chan struct{})
	tx := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink := durableSink(t, tx, stop, dir)
	emitAll(t, sink, 50)
	// The spool task appends after the RPCs return. Wait until all 50 are on disk, so
	// nothing writes into the test directory once the test ends.
	waitForLines(t, dir, 50)
	close(stop)
}

// waitForLines polls the spool log until it holds want lines.
func waitForLines(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(emitBound)
	log := filepath.Join(dir, "accounting-spool", "records.log")
	for {
		raw, _ := os.ReadFile(log)
		if bytes.Count(raw, []byte("\n")) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spool log did not reach %d records (has %d)", want, bytes.Count(raw, []byte("\n")))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDurableSpoolReplaysOutageInOrderAndCompacts: the publisher is blocked while
// ten records are emitted, then resumes. Every record arrives once, in emit
// order, and once all are acknowledged the log is removed.
func TestDurableSpoolReplaysOutageInOrderAndCompacts(t *testing.T) {
	dir := t.TempDir()
	stop := make(chan struct{})
	defer close(stop)
	tx := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink := durableSink(t, tx, stop, dir)

	emitAll(t, sink, 10)
	got := receiveRequestIDs(t, sink, tx, 10)
	for i, id := range got {
		if want := fmt.Sprintf("r%d", i); id != want {
			t.Fatalf("record %d arrived as %q, want %q: the spool did not replay in order", i, id, want)
		}
	}
	expectNoMore(t, tx)

	logPath := filepath.Join(dir, "accounting-spool", "records.log")
	deadline := time.Now().Add(emitBound)
	for {
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log %s was not removed after every record was acknowledged", logPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDurableSpoolDropsAndCountsOnlyPastDiskBound: the publisher is blocked and
// the disk bound is shrunk so a few records fit. Records past it are dropped and
// counted, and every record is either delivered or counted as dropped.
func TestDurableSpoolDropsAndCountsOnlyPastDiskBound(t *testing.T) {
	saved := accountingSpoolBytes
	accountingSpoolBytes = 1024
	defer func() { accountingSpoolBytes = saved }()

	stop := make(chan struct{})
	defer close(stop)
	tx := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink := durableSink(t, tx, stop, t.TempDir())

	const emitted = 40
	emitAll(t, sink, emitted)
	// Let the spool goroutine take everything in its queue before counting.
	deadline := time.Now().Add(emitBound)
	for len(sink.spool) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	dropped := int(sink.dropped.Load())
	if dropped == 0 {
		t.Fatalf("emitted %d records past a 1024-byte disk bound, none were dropped", emitted)
	}

	delivered := 0
	for drained := false; !drained; {
		select {
		case <-tx:
			delivered++
			sink.Ack()
		case <-time.After(emitBound):
			drained = true
		}
	}
	// The record in tx is counted as delivered once it is read, so the sum is exact.
	if delivered+dropped != emitted {
		t.Fatalf("delivered %d + dropped %d != emitted %d: a record was lost without being counted", delivered, dropped, emitted)
	}
}

// TestDurableSpoolRejectsCorruptLog: a log line that is not a record stops the
// sink from opening, rather than skipping a record and misnumbering the cursor.
func TestDurableSpoolRejectsCorruptLog(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "accounting-spool"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "accounting-spool", "records.log"), []byte("!!not base64!!\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	tx := make(chan *accountingrecord_pb.AccountingRecord, 1)
	if _, err := NewDurableEventBusSink(tx, "test-corrupt", make(chan struct{}), dir); err == nil {
		t.Fatalf("a corrupt spool log opened without error")
	}
}
