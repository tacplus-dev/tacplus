// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Rust leg of the trace-carry gate (tacplus#313). Drives the GENERATED
// extraction and record dispatch:
//   - a projection carries its source's trace_id;
//   - the row the dispatch hands its store has no trace, read back raw from
//     the JSON the store would hold;
//   - a write through the generated disk helpers (snapshot and redo record),
//     then a raw read of what the store holds, carries no trace.
// Prints one line `trace-carry rust: <n> cases`.

use std::future::Future;
use std::pin::pin;
use std::process::exit;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll, Waker};

use tracecarry_model::s2sm::diskstore_stores::{load_note, record_note, save_note};
use tracecarry_model_types::trace::tracecarry::*;

const TRACE_ID: &str = "trace-carry-313";

fn fail(msg: String) -> ! {
    eprintln!("trace-carry rust: {msg}");
    exit(1);
}

// assert_no_trace fails when raw stored JSON has a trace key or the trace value.
fn assert_no_trace(what: &str, raw: &str) {
    let obj: serde_json::Value =
        serde_json::from_str(raw).unwrap_or_else(|e| fail(format!("{what}: not JSON: {e}: {raw}")));
    if obj.get("trace_id").is_some() || obj.get("traceId").is_some() || raw.contains(TRACE_ID) {
        fail(format!("{what} carries the trace: {raw}"));
    }
}

// The dispatch awaits nothing but its own insert, which is ready at once.
fn block_on<F: Future>(f: F) -> F::Output {
    let mut f = pin!(f);
    let mut cx = Context::from_waker(Waker::noop());
    loop {
        if let Poll::Ready(v) = f.as_mut().poll(&mut cx) {
            return v;
        }
    }
}

fn main() {
    let src = Reading {
        reading_id: "r-1".into(),
        observed_at_ms: 1_700_000_000_000,
        value: 2.5,
        trace_id: TRACE_ID.into(),
        ..Default::default()
    };

    let row = extract_reading_row_from_reading(&src).unwrap_or_else(|| fail("no projection".into()));
    if row.trace_id != TRACE_ID {
        fail(format!("projection trace_id = {:?}, want the source's {TRACE_ID:?}", row.trace_id));
    }
    let projected = reading_to_projections(&src).reading_row.map(|r| r.trace_id);
    if projected.as_deref() != Some(TRACE_ID) {
        fail(format!("reading_to_projections trace_id = {projected:?}, want {TRACE_ID:?}"));
    }

    let stored: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = stored.clone();
    let insert: ReadingRowInsert = Arc::new(move |row: ReadingRowRow| {
        let sink = sink.clone();
        Box::pin(async move {
            sink.lock().unwrap().push(serde_json::to_string(&row)?);
            Ok(())
        })
    });
    if let Err(e) = block_on(handle_reading(&src, Some(&insert))) {
        fail(format!("handle_reading: {e}"));
    }
    let stored = stored.lock().unwrap();
    if stored.len() != 1 {
        fail(format!("dispatch stored {} rows, want 1", stored.len()));
    }
    let raw: serde_json::Value = serde_json::from_str(&stored[0]).unwrap();
    if raw.get("trace_id").is_some() || stored[0].contains(TRACE_ID) {
        fail(format!("the stored row carries the trace: {}", stored[0]));
    }
    if src.trace_id != TRACE_ID {
        fail("storing a projection must not clear the source's trace".into());
    }

    let base = std::env::temp_dir().join(format!("trace-carry-rust-{}", std::process::id()));
    let note = Note {
        note_id: "n-1".into(),
        body: "kept".into(),
        trace_id: TRACE_ID.into(),
        ..Default::default()
    };
    if let Err(e) = save_note(&base, "ns", &note, &diskstore::SnapshotOpts::default()) {
        fail(format!("save_note: {e}"));
    }
    let raw = std::fs::read(diskstore::store_path(&base, "ns", "note", ".snapshot"))
        .unwrap_or_else(|e| fail(format!("raw read of the snapshot: {e}")));
    assert_no_trace("the snapshot", &String::from_utf8_lossy(&raw));
    match load_note(&base, "ns") {
        Ok(Some(n)) if n.trace_id.is_empty() && n.body == "kept" => {}
        other => fail(format!("load_note = {other:?}; want the note back without a trace")),
    }
    let _ = std::fs::remove_dir_all(&base);
    let rec = record_note(&note).unwrap_or_else(|e| fail(format!("record_note: {e}")));
    assert_no_trace("the redo record", &serde_json::to_string(&rec.payload).unwrap());
    if note.trace_id != TRACE_ID {
        fail("storing a value must not clear the caller's trace".into());
    }
    println!("trace-carry rust: 4 cases");
}
