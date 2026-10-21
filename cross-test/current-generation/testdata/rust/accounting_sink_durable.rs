// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the durable accounting spool (issue #70). NOT generated: run.sh
// copies this file into the generated chassis/am crate as tests/accounting_sink_durable.rs
// and runs it with `cargo test -p am`.
//
// What it pins: with grpcconfig.disk_persistence set, the spool is an append-only log
// on disk. Records the publisher has not acknowledged survive a restart and replay in
// order; records it has acknowledged are not replayed; records emitted during a bus
// outage replay in order once it returns, and the log is removed once all are
// acknowledged; the log is bounded on disk, dropping and counting only past the bound;
// and a corrupt log stops the sink from opening.
//
// Each body runs on its own thread, bounded by recv_timeout, so a stuck spool fails the
// gate rather than hanging it.

use std::future::Future;
use std::path::{Path, PathBuf};
use std::sync::mpsc;
use std::time::Duration;

use agentsensor_model_types::sys::accounting::accounting::Event;
use agentsensor_model_types::vservices::accountingrecord::accountingrecord::AccountingRecord;
use am::{EventBusSink, Sink};
use tokio::sync::mpsc::{Receiver, Sender, UnboundedSender};

const BOUND: Duration = Duration::from_secs(10);

fn event(i: usize) -> Event {
    Event {
        request_id: format!("r{i}"),
        source_id: "test".to_string(),
        timestamp_ms: 1,
        ..Default::default()
    }
}

// fresh_dir returns an empty directory unique to this test run.
fn fresh_dir(name: &str) -> PathBuf {
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let dir = std::env::temp_dir().join(format!("accounting-spool-{name}-{}-{nanos}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

// run_within_bound drives body on a fresh current-thread runtime and fails if it has not
// finished within BOUND.
fn run_within_bound<F>(name: &str, body: F)
where
    F: Future<Output = ()> + Send + 'static,
{
    let (done_tx, done_rx) = mpsc::channel();
    std::thread::spawn(move || {
        tokio::runtime::Builder::new_current_thread()
            .build()
            .expect("build runtime")
            .block_on(body);
        let _ = done_tx.send(());
    });
    if done_rx.recv_timeout(BOUND).is_err() {
        panic!("{name} did not finish within {BOUND:?}");
    }
}

// yield_until yields to the runtime until cond holds, so the spool task can run. It
// fails when cond does not hold after many yields.
async fn yield_until(what: &str, mut cond: impl FnMut() -> bool) {
    for _ in 0..100_000 {
        if cond() {
            return;
        }
        tokio::task::yield_now().await;
    }
    panic!("{what} did not happen");
}

fn cursor_reads(dir: &Path, want: usize) -> bool {
    std::fs::read_to_string(dir.join("accounting-spool").join("cursor"))
        .ok()
        .and_then(|raw| raw.trim().parse::<usize>().ok())
        == Some(want)
}

// recv_n reads n records, acknowledging each as a publisher does, and returns their
// request IDs in arrival order.
async fn recv_n(rx: &mut Receiver<AccountingRecord>, ack: &UnboundedSender<()>, n: usize) -> Vec<String> {
    let mut got = Vec::new();
    for _ in 0..n {
        let record = rx.recv().await.expect("record did not reach the publisher");
        got.push(record.request_id);
        ack.send(()).unwrap();
    }
    got
}

// The publisher acknowledges three of five records, then the process stops. A new sink
// over the same dir replays exactly the two unacknowledged records, in order.
#[test]
fn durable_spool_replays_unacknowledged_after_restart() {
    run_within_bound("restart replay", async {
        let dir = fresh_dir("restart");

        let (tx1, mut rx1): (Sender<AccountingRecord>, Receiver<AccountingRecord>) = tokio::sync::mpsc::channel(1);
        let (ack1, ack_rx1) = tokio::sync::mpsc::unbounded_channel::<()>();
        let sink1 = EventBusSink::new_durable(tx1, "test-restart".to_string(), &dir, ack_rx1).unwrap();
        for i in 0..5 {
            sink1.emit(&event(i)).await;
        }
        assert_eq!(recv_n(&mut rx1, &ack1, 3).await, vec!["r0", "r1", "r2"]);
        // The stop: the sink and the publisher's queue go away. The spool task writes its
        // cursor as it exits.
        drop(sink1);
        drop(rx1);
        yield_until("cursor 3 written at shutdown", || cursor_reads(&dir, 3)).await;

        let (tx2, mut rx2) = tokio::sync::mpsc::channel(1);
        let (ack2, ack_rx2) = tokio::sync::mpsc::unbounded_channel::<()>();
        let sink2 = EventBusSink::new_durable(tx2, "test-restart".to_string(), &dir, ack_rx2).unwrap();
        assert_eq!(recv_n(&mut rx2, &ack2, 2).await, vec!["r3", "r4"], "restart did not replay r3,r4 in order");
        for _ in 0..10_000 {
            tokio::task::yield_now().await;
        }
        assert!(rx2.try_recv().is_err(), "a record was replayed after it was acknowledged");
        drop(sink2);
    });
}

// The publisher is blocked while ten records are emitted, then resumes. Every record
// arrives once, in emit order, and the log is removed once all are acknowledged.
#[test]
fn durable_spool_replays_outage_in_order_and_compacts() {
    run_within_bound("outage replay", async {
        let dir = fresh_dir("outage");
        let (tx, mut rx) = tokio::sync::mpsc::channel(1);
        let (ack, ack_rx) = tokio::sync::mpsc::unbounded_channel::<()>();
        let sink = EventBusSink::new_durable(tx, "test-outage".to_string(), &dir, ack_rx).unwrap();
        for i in 0..10 {
            sink.emit(&event(i)).await;
        }
        let got = recv_n(&mut rx, &ack, 10).await;
        for (i, id) in got.iter().enumerate() {
            assert_eq!(id, &format!("r{i}"), "the spool did not replay in order");
        }
        let log = dir.join("accounting-spool").join("records.log");
        yield_until("log removed after every record was acknowledged", || !log.exists()).await;
        drop(sink);
    });
}

// The publisher is blocked and the disk bound is shrunk so a few records fit. Records past
// it are dropped and counted, and every record is either delivered or counted as dropped.
#[test]
fn durable_spool_drops_and_counts_only_past_disk_bound() {
    run_within_bound("disk bound", async {
        let dir = fresh_dir("bound");
        let (tx, mut rx) = tokio::sync::mpsc::channel(1);
        let (ack, ack_rx) = tokio::sync::mpsc::unbounded_channel::<()>();
        let sink = EventBusSink::with_spool_bytes(tx, "test-bound".to_string(), &dir, 1024, ack_rx).unwrap();
        const EMITTED: usize = 40;
        for i in 0..EMITTED {
            sink.emit(&event(i)).await;
        }
        // Let the spool task take everything emitted before counting.
        for _ in 0..10_000 {
            tokio::task::yield_now().await;
        }
        let dropped = sink.dropped_total() as usize;
        assert!(dropped > 0, "emitted {EMITTED} records past a 1024-byte disk bound, none were dropped");

        // Drain until the publisher side has been quiet for a long stretch of yields.
        let mut delivered = 0usize;
        let mut idle = 0usize;
        while idle < 10_000 {
            match rx.try_recv() {
                Ok(_record) => {
                    delivered += 1;
                    idle = 0;
                    ack.send(()).unwrap();
                }
                Err(_) => {
                    idle += 1;
                    tokio::task::yield_now().await;
                }
            }
        }
        assert_eq!(
            delivered + dropped,
            EMITTED,
            "delivered {delivered} + dropped {dropped} != emitted {EMITTED}: a record was lost without being counted"
        );
    });
}

// A log line that is not a record stops the sink from opening, rather than skipping a
// record and misnumbering the cursor.
#[test]
fn durable_spool_rejects_corrupt_log() {
    run_within_bound("corrupt log", async {
        let dir = fresh_dir("corrupt");
        std::fs::create_dir_all(dir.join("accounting-spool")).unwrap();
        std::fs::write(dir.join("accounting-spool").join("records.log"), "!!not base64!!\n").unwrap();
        let (tx, _rx) = tokio::sync::mpsc::channel(1);
        let (_ack, ack_rx) = tokio::sync::mpsc::unbounded_channel::<()>();
        assert!(EventBusSink::new_durable(tx, "test-corrupt".to_string(), &dir, ack_rx).is_err());
    });
}
