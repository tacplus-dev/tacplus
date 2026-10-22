// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the durable accounting spool (issue #70). NOT generated:
// run.sh copies this file into the generated chassis/am package and runs it as a
// main class.
//
// What it pins: with grpcconfig.disk_persistence set, the spool is an append-only
// log on disk. Records the publisher has not acknowledged survive a restart and
// replay in order; records it has acknowledged are not replayed; records emitted
// during a bus outage replay in order once it returns, and the log is removed once
// all are acknowledged; the log is bounded on disk, dropping and counting only past
// the bound; and a corrupt log stops the sink from opening.

package com.gitlab.example.dummy.agentsensor.model.chassis.am;

import com.gitlab.example.dummy.agentsensor.model.chassis.diskstore.DiskStoreException;
import com.gitlab.example.dummy.agentsensor.model.types.sys.accounting.Accounting;
import com.gitlab.example.dummy.agentsensor.model.types.vservices.accountingrecord.Accountingrecord;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

public final class AccountingSinkDurableTest {
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

    private static Path tempDir() throws Exception {
        return Files.createTempDirectory("accounting-spool-test");
    }

    private static AccountingManager.EventBusSink durable(BlockingQueue<Accountingrecord.AccountingRecord> queue,
                                                          Path dir, AtomicBoolean stop) {
        return AccountingManager.EventBusSink.durable(queue, "test-durable", stop::get, dir);
    }

    // emitAll runs n emits on one thread and checks they all return within the bound.
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

    // receive reads n records from the queue, acknowledging each as a publisher
    // does, and returns their request IDs in arrival order.
    private static List<String> receive(AccountingManager.EventBusSink sink,
                                        BlockingQueue<Accountingrecord.AccountingRecord> queue,
                                        int n) throws InterruptedException {
        List<String> got = new ArrayList<>();
        for (int i = 0; i < n; i++) {
            Accountingrecord.AccountingRecord rec = queue.poll(EMIT_BOUND_MS, TimeUnit.MILLISECONDS);
            check(rec != null, "record " + i + " of " + n + " did not reach the publisher; got " + got);
            got.add(rec.getRequestId());
            sink.ack();
        }
        return got;
    }

    // expectNoMore fails if a record arrives on the queue within a short bound.
    private static void expectNoMore(BlockingQueue<Accountingrecord.AccountingRecord> queue) throws InterruptedException {
        Accountingrecord.AccountingRecord rec = queue.poll(200, TimeUnit.MILLISECONDS);
        check(rec == null, "record " + (rec == null ? "" : rec.getRequestId()) + " was replayed after it was acknowledged");
    }

    // waitForCursor polls the spool cursor until it reads want. A sink writes its
    // cursor on shutdown, so a restart must wait for it.
    private static void waitForCursor(Path dir, int want) throws Exception {
        long deadline = System.currentTimeMillis() + EMIT_BOUND_MS;
        Path cursor = dir.resolve("accounting-spool").resolve("cursor");
        while (true) {
            String raw = Files.exists(cursor) ? new String(Files.readAllBytes(cursor), StandardCharsets.UTF_8).trim() : "";
            if (raw.equals(String.valueOf(want))) {
                return;
            }
            check(System.currentTimeMillis() < deadline, "spool cursor did not reach " + want + " (last read \"" + raw + "\")");
            Thread.sleep(10);
        }
    }

    // The publisher acknowledges three of five records, then the process stops. A new
    // sink over the same dir replays exactly the two unacknowledged records, in order.
    private static void checkReplaysUnacknowledgedAfterRestart() throws Exception {
        Path dir = tempDir();
        AtomicBoolean stop1 = new AtomicBoolean(false);
        BlockingQueue<Accountingrecord.AccountingRecord> q1 = new ArrayBlockingQueue<>(1);
        AccountingManager.EventBusSink sink1 = durable(q1, dir, stop1);
        emitAll(sink1, 5);
        check(receive(sink1, q1, 3).equals(List.of("r0", "r1", "r2")), "first run did not deliver r0,r1,r2 in order");
        stop1.set(true);
        waitForCursor(dir, 3);

        AtomicBoolean stop2 = new AtomicBoolean(false);
        BlockingQueue<Accountingrecord.AccountingRecord> q2 = new ArrayBlockingQueue<>(1);
        AccountingManager.EventBusSink sink2 = durable(q2, dir, stop2);
        check(receive(sink2, q2, 2).equals(List.of("r3", "r4")), "restart did not replay r3,r4 in order");
        expectNoMore(q2);
        stop2.set(true);
    }

    // The publisher is blocked: fifty completions must each return within the bound
    // while the records go to disk.
    private static void checkRpcNeverHeldWhenPublisherBlocked() throws Exception {
        BlockingQueue<Accountingrecord.AccountingRecord> queue = new ArrayBlockingQueue<>(1);
        AtomicBoolean stop = new AtomicBoolean(false);
        AccountingManager.EventBusSink sink = durable(queue, tempDir(), stop);
        emitAll(sink, 50);
        stop.set(true);
    }

    // The publisher is blocked while ten records are emitted, then resumes. Every record
    // arrives once, in emit order, and the log is removed once all are acknowledged.
    private static void checkOutageReplaysInOrderAndCompacts() throws Exception {
        Path dir = tempDir();
        AtomicBoolean stop = new AtomicBoolean(false);
        BlockingQueue<Accountingrecord.AccountingRecord> queue = new ArrayBlockingQueue<>(1);
        AccountingManager.EventBusSink sink = durable(queue, dir, stop);
        emitAll(sink, 10);
        List<String> got = receive(sink, queue, 10);
        for (int i = 0; i < 10; i++) {
            check(got.get(i).equals("r" + i), "record " + i + " arrived as " + got.get(i) + ": the spool did not replay in order");
        }
        expectNoMore(queue);
        Path log = dir.resolve("accounting-spool").resolve("records.log");
        long deadline = System.currentTimeMillis() + EMIT_BOUND_MS;
        while (Files.exists(log)) {
            check(System.currentTimeMillis() < deadline, "log " + log + " was not removed after every record was acknowledged");
            Thread.sleep(10);
        }
        stop.set(true);
    }

    // The publisher is blocked and the disk bound is shrunk so a few records fit.
    // Records past it are dropped and counted, and every record is either delivered
    // or counted as dropped.
    private static void checkDropsOnlyPastDiskBound() throws Exception {
        long saved = AccountingManager.EventBusSink.SPOOL_BYTES;
        AccountingManager.EventBusSink.SPOOL_BYTES = 1024;
        try {
            AtomicBoolean stop = new AtomicBoolean(false);
            BlockingQueue<Accountingrecord.AccountingRecord> queue = new ArrayBlockingQueue<>(1);
            AccountingManager.EventBusSink sink = durable(queue, tempDir(), stop);
            final int emitted = 40;
            emitAll(sink, emitted);
            // Let the drain thread take everything in its spool before counting.
            Thread.sleep(300);
            long dropped = sink.droppedCount();
            check(dropped > 0, "emitted " + emitted + " records past a 1024-byte disk bound, none were dropped");

            long delivered = 0;
            Accountingrecord.AccountingRecord rec;
            while ((rec = queue.poll(EMIT_BOUND_MS, TimeUnit.MILLISECONDS)) != null) {
                delivered++;
                sink.ack();
            }
            check(delivered + dropped == emitted,
                    "delivered " + delivered + " + dropped " + dropped + " != emitted " + emitted
                            + ": a record was lost without being counted");
            stop.set(true);
        } finally {
            AccountingManager.EventBusSink.SPOOL_BYTES = saved;
        }
    }

    // A log line that is not a record stops the sink from opening, rather than
    // skipping a record and misnumbering the cursor.
    private static void checkRejectsCorruptLog() throws Exception {
        Path dir = tempDir();
        Files.createDirectories(dir.resolve("accounting-spool"));
        Files.write(dir.resolve("accounting-spool").resolve("records.log"), "!!not base64!!\n".getBytes(StandardCharsets.UTF_8));
        boolean opened = true;
        try {
            durable(new ArrayBlockingQueue<>(1), dir, new AtomicBoolean(false));
        } catch (DiskStoreException e) {
            opened = false;
        }
        check(!opened, "a corrupt spool log opened without error");
    }

    public static void main(String[] args) throws Exception {
        // The drops and the restart checks log at ERROR on purpose. Silence the
        // accounting logger to keep the gate's clean-log rule meaningful.
        ((ch.qos.logback.classic.Logger) org.slf4j.LoggerFactory.getLogger("accounting"))
                .setLevel(ch.qos.logback.classic.Level.OFF);
        checkReplaysUnacknowledgedAfterRestart();
        checkRpcNeverHeldWhenPublisherBlocked();
        checkOutageReplaysInOrderAndCompacts();
        checkDropsOnlyPastDiskBound();
        checkRejectsCorruptLog();
        System.out.println("AccountingSinkDurableTest: all checks passed");
    }
}
