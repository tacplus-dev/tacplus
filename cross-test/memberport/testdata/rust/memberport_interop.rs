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
//! Java listen) on 3-second tokens, so each session rotates several times, and
//! every byte must arrive intact both ways.

mod support;

use std::path::PathBuf;
use std::sync::atomic::Ordering;
use std::sync::{Arc, RwLock};
use std::time::{Duration, Instant};

use base64::Engine as _;
use support::peer::{self, Mode, Server};
use support::*;

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

    // 3-second tokens renewed with 2 seconds left: about one rotation every
    // 2.25 seconds, each way.
    let rm = Minter::new(&fleet, X, 3, 2);
    let r = member_with(&fleet, rm.source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, false))), fleet.jwk_lookup());
    // Behind the Rust port: pushes seed 41, expects the Java client's seed 42.
    let server = Server::start(Mode::Push, 41, 800_000, 42, 4096, Duration::from_millis(20));
    let r_addr = r.listen(loopback0(), server.addr).await.expect("listen");
    write_interop(&dir, "go.port", &format!("{}\n", r_addr));

    let java_addr: std::net::SocketAddr =
        wait_interop(&dir, "java.port", Duration::from_secs(600)).await.parse().expect("java.port");
    let fwd = r.forward(java_addr).await.expect("forward");
    // Rust client -> Rust port -> Java port -> Java peer server (pushes 31, expects 32).
    let start = Instant::now();
    let result = blocking(move || peer::exchange_with(fwd, 32, 800_000, 31, 4096, Duration::from_millis(20), 0)).await;
    let elapsed = start.elapsed();
    assert!(result.ok(800_000), "Rust forward -> Java listen: no loss, duplication or reordering: {:?}", result);
    assert!(elapsed > Duration::from_millis(6_500), "the session must outlive two token lifetimes, took {:?}", elapsed);
    assert!(rm.mints.load(Ordering::SeqCst) >= 3, "the Rust client rotated onto new tokens: {} mints", rm.mints.load(Ordering::SeqCst));
    // Java client -> Java port -> Rust port -> this peer server.
    wait_for("the Java client's session", Duration::from_secs(120), || server.result_count() == 1).await;
    let sr = server.result(1).unwrap();
    assert!(sr.ok(800_000), "Java forward -> Rust listen: {:?}", sr);
    assert_eq!(server.accepted.load(Ordering::SeqCst), 1, "one byte session from Java: the target was dialled once");
    write_interop(&dir, "go.done", "1\n");
    wait_interop(&dir, "java.done", Duration::from_secs(120)).await;
}
