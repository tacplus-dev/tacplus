// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the accounting-sink fixture (issue #34). NOT generated: run.sh
// copies this file into the generated chassis/am package before `go test ./...`.
//
// What it pins: with the event-bus publisher blocked (nothing reads the queue),
// EventBusSink.Emit never holds the calling RPC. N concurrent completions each
// return within a bound. Records emitted during the outage are spooled and reach
// the publisher, in order, once it resumes. A spool that is full drops and counts
// the overflow rather than blocking.

package am

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	accounting_pb "gitlab.com/example/dummy/agentsensor/tacplus/model/types/sys/accounting"
	accountingrecord_pb "gitlab.com/example/dummy/agentsensor/tacplus/model/types/vservices/accountingrecord"
)

// emitBound is how long an RPC completion may take to hand its record to the sink.
const emitBound = time.Second

func sinkEvent(i int) *accounting_pb.Event {
	return &accounting_pb.Event{RequestId: fmt.Sprintf("r%d", i), SourceId: "test", TimestampMs: 1}
}

// emitAll runs n emits on one goroutine and reports whether they all returned
// within emitBound. A blocked emit leaves the goroutine parked, which is the
// failure this gate exists to catch.
func emitAll(t *testing.T, sink *EventBusSink, n int) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			_ = sink.Emit(context.Background(), sinkEvent(i))
		}
	}()
	select {
	case <-done:
	case <-time.After(emitBound):
		t.Fatalf("%d emits did not return within %v while the publisher was blocked", n, emitBound)
	}
}

// TestEventBusSinkNeverHoldsRPCWhenPublisherBlocked: nothing reads tx, so the
// publisher is blocked. Fifty concurrent completions must all return within the
// bound. Before the spool, the second emit parked its handler goroutine.
func TestEventBusSinkNeverHoldsRPCWhenPublisherBlocked(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	tx := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink := NewEventBusSink(tx, "test-1", stop)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = sink.Emit(context.Background(), sinkEvent(i))
		}(i)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(emitBound):
		t.Fatalf("50 RPC completions did not return within %v while the publisher was blocked", emitBound)
	}
}

// TestEventBusSinkReplaysSpoolInOrderAfterOutage: the publisher is blocked while
// ten records are emitted, then resumes. Every record arrives, in emit order.
func TestEventBusSinkReplaysSpoolInOrderAfterOutage(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	tx := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink := NewEventBusSink(tx, "test-2", stop)

	emitAll(t, sink, 10)
	for i := 0; i < 10; i++ {
		select {
		case rec := <-tx:
			if want := fmt.Sprintf("r%d", i); rec.RequestId != want {
				t.Fatalf("record %d arrived as %q, want %q: the spool did not replay in order", i, rec.RequestId, want)
			}
		case <-time.After(emitBound):
			t.Fatalf("record %d did not reach the publisher after it resumed", i)
		}
	}
}

// TestEventBusSinkDropsAndCountsOnlyWhenSpoolFull: the publisher is blocked and
// the spool depth is exceeded. Records are dropped, never held, and every record
// is either delivered once the publisher resumes or dropped. The queue and the
// drain goroutine hold a few records in flight, so the delivered count is checked
// against that window rather than an exact figure.
func TestEventBusSinkDropsAndCountsOnlyWhenSpoolFull(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	tx := make(chan *accountingrecord_pb.AccountingRecord, 1)
	sink := NewEventBusSink(tx, "test-3", stop)

	const emitted = accountingSpoolDepth + 100
	emitAll(t, sink, emitted)
	dropped := int(sink.dropped.Load())
	if dropped == 0 {
		t.Fatalf("emitted %d records past a spool of %d with the publisher blocked, none were dropped", emitted, accountingSpoolDepth)
	}

	delivered := 0
	for drained := false; !drained; {
		select {
		case <-tx:
			delivered++
		case <-time.After(emitBound):
			drained = true
		}
	}
	// One record sits in tx, one in the drain goroutine's hand, and up to
	// accountingSpoolDepth in the spool.
	if delivered < accountingSpoolDepth || delivered > accountingSpoolDepth+2 {
		t.Fatalf("delivered %d of %d records; want between %d and %d", delivered, emitted, accountingSpoolDepth, accountingSpoolDepth+2)
	}
	if delivered+dropped != emitted {
		t.Fatalf("delivered %d + dropped %d != emitted %d: a record was lost without being counted", delivered, dropped, emitted)
	}
}
