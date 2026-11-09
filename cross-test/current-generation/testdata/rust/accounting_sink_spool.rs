// Copyright (c) 2026 Jody Scott
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

// tacplus#273: the stored record carries the call's typed outcome, reason, code
// and metrics, and the sink's health is counters, never records.
#[test]
fn record_carries_typed_outcome_and_metrics() {
    use agentsensor_model_types::sys::accounting::accounting::{
        event, RpcCompleted, RpcMetrics, RpcOutcome, RpcReason,
    };
    run_within_bound("typed record", EMIT_BOUND, async {
        let (tx, mut rx) = tokio::sync::mpsc::channel::<AccountingRecord>(4);
        let sink = EventBusSink::new(tx, "test-4".to_string());
        let mut ev = event(0);
        ev.event = Some(event::Event::RpcCompleted(RpcCompleted {
            method: None,
            duration_ms: 12,
            outcome: RpcOutcome::DeniedAuthz.into(),
            code: 7,
            reason: RpcReason::NotEntitled.into(),
            metrics: Some(RpcMetrics { records_returned: 3, bytes: 10 }),
        }));
        sink.emit(&ev).await;
        let rec = rx.recv().await.expect("record reached the publisher");
        assert_eq!(rec.outcome, i32::from(RpcOutcome::DeniedAuthz));
        assert_eq!(rec.reason, i32::from(RpcReason::NotEntitled));
        assert_eq!(rec.code, 7);
        assert_eq!(rec.duration_ms, 12);
        assert_eq!(rec.records_returned, 3);
        assert_eq!(rec.bytes, 10);
        let stats = sink.stats();
        assert_eq!(stats.ingested, 1, "ingested counts records accepted into the spool");
        assert_eq!(stats.dropped, 0);
    });
}

// RecordingSink keeps every event it is handed.
struct RecordingSink(Arc<std::sync::Mutex<Vec<Event>>>);

#[tonic::async_trait]
impl Sink for RecordingSink {
    async fn emit(&self, event: &Event) {
        self.0.lock().unwrap().push(event.clone());
    }
}

fn recording_emitter() -> (Arc<am::Emitter>, Arc<std::sync::Mutex<Vec<Event>>>) {
    let events = Arc::new(std::sync::Mutex::new(Vec::new()));
    let emitter = Arc::new(am::Emitter::new("test".to_string(), Box::new(RecordingSink(events.clone()))));
    (emitter, events)
}

fn completed(ev: &Event) -> agentsensor_model_types::sys::accounting::accounting::RpcCompleted {
    use agentsensor_model_types::sys::accounting::accounting::event;
    match &ev.event {
        Some(event::Event::RpcCompleted(p)) => p.clone(),
        other => panic!("want an RpcCompleted, got {other:?}"),
    }
}

// A call refused at each stage is recorded once, with the outcome of its code
// and the reason of the stage it reached; a permitted call carries the metrics
// its handler attached. A SERVICE caller's call carrying the accounting_internal
// marker is not recorded; a user's call carrying it is.
#[test]
fn call_records_once_per_exit_path() {
    use agentsensor_model_types::generated::accounting_method::accounting_method::AccountingMethod;
    use agentsensor_model_types::sys::accounting::accounting::{EventLevel, RpcOutcome, RpcReason};
    run_within_bound("call accounting", EMIT_BOUND, async {
        let (emitter, events) = recording_emitter();
        let start = |internal| am::Call::start(Some(emitter.clone()), AccountingMethod::default(), "r1", internal);

        // Denied by the auth check: no identity, NOT_ENTITLED.
        let call = start(false);
        call.finish(Some(&tonic::Status::permission_denied("no"))).await;
        call.finish(Some(&tonic::Status::permission_denied("no"))).await;

        // Denied by a guard after authentication: POLICY_DENIED, with identity.
        let call = start(false);
        let identity = std::collections::HashMap::from([("sub".to_string(), "alice".to_string())]);
        call.authenticated(&identity, false);
        call.stage(am::Stage::Request);
        call.finish(Some(&tonic::Status::permission_denied("guard"))).await;

        // Permitted, with metrics the handler attached.
        let call = start(false);
        call.authenticated(&identity, false);
        call.stage(am::Stage::Request);
        let out: Result<(), tonic::Status> = call
            .handler(async {
                am::set_records_returned(5);
                am::add_bytes(7);
                am::add_bytes(1);
                Ok(())
            })
            .await;
        call.finish(out.as_ref().err()).await;

        // Handler error: HANDLER_ERROR, FAILED.
        let call = start(false);
        call.stage(am::Stage::Request);
        let out: Result<(), tonic::Status> = call.handler(async { Err(tonic::Status::internal("boom")) }).await;
        call.finish(out.as_ref().err()).await;

        // accounting_internal from a service caller: nothing.
        let call = start(true);
        call.authenticated(&identity, true);
        call.finish(None).await;

        // accounting_internal from a user: recorded.
        let call = start(true);
        call.authenticated(&identity, false);
        call.finish(None).await;

        // Outside an accounted call the metrics setters are no-ops.
        am::set_records_returned(9);

        let events = events.lock().unwrap();
        assert_eq!(events.len(), 5, "one record per call, none for the service's internal call: {events:?}");

        let denied = completed(&events[0]);
        assert_eq!(denied.outcome, i32::from(RpcOutcome::DeniedAuthz));
        assert_eq!(denied.reason, i32::from(RpcReason::NotEntitled));
        assert_eq!(denied.code, 7);
        assert!(events[0].identity.is_empty());
        assert_eq!(events[0].level, i32::from(EventLevel::Warn));

        let guarded = completed(&events[1]);
        assert_eq!(guarded.reason, i32::from(RpcReason::PolicyDenied));
        assert_eq!(events[1].identity.get("sub").map(String::as_str), Some("alice"));

        let ok = completed(&events[2]);
        assert_eq!(ok.outcome, i32::from(RpcOutcome::Permitted));
        assert_eq!(ok.reason, i32::from(RpcReason::Undefined));
        assert_eq!(ok.code, 0);
        let metrics = ok.metrics.expect("metrics attached");
        assert_eq!((metrics.records_returned, metrics.bytes), (5, 8));
        assert_eq!(events[2].level, i32::from(EventLevel::Info));

        let failed = completed(&events[3]);
        assert_eq!(failed.outcome, i32::from(RpcOutcome::Failed));
        assert_eq!(failed.reason, i32::from(RpcReason::HandlerError));
        assert_eq!(events[3].level, i32::from(EventLevel::Error));

        assert_eq!(completed(&events[4]).outcome, i32::from(RpcOutcome::Permitted), "a user's marker is ignored");
    });
}

// A response stream is recorded once, when it ends.
#[test]
fn stream_records_when_it_ends() {
    use agentsensor_model_types::generated::accounting_method::accounting_method::AccountingMethod;
    use agentsensor_model_types::sys::accounting::accounting::RpcOutcome;
    use tokio_stream::StreamExt;
    run_within_bound("stream accounting", EMIT_BOUND, async {
        let (emitter, events) = recording_emitter();
        let call = am::Call::start(Some(emitter.clone()), AccountingMethod::default(), "r1", false);
        let items = tokio_stream::iter(vec![Ok::<i32, tonic::Status>(1), Ok(2)]);
        let mut stream = am::AccountedStream::new(call, items);
        while stream.next().await.is_some() {}
        drop(stream);
        // The record is emitted from a spawned task.
        for _ in 0..100 {
            if !events.lock().unwrap().is_empty() {
                break;
            }
            tokio::task::yield_now().await;
        }
        let events = events.lock().unwrap();
        assert_eq!(events.len(), 1, "one record per stream: {events:?}");
        assert_eq!(completed(&events[0]).outcome, i32::from(RpcOutcome::Permitted));
    });
}
