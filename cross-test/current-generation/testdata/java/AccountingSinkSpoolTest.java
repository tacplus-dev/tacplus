// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the accounting-sink fixture (issue #34). NOT generated: run.sh
// copies this file into the generated chassis/am package and runs it as a main
// class.
//
// What it pins: with the event-bus publisher blocked (nothing drains the queue),
// AccountingManager.EventBusSink.emit never holds the calling RPC. Fifty
// concurrent completions each return within a bound. Records emitted during the
// outage are spooled and reach the publisher, in order, once it resumes. A spool
// that is full drops the overflow rather than blocking.

package com.gitlab.example.dummy.agentsensor.model.chassis.am;

import com.gitlab.example.dummy.agentsensor.model.types.sys.accounting.Accounting;
import com.gitlab.example.dummy.agentsensor.model.types.vservices.accountingrecord.Accountingrecord;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.TimeUnit;

public final class AccountingSinkSpoolTest {
    // EMIT_BOUND_MS is how long an RPC completion may take to hand its record to the sink.
    private static final long EMIT_BOUND_MS = 1000;

    private static Accounting.Event event(int i) {
        return Accounting.Event.newBuilder()
                .setRequestId("r" + i)
                .setSourceId("test")
                .setTimestampMs(1)
                .build();
    }

    private static void check(boolean ok, String message) {
        if (!ok) {
            throw new AssertionError(message);
        }
    }

    // emitAll runs n emits on one thread and checks they all return within the bound.
    // A blocked emit leaves the thread parked, which is the failure this gate exists
    // to catch.
    private static void emitAll(AccountingManager.EventBusSink sink, int n) throws InterruptedException {
        Thread emitter = new Thread(() -> {
            for (int i = 0; i < n; i++) {
                sink.emit(event(i));
            }
        }, "emitter");
        emitter.setDaemon(true);
        emitter.start();
        emitter.join(EMIT_BOUND_MS);
        check(!emitter.isAlive(), n + " emits did not return within " + EMIT_BOUND_MS + " ms while the publisher was blocked");
    }

    // The publisher is blocked: nothing drains queue. Fifty concurrent completions
    // must each return within the bound. Before the spool, the second emit parked
    // its handler thread.
    private static void checkRpcNeverHeldWhenPublisherBlocked() throws InterruptedException {
        BlockingQueue<Accountingrecord.AccountingRecord> queue = new ArrayBlockingQueue<>(1);
        AccountingManager.EventBusSink sink = new AccountingManager.EventBusSink(queue, "test-1", () -> false);
        List<Thread> rpcs = new ArrayList<>();
        for (int i = 0; i < 50; i++) {
            Thread rpc = new Thread(() -> sink.emit(event(0)), "rpc-" + i);
            rpc.setDaemon(true);
            rpcs.add(rpc);
            rpc.start();
        }
        for (Thread rpc : rpcs) {
            rpc.join(EMIT_BOUND_MS);
            check(!rpc.isAlive(), "an RPC completion did not return within " + EMIT_BOUND_MS + " ms while the publisher was blocked");
        }
    }

    // The publisher is blocked while ten records are emitted, then resumes. Every
    // record arrives, in emit order.
    private static void checkSpoolReplaysInOrderAfterOutage() throws InterruptedException {
        BlockingQueue<Accountingrecord.AccountingRecord> queue = new ArrayBlockingQueue<>(1);
        AccountingManager.EventBusSink sink = new AccountingManager.EventBusSink(queue, "test-2", () -> false);
        emitAll(sink, 10);
        for (int i = 0; i < 10; i++) {
            Accountingrecord.AccountingRecord rec = queue.poll(EMIT_BOUND_MS, TimeUnit.MILLISECONDS);
            check(rec != null, "record " + i + " did not reach the publisher after it resumed");
            check(rec.getRequestId().equals("r" + i),
                    "record " + i + " arrived as " + rec.getRequestId() + ": the spool did not replay in order");
        }
    }

    // The publisher is blocked and the spool depth is exceeded. Records are dropped,
    // never held, and every record is either delivered or dropped. The queue and the
    // drain thread hold a few records in flight, so the delivered count is checked
    // against that window rather than an exact figure.
    private static void checkDropsOnlyWhenSpoolFull() throws InterruptedException {
        BlockingQueue<Accountingrecord.AccountingRecord> queue = new ArrayBlockingQueue<>(1);
        AccountingManager.EventBusSink sink = new AccountingManager.EventBusSink(queue, "test-3", () -> false);
        int emitted = AccountingManager.EventBusSink.SPOOL_DEPTH + 100;
        emitAll(sink, emitted);

        int delivered = 0;
        while (queue.poll(EMIT_BOUND_MS, TimeUnit.MILLISECONDS) != null) {
            delivered++;
        }
        // One record sits in the queue, one in the drain thread's hand, and up to
        // SPOOL_DEPTH in the spool.
        check(delivered >= AccountingManager.EventBusSink.SPOOL_DEPTH
                        && delivered <= AccountingManager.EventBusSink.SPOOL_DEPTH + 2
                        && delivered < emitted,
                "delivered " + delivered + " of " + emitted + " records; want between "
                        + AccountingManager.EventBusSink.SPOOL_DEPTH + " and "
                        + (AccountingManager.EventBusSink.SPOOL_DEPTH + 2) + ", and fewer than emitted");
    }

    public static void main(String[] args) throws InterruptedException {
        // The spool-full checks drop records on purpose, and each drop logs at ERROR.
        // The checks read the queue, not the log, so silence the accounting logger
        // to keep the gate's clean-log rule meaningful for the rest of the build.
        ((ch.qos.logback.classic.Logger) org.slf4j.LoggerFactory.getLogger("accounting"))
                .setLevel(ch.qos.logback.classic.Level.OFF);
        checkRpcNeverHeldWhenPublisherBlocked();
        checkSpoolReplaysInOrderAfterOutage();
        checkDropsOnlyWhenSpoolFull();
        System.out.println("AccountingSinkSpoolTest: all checks passed");
    }
}
