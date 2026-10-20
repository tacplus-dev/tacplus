// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the accounting-sink fixture (issue #34). NOT generated: run.sh
// copies this file into the generated chassis/am crate as tests/accounting_sink_spool.rs
// and runs it with `cargo test -p am`.
//
// What it pins: with the event-bus publisher blocked (nothing reads the queue),
// EventBusSink::emit never holds the calling RPC. Fifty concurrent completions each
// return within a bound. Records emitted during the outage are spooled and reach
// the publisher, in order, once it resumes. A spool that is full drops the overflow
// rather than blocking.
//
// Each body runs on its own thread, bounded by recv_timeout, so a blocked emit
// fails the gate rather than hanging it.

use std::future::Future;
use std::sync::Arc;
use std::sync::mpsc;
use std::time::Duration;

use agentsensor_model_types::sys::accounting::accounting::Event;
use agentsensor_model_types::vservices::accountingrecord::accountingrecord::AccountingRecord;
use am::{EventBusSink, Sink};

// EMIT_BOUND is how long an RPC completion may take to hand its record to the sink.
const EMIT_BOUND: Duration = Duration::from_secs(1);
// SPOOL_DEPTH must match SPOOL_DEPTH in chassis/am/src/am.rs.
const SPOOL_DEPTH: usize = 4096;

fn event(i: usize) -> Event {
    Event {
        request_id: format!("r{i}"),
        source_id: "test".to_string(),
        timestamp_ms: 1,
        ..Default::default()
    }
}

// run_within_bound drives body on a fresh current-thread runtime and fails if it has
// not finished within bound.
fn run_within_bound<F>(name: &str, bound: Duration, body: F)
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
    if done_rx.recv_timeout(bound).is_err() {
        panic!("{name} did not finish within {bound:?} while the publisher was blocked");
    }
}

// The publisher is blocked: nothing reads rx. Fifty concurrent completions must each
// return within the bound. Before the spool, the second emit parked its task.
#[test]
fn rpc_is_never_held_when_publisher_is_blocked() {
    run_within_bound("50 RPC completions", EMIT_BOUND, async {
        let (tx, _rx) = tokio::sync::mpsc::channel::<AccountingRecord>(1);
        let sink = Arc::new(EventBusSink::new(tx, "test-1".to_string()));
        let mut rpcs = Vec::new();
        for i in 0..50 {
            let sink = sink.clone();
            rpcs.push(tokio::spawn(async move { sink.emit(&event(i)).await }));
        }
        for rpc in rpcs {
            rpc.await.expect("RPC completion task");
        }
    });
}

// The publisher is blocked while ten records are emitted, then resumes. Every record
// arrives, in emit order.
#[test]
fn spool_replays_in_order_after_outage() {
    run_within_bound("replay of 10 records", EMIT_BOUND, async {
        let (tx, mut rx) = tokio::sync::mpsc::channel::<AccountingRecord>(1);
        let sink = EventBusSink::new(tx, "test-2".to_string());
        for i in 0..10 {
            sink.emit(&event(i)).await;
        }
        for i in 0..10 {
            let rec = rx.recv().await.expect("record reached the publisher");
            assert_eq!(
                rec.request_id,
                format!("r{i}"),
                "the spool did not replay in order"
            );
        }
    });
}

// The publisher is blocked and the spool depth is exceeded. Records are dropped, never
// held. Dropping the sink ends the drain task once it has delivered what it holds, so
// counting until the publisher's channel closes gives an exact delivered count to
// check against the emitted count.
#[test]
fn drops_only_when_spool_is_full() {
    let emitted = SPOOL_DEPTH + 100;
    run_within_bound("emit past a full spool", Duration::from_secs(5), async move {
        let (tx, mut rx) = tokio::sync::mpsc::channel::<AccountingRecord>(1);
        let sink = EventBusSink::new(tx, "test-3".to_string());
        for i in 0..emitted {
            sink.emit(&event(i)).await;
        }
        drop(sink);
        let mut delivered = 0usize;
        while rx.recv().await.is_some() {
            delivered += 1;
        }
        // The spool holds SPOOL_DEPTH records and nothing drains it while the emit loop
        // runs, so exactly the spooled records are delivered.
        assert!(
            delivered == SPOOL_DEPTH,
            "delivered {delivered} of {emitted} records; want exactly {SPOOL_DEPTH}"
        );
    });
}
