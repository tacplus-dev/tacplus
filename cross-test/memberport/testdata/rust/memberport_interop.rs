// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! The Rust half of the Rust <-> Java member port interop leg (tacplus#322):
//! cross-test/memberport/run.sh --language rust --interop runs it beside
//! MemberPortInteropTest.java (unchanged: it is the Go leg's Java half too),
//! both pointed at MEMBERPORT_INTEROP_DIR. This side writes the one fake fleet
//! there in that test's format (kernel.key, ca.pem, java-leaf.pem,
//! java-leaf.key, fleet.ready) and plays the "go" side of its marker files
//! (go.port, go.done); then a Rust member port and a Java member port tunnel to
//! each other both ways at once (Java forward -> Rust listen, Rust forward ->
//! Java listen) on 6-second tokens, so each session rotates several times, and
//! every byte must arrive intact both ways.
//!
//! The leg is bounded by its bytes, not by seconds (#919): each stream is a
//! fixed count, cut only when it stops moving (the peer's stall bound).
//! Neither side takes its member port down before the other is done: a side
//! whose own direction fails says so in its done file ("ok", or its failures)
//! and keeps serving the other's session, so each side reports its own result,
//! not a reset. Every wait after the streams start has an explicit deadline,
//! named when it passes.

mod support;

use std::path::PathBuf;
use std::sync::atomic::Ordering;
use std::sync::{Arc, RwLock};
use std::time::{Duration, Instant};

use base64::Engine as _;
use support::peer::{self, Mode, Server};
use support::*;

/// Each wait's after the streams start: for the other side's session, and for
/// its done file. Generous: the streams take seconds.
const DEADLINE: Duration = Duration::from_secs(300);

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_and_java_member_ports_tunnel_to_each_other() {
    let Some(dir) = std::env::var_os("MEMBERPORT_INTEROP_DIR").map(PathBuf::from) else {
        eprintln!("MEMBERPORT_INTEROP_DIR runs the Rust <-> Java interop leg (run.sh --interop)");
        return;
    };
    let fleet = Fleet::new();

    // The shared fleet: the Java side signs its own member tokens with the same
    // kernel key and trusts the same CA.
    write_interop(
        &dir,
        "kernel.key",
        &format!(
            "{}\n{}\n",
            fleet.kernel.id(),
            base64::engine::general_purpose::STANDARD.encode(ed25519_pkcs8_v1_pem(&fleet.kernel))
        ),
    );
    write_interop(&dir, "ca.pem", &cert_pem(&fleet.ca_der));
    let (java_chain, java_key) = fleet.leaf_parts(X, SVC, false);
    write_interop(&dir, "java-leaf.pem", &java_chain.iter().map(cert_pem).collect::<String>());
    write_interop(&dir, "java-leaf.key", &java_key);
    write_interop(&dir, "fleet.ready", "1\n");

    // 6-second tokens renewed with 4 seconds left: a stream lives 4 to 6
    // seconds and the client rotates at 75% of it, so a cutover has a second
    // or more before the old stream's exp, as in the behaviour tests (#737).
    let rm = Minter::new(&fleet, X, 6, 4);
    let r = member_with(&fleet, rm.source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, false))), fleet.jwk_lookup());
    // Behind the Rust port: pushes seed 41, expects the Java client's seed 42.
    let server = Server::start(Mode::Push, 41, 1_600_000, 42, 4096, Duration::from_millis(20));
    let r_addr = r.listen(loopback0(), server.addr).await.expect("listen");
    write_interop(&dir, "go.port", &format!("{}\n", r_addr));

    let java_addr: std::net::SocketAddr =
        wait_interop(&dir, "java.port", Duration::from_secs(600)).await.parse().expect("java.port");
    let mut failures = Vec::new();
    // Rust client -> Rust port -> Java port -> Java peer server (pushes 31, expects 32).
    match r.forward(java_addr).await {
        Err(e) => failures.push(format!("Rust forward -> Java listen: forward: {:?}", e)),
        Ok(fwd) => {
            let start = Instant::now();
            let result = blocking(move || peer::exchange_with(fwd, 32, 1_600_000, 31, 4096, Duration::from_millis(20), 0)).await;
            let elapsed = start.elapsed();
            if !result.ok(1_600_000) {
                failures.push(format!(
                    "Rust forward -> Java listen: no loss, duplication or reordering: {:?} after {:?}",
                    result, elapsed
                ));
            } else if elapsed <= Duration::from_millis(12_500) {
                failures.push(format!("the session must outlive two token lifetimes, took {:?}", elapsed));
            }
            let mints = rm.mints.load(Ordering::SeqCst);
            if mints < 3 {
                failures.push(format!("the Rust client rotated onto new tokens: {} mints", mints));
            }
        }
    }
    // Java client -> Java port -> Rust port -> this peer server.
    let until = Instant::now() + DEADLINE;
    while server.result_count() == 0 && Instant::now() < until {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    match server.result(1) {
        None => failures.push(format!(
            "Java forward -> Rust listen: no session ended within the Rust side's deadline of {:?}",
            DEADLINE
        )),
        Some(sr) if !sr.ok(1_600_000) => failures.push(format!("Java forward -> Rust listen: {:?}", sr)),
        Some(_) => {}
    }
    let accepted = server.accepted.load(Ordering::SeqCst);
    if accepted != 1 {
        failures.push(format!("one byte session from Java: the target was dialled {} times", accepted));
    }
    let report = if failures.is_empty() { "ok\n".to_string() } else { failures.join("\n") + "\n" };
    write_interop(&dir, "go.done", &report);
    let java = read_interop(&dir, "java.done", DEADLINE).await.unwrap_or_else(|| {
        format!("nothing: it was not done within the Rust side's deadline of {:?}", DEADLINE)
    });
    assert!(failures.is_empty(), "{}\nthe Java side reported: {}", failures.join("\n"), java);
}
