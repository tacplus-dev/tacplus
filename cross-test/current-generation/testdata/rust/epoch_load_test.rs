// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the epoch-load fixture. NOT generated: run.sh copies this
// file into the generated crate's tests/ directory and runs it with
// `cargo test --test epoch_load_test`.
//
// What it pins: Lease carries an Expiry whose exp is (spec.epoch_expiration), a
// write-time assertion (a future epoch). A record written while valid must still
// LOAD after its exp has passed, or a service cannot restart once any stored
// record ages out. The paired control: a non-time violation (an empty lease_id)
// must still fail the load, even when the record is also expired. This executes
// the generated load_lease, which is the path the disk loader runs.

use std::path::Path;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use epochload_model::s2sm::diskstore_stores;
use epochload_model::types::services::epochload::{Expiry, Lease};

fn now_secs() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs() as i64
}

fn fresh_base(name: &str) -> std::path::PathBuf {
    let base = std::env::temp_dir().join(format!("epoch-load-test-{}-{}", name, std::process::id()));
    let _ = std::fs::remove_dir_all(&base);
    base
}

// save_then_age writes one Lease, then waits until its exp has passed.
fn save_then_age(base: &Path, lease: &Lease) {
    diskstore_stores::save_lease(base, "epochload", lease, &diskstore::SnapshotOpts::default()).expect("save_lease");
    let exp = lease.expiry.as_ref().expect("expiry").exp;
    while now_secs() <= exp {
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[test]
fn load_lease_accepts_an_epoch_that_expired_after_it_was_written() {
    let base = fresh_base("accepts");
    let exp = now_secs() + 1;
    let lease = Lease {
        lease_id: "lease-1".to_string(),
        expiry: Some(Expiry { exp, ..Default::default() }),
        ..Default::default()
    };
    save_then_age(&base, &lease);

    let got = diskstore_stores::load_lease(&base, "epochload")
        .expect("load_lease after exp has passed must succeed (data ages; the owner retires it)")
        .expect("snapshot present");
    assert_eq!(got.lease_id, "lease-1");
    assert_eq!(got.expiry.map(|e| e.exp), Some(exp));
}

#[test]
fn load_lease_still_rejects_a_non_time_violation() {
    let base = fresh_base("rejects");
    let exp = now_secs() + 1;
    let lease = Lease {
        lease_id: String::new(),
        expiry: Some(Expiry { exp, ..Default::default() }),
        ..Default::default()
    };
    save_then_age(&base, &lease);

    assert!(
        diskstore_stores::load_lease(&base, "epochload").is_err(),
        "load_lease with an empty lease_id must fail even when exp has passed"
    );
}
