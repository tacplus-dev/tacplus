// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! The generated Rust member port (tacplus#212, on gRPC since #321, Rust in
//! #322), driven over real sockets in-process: memberport-peer ↔ member port ↔
//! member port ↔ memberport-peer, and hand-driven tunnel streams for what a
//! generated member port never sends. The twin of MemberPortBehaviorTest.java;
//! cross-test/memberport/run.sh copies it into the generated memberport crate.

mod support;

use std::io::{Read, Write};
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, RwLock};
use std::time::{Duration, Instant};

use support::peer::{self, Mode, Server};
use support::*;
use tonic::Code;

// ==== Correctness ====

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn bytes_cross_intact_both_directions_from_one_byte_to_multi_megabyte() {
    let fleet = Fleet::new();
    let a = member(&fleet, X);
    let b = member(&fleet, X);
    for n in [1u64, 1000, 65_536, 65_537, 3 * MB + 17] {
        let server = Server::push(2_000 + n, n, 1_000 + n);
        let fwd = tunnel(&a, &b, &server).await;
        let client = blocking(move || peer::exchange(fwd, 1_000 + n, n, 2_000 + n)).await;
        assert!(client.ok(n), "client side of {} bytes: {:?}", n, client);
        wait_for("server result", Duration::from_secs(10), || server.result_count() == 1).await;
        let r = server.result(1).unwrap();
        assert!(r.ok(n), "server side of {} bytes: {:?}", n, r);
    }
    let echo = Server::echo();
    let fwd = tunnel(&a, &b, &echo).await;
    let r = blocking(move || peer::exchange(fwd, 77, 2 * MB, 77)).await;
    assert!(r.ok(2 * MB), "echo: {:?}", r);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn many_concurrent_connections() {
    let fleet = Fleet::new();
    let a = member(&fleet, X);
    let b = member(&fleet, X);
    let echo = Server::echo();
    let fwd = tunnel(&a, &b, &echo).await;
    let mut runs = Vec::new();
    for i in 0..48u64 {
        let seed = 10_000 + i;
        runs.push((seed, blocking_spawn(move || peer::exchange(fwd, seed, 256 * 1024 + seed, seed))));
    }
    for (i, (seed, run)) in runs.into_iter().enumerate() {
        let r = tokio::time::timeout(Duration::from_secs(120), run).await.expect("in time").expect("run");
        assert!(r.ok(256 * 1024 + seed), "connection {}: {:?}", i, r);
    }
    assert_eq!(echo.accepted.load(Ordering::SeqCst), 48);
}

fn blocking_spawn<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> tokio::task::JoinHandle<T> {
    tokio::task::spawn_blocking(f)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn half_close_in_either_direction() {
    let fleet = Fleet::new();
    let a = member(&fleet, X);
    let b = member(&fleet, X);
    // The client half-closes at once; the server still pushes 1 MiB after.
    let server_pushes = Server::push(5, MB, 6);
    let fwd = tunnel(&a, &b, &server_pushes).await;
    let r1 = blocking(move || peer::exchange(fwd, 6, 0, 5)).await;
    assert!(r1.ok(MB), "client half-closed first: {:?}", r1);
    // The server half-closes at once; the client still sends 1 MiB after.
    let server_done = Server::push(7, 0, 8);
    let fwd = tunnel(&a, &b, &server_done).await;
    let r2 = blocking(move || peer::exchange(fwd, 8, MB, 7)).await;
    assert!(r2.ok(0), "server half-closed first: {:?}", r2);
    wait_for("server result", Duration::from_secs(10), || server_done.result_count() == 1).await;
    let r = server_done.result(1).unwrap();
    assert!(r.ok(MB), "server side: {:?}", r);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn byte_session_runs_across_several_token_lifetimes_both_ways_at_once() {
    let fleet = Fleet::new();
    // 3-second tokens renewed with 2 seconds left: the client rotates at 75%
    // of each lifetime, about every 2.25 seconds.
    let am = Minter::new(&fleet, X, 3, 2);
    let bm = Minter::new(&fleet, X, 3, 2);
    let a = member_with(&fleet, am.source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, true))), fleet.jwk_lookup());
    let b = member_with(&fleet, bm.source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, true))), fleet.jwk_lookup());
    // Both directions trickle at once for about 8 seconds, so every cutover
    // has bytes in flight each way.
    let server = Server::start(Mode::Push, 21, 800_000, 22, 4096, Duration::from_millis(20));
    let fwd = tunnel(&a, &b, &server).await;
    let start = Instant::now();
    let r = blocking(move || peer::exchange_with(fwd, 22, 800_000, 21, 4096, Duration::from_millis(20), 0)).await;
    let elapsed = start.elapsed();
    assert!(r.ok(800_000), "client: no loss, duplication or reordering: {:?}", r);
    wait_for("server result", Duration::from_secs(10), || server.result_count() == 1).await;
    let sr = server.result(1).unwrap();
    assert!(sr.ok(800_000), "server: {:?}", sr);
    assert!(elapsed > Duration::from_millis(6_500), "the session must outlive two token lifetimes, took {:?}", elapsed);
    assert_eq!(server.accepted.load(Ordering::SeqCst), 1, "one byte session: the target was dialled once");
    assert!(am.mints.load(Ordering::SeqCst) >= 3, "the client rotated onto new tokens: {} mints", am.mints.load(Ordering::SeqCst));
}

// ==== Backpressure ====

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn slow_reader_bounds_buffering_and_drops_nothing() {
    let fleet = Fleet::new();
    let a = member(&fleet, X);
    let b = member(&fleet, X);
    let server = Server::push(31, 0, 32);
    server.read_pause_ms.store(1_000_000, Ordering::SeqCst); // reads one chunk, then stalls
    let fwd = tunnel(&a, &b, &server).await;
    let total = 64 * MB;
    let written = Arc::new(AtomicU64::new(0));
    let w = written.clone();
    let writer = std::thread::spawn(move || {
        let mut s = std::net::TcpStream::connect(fwd).expect("connect");
        let mut stream = peer::Stream::new(32);
        let mut buf = vec![0u8; 64 * 1024];
        while w.load(Ordering::SeqCst) < total {
            let len = (buf.len() as u64).min(total - w.load(Ordering::SeqCst)) as usize;
            stream.fill(&mut buf[..len]);
            if s.write_all(&buf[..len]).is_err() {
                return;
            }
            w.fetch_add(len as u64, Ordering::SeqCst);
        }
        let _ = s.shutdown(std::net::Shutdown::Write);
        // Hold the connection until the server is done with it.
        let mut sink = [0u8; 1];
        let _ = s.read(&mut sink);
    });
    tokio::time::sleep(Duration::from_secs(3)).await;
    let buffered = written.load(Ordering::SeqCst);
    assert!(
        buffered < 24 * MB,
        "a stalled reader must stall the writer, not buffer: {} bytes written of {}",
        buffered,
        total
    );
    // Unstall the reader: every byte must still arrive, in order.
    server.read_pause_ms.store(0, Ordering::SeqCst);
    wait_for("server result", Duration::from_secs(120), || server.result_count() == 1).await;
    assert_eq!(written.load(Ordering::SeqCst), total);
    let r = server.result(1).unwrap();
    assert!(r.ok(total), "server: {:?}", r);
    let _ = blocking(move || writer.join()).await;
}

// ==== Refusals: fail fast, nothing forwarded ====

async fn listener_on_x(fleet: &Arc<Fleet>, behind: &Server) -> std::net::SocketAddr {
    member(fleet, X).listen(loopback0(), behind.addr).await.expect("listen")
}

/// Opens a stream with `token` and asserts how it is refused, and that nothing was dialled.
async fn assert_open_refused(fleet: &Arc<Fleet>, code: Code, what: &str, token: Option<&str>) {
    let behind = Server::echo();
    let port = listener_on_x(fleet, &behind).await;
    let r = Raw::open(&raw_channel(port), token);
    r.send(open_chunk()).await.send(data(&[1, 2, 3])).await;
    assert_refused(code, &r.status(Duration::from_secs(5)).await, what);
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(behind.accepted.load(Ordering::SeqCst), 0, "{}: nothing may reach the peer behind the port", what);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn bad_missing_or_expired_tokens_are_unauthenticated() {
    let fleet = Fleet::new();
    assert_open_refused(&fleet, Code::Unauthenticated, "no token", None).await;
    assert_open_refused(&fleet, Code::Unauthenticated, "garbage token", Some("not-a-token")).await;
    // Ten seconds past: inside the JWT clock-skew window, still refused.
    let expired = fleet.mint(SVC, X, &aud(), -10);
    assert_open_refused(&fleet, Code::Unauthenticated, "expired token", Some(&expired)).await;
    let grpc_aud = fleet.mint(SVC, X, SVC, 300);
    assert_open_refused(&fleet, Code::Unauthenticated, "gRPC audience", Some(&grpc_aud)).await;
    let rogue = key::keys::generate_ed25519_key().unwrap();
    let unknown = mint_with(&rogue, SVC, X, &aud(), 300);
    assert_open_refused(&fleet, Code::Unauthenticated, "unknown signing key", Some(&unknown)).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn valid_tokens_not_allowed_here_are_permission_denied() {
    let fleet = Fleet::new();
    let other_tenant = fleet.mint(SVC, Y, &aud(), 300);
    assert_open_refused(&fleet, Code::PermissionDenied, "tenant Y's token", Some(&other_tenant)).await;
    let other_service = fleet.mint("other", X, &aud(), 300);
    assert_open_refused(&fleet, Code::PermissionDenied, "another service", Some(&other_service)).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_data_chunk_cannot_open_a_stream() {
    let fleet = Fleet::new();
    let behind = Server::echo();
    let port = listener_on_x(&fleet, &behind).await;
    let r = Raw::open(&raw_channel(port), Some(&fleet.mint(SVC, X, &aud(), 300)));
    r.send(data(&[1, 2, 3])).await;
    assert_refused(Code::PermissionDenied, &r.status(Duration::from_secs(5)).await, "data before the session opens");
    assert_eq!(behind.accepted.load(Ordering::SeqCst), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn plaintext_and_non_grpc_tls_peers_are_refused() {
    let fleet = Fleet::new();
    let behind = Server::echo();
    let port = listener_on_x(&fleet, &behind).await;
    let dropped = blocking(move || {
        let mut plain = std::net::TcpStream::connect(port).unwrap();
        plain.write_all(b"HZC").unwrap();
        plain.write_all(&[0u8; 64]).unwrap();
        plain.set_read_timeout(Some(Duration::from_secs(12))).unwrap();
        let mut buf = [0u8; 4096];
        loop {
            match plain.read(&mut buf) {
                Ok(0) => return true,
                Ok(_) => continue,
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock || e.kind() == std::io::ErrorKind::TimedOut => return false,
                Err(_) => return true,
            }
        }
    })
    .await;
    assert!(dropped, "a plaintext peer must be dropped");
    // A #212 TLS-framing peer (ALPN tacplus-member/1) must be refused.
    let old = raw_tls(port, b"tacplus-member/1").await;
    let refused = match old {
        Err(_) => true,
        Ok(mut tls) => {
            use tokio::io::{AsyncReadExt, AsyncWriteExt};
            let _ = tls.write_all(&[1, 0, 0, 0, 1, 0]).await;
            let mut b = [0u8; 1];
            !matches!(tokio::time::timeout(Duration::from_secs(5), tls.read(&mut b)).await, Ok(Ok(n)) if n > 0)
        }
    };
    assert!(refused, "a #212 TLS-framing peer must be refused");
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(behind.accepted.load(Ordering::SeqCst), 0, "nothing may reach the peer behind the port");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_stream_that_never_sends_its_first_chunk_is_dropped_at_the_admission_deadline() {
    let fleet = Fleet::new();
    let behind = Server::echo();
    let port = listener_on_x(&fleet, &behind).await;
    let start = Instant::now();
    let r = Raw::open(&raw_channel(port), Some(&fleet.mint(SVC, X, &aud(), 300)));
    let s = r.status(Duration::from_secs(20)).await;
    let elapsed = start.elapsed();
    assert!(
        s.code() != Code::Ok && elapsed < Duration::from_secs(14),
        "dropped at the 10-second admission deadline: {:?} after {:?}",
        s,
        elapsed
    );
    assert_eq!(behind.accepted.load(Ordering::SeqCst), 0, "nothing reached the peer behind the port");
}

// ==== Sessions: binding and faults ====

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_rotation_presenting_another_subjects_token_is_refused() {
    let fleet = Fleet::new();
    let behind = Server::echo();
    let ch = raw_channel(listener_on_x(&fleet, &behind).await);
    let mut first = Raw::open(&ch, Some(&fleet.mint(SVC, X, &aud(), 300)));
    let id = open_session(&mut first).await;
    let other_tenant = Raw::open(&ch, Some(&fleet.mint(SVC, Y, &aud(), 300)));
    other_tenant.send(join(&id)).await;
    assert_refused(Code::PermissionDenied, &other_tenant.status(Duration::from_secs(5)).await, "tenant Y's token with X's session id");
    let other_service = Raw::open(&ch, Some(&fleet.mint("other", X, &aud(), 300)));
    other_service.send(join(&id)).await;
    assert_refused(Code::PermissionDenied, &other_service.status(Duration::from_secs(5)).await, "another service's token with the session id");
    let unknown = Raw::open(&ch, Some(&fleet.mint(SVC, X, &aud(), 300)));
    unknown.send(join("no-such-session")).await;
    assert_refused(Code::PermissionDenied, &unknown.status(Duration::from_secs(5)).await, "an unknown session id");
    // The session itself is untouched by the refused rotations.
    first.send(data(&[7, 8, 9])).await;
    assert_eq!(first.next().await.payload, vec![7, 8, 9], "the session still echoes");
    assert_eq!(behind.accepted.load(Ordering::SeqCst), 1);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_old_stream_cut_off_without_last_drops_the_session_and_does_not_splice() {
    let fleet = Fleet::new();
    let behind = Server::echo();
    let ch = raw_channel(listener_on_x(&fleet, &behind).await);
    let mut old = Raw::open(&ch, Some(&fleet.mint(SVC, X, &aud(), 300)));
    let id = open_session(&mut old).await;
    old.send(data(&[1, 2, 3])).await;
    assert_eq!(old.next().await.payload, vec![1, 2, 3]);
    let mut next = Raw::open(&ch, Some(&fleet.mint(SVC, X, &aud(), 300)));
    next.send(join(&id)).await;
    assert_eq!(next.next().await.session_id, id, "the rotation is attached");
    // The old stream ends with no last: a fault, not a cutover.
    old.cancel();
    next.send(data(&[4, 5, 6])).await;
    let s = next.status(Duration::from_secs(5)).await;
    assert_ne!(s.code(), Code::Ok, "the new stream is dropped with the session: {:?}", s);
    wait_for("the connection behind the port to end", Duration::from_secs(5), || behind.result_count() == 1).await;
    assert_eq!(behind.bytes_received.load(Ordering::SeqCst), 3, "nothing sent on the new stream was spliced on");
    let r = behind.result(1).unwrap();
    assert!(r.reset(), "the connection behind the port is reset, not closed: {:?}", r);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_receiver_ending_its_stream_without_last_resets_the_senders_local_connection() {
    // A receiver that echoes one chunk, then ends the stream OK with no last:
    // the sender must not take that for a clean end.
    let fleet = Fleet::new();
    let token = fleet.mint(SVC, X, &aud(), 300);
    let receiver = fake_receiver(&fleet, token, FakeMode::EchoThenEnd, Arc::new(AtomicUsize::new(0))).await;
    let fwd = member(&fleet, X).forward(receiver).await.unwrap();
    let outcome = blocking(move || {
        let mut s = std::net::TcpStream::connect(fwd).unwrap();
        s.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
        s.write_all(&[1, 2, 3]).unwrap();
        let mut echoed = [0u8; 3];
        s.read_exact(&mut echoed).unwrap();
        let mut b = [0u8; 1];
        (echoed, s.read(&mut b))
    })
    .await;
    assert_eq!(outcome.0, [1, 2, 3]);
    match outcome.1 {
        Err(e) => assert_eq!(e.kind(), std::io::ErrorKind::ConnectionReset, "the local connection is reset: {}", e),
        Ok(n) => panic!("a stream that ends without last is a fault, not an EOF: read {}", n),
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn cutover_with_last_in_both_directions_loses_nothing() {
    let fleet = Fleet::new();
    let behind = Server::echo();
    let ch = raw_channel(listener_on_x(&fleet, &behind).await);
    let mut old = Raw::open(&ch, Some(&fleet.mint(SVC, X, &aud(), 300)));
    let id = open_session(&mut old).await;
    old.send(data(&[1, 2, 3])).await;
    let mut next = Raw::open(&ch, Some(&fleet.mint(SVC, X, &aud(), 300)));
    next.send(join(&id)).await;
    assert_eq!(next.next().await.session_id, id);
    // Bytes sent on the new stream before the old one's last wait for it.
    next.send(data(&[7, 8])).await;
    old.send(data(&[4, 5, 6])).await.send(last()).await;
    let mut on_old = Vec::new();
    loop {
        let c = old.next().await;
        on_old.extend_from_slice(&c.payload);
        if c.last {
            break;
        }
    }
    assert_eq!(old.status(Duration::from_secs(5)).await.code(), Code::Ok, "the server ends the old stream after its own last");
    let mut on_new = Vec::new();
    while on_old.len() + on_new.len() < 8 {
        on_new.extend_from_slice(&next.next().await.payload);
    }
    let all: Vec<u8> = on_old.into_iter().chain(on_new).collect();
    assert_eq!(all, vec![1, 2, 3, 4, 5, 6, 7, 8], "in order across the cutover, each way");
}

// ==== Failure modes ====

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_mint_failure_drops_the_session_at_expiry() {
    let fleet = Fleet::new();
    let am = Minter::new(&fleet, X, 3, 2);
    let a = member_with(&fleet, am.source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, true))), fleet.jwk_lookup());
    let echo = Server::echo();
    let fwd = tunnel(&a, &member(&fleet, X), &echo).await;
    am.token().await; // the first token is in hand
    am.stall.store(true, Ordering::SeqCst);
    let start = Instant::now();
    let r = blocking(move || peer::exchange_with(fwd, 51, 2_000_000, 51, 2048, Duration::from_millis(10), 0)).await;
    let elapsed = start.elapsed();
    am.stall.store(false, Ordering::SeqCst);
    assert!(!r.ok(2_000_000) && r.first_mismatch.is_none(), "cut, intact: {:?}", r);
    assert!(r.reset(), "the local connection is reset: {:?}", r);
    assert!(elapsed < Duration::from_secs(5), "dropped at the token's expiry, took {:?}", elapsed);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_revoked_caller_is_cut_off_within_one_lifetime() {
    let fleet = Fleet::new();
    // The identity is revoked: it gets no new token, only the one in hand.
    let revoked = Minter::new(&fleet, X, 3, 1);
    let a = member_with(&fleet, revoked.source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, true))), fleet.jwk_lookup());
    let echo = Server::echo();
    let fwd = tunnel(&a, &member(&fleet, X), &echo).await;
    revoked.token().await;
    revoked.never_renew.store(true, Ordering::SeqCst);
    let start = Instant::now();
    let r = blocking(move || peer::exchange_with(fwd, 41, 2_000_000, 41, 2048, Duration::from_millis(10), 0)).await;
    let elapsed = start.elapsed();
    assert!(!r.ok(2_000_000) && r.first_mismatch.is_none(), "cut, intact: {:?}", r);
    assert!(elapsed < Duration::from_millis(4_500), "cut off within one token lifetime, took {:?}", elapsed);
    // And a new connection with the expired token in hand is refused at once.
    let again = blocking(move || peer::exchange(fwd, 42, 1000, 42)).await;
    assert!(!again.ok(1000) && again.received == 0, "refused: {:?}", again);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_refused_tunnel_resets_its_local_connection_well_under_a_second() {
    let fleet = Fleet::new();
    let x = member(&fleet, X);
    let behind = Server::echo();
    let x_port = x.listen(loopback0(), behind.addr).await.unwrap();
    // Warm up the TLS and gRPC paths on an admitted tunnel first.
    let warm = member(&fleet, X).forward(x_port).await.unwrap();
    assert!(blocking(move || peer::exchange(warm, 1, 1000, 1)).await.ok(1000));
    let from_y = member(&fleet, Y).forward(x_port).await.unwrap();
    let (result, elapsed) = blocking(move || {
        let mut s = std::net::TcpStream::connect(from_y).unwrap();
        s.write_all(&[1, 2, 3]).unwrap();
        s.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
        let start = Instant::now();
        let mut b = [0u8; 1];
        let r = s.read(&mut b);
        (r, start.elapsed())
    })
    .await;
    match result {
        Err(e) => assert_eq!(e.kind(), std::io::ErrorKind::ConnectionReset, "ECONNRESET, not a timeout or EOF: {}", e),
        Ok(n) => panic!("the black-box client must see the connection reset, read {}", n),
    }
    assert!(elapsed < Duration::from_secs(1), "reset well under a second, took {:?}", elapsed);
    assert_eq!(behind.accepted.load(Ordering::SeqCst), 1, "the refused tunnel opened no local connection behind the port");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn unreachable_jwks_refuses_everyone_until_keys_arrive() {
    let fleet = Fleet::new();
    let none: Arc<RwLock<std::collections::HashMap<String, Arc<jwk::ImmutableJwk>>>> = Default::default();
    let lookup = {
        let none = none.clone();
        Arc::new(move |kid: &str| none.read().unwrap().get(kid).cloned()) as memberport::JwkLookup
    };
    let b = member_with(&fleet, Minter::new(&fleet, X, 300, 60).source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, true))), lookup);
    let echo = Server::echo();
    let fwd = member(&fleet, X).forward(b.listen(loopback0(), echo.addr).await.unwrap()).await.unwrap();
    let refused = blocking(move || peer::exchange(fwd, 61, 1000, 61)).await;
    assert!(!refused.ok(1000), "no JWKS, no admission: {:?}", refused);
    assert_eq!(echo.accepted.load(Ordering::SeqCst), 0);
    none.write().unwrap().extend(fleet.jwks.read().unwrap().clone());
    let admitted = blocking(move || peer::exchange(fwd, 62, 1000, 62)).await;
    assert!(admitted.ok(1000), "admitted once the keys arrive: {:?}", admitted);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn certificate_rotates_mid_stream() {
    let fleet = Fleet::new();
    let b_cert = Arc::new(RwLock::new(fleet.leaf(X, SVC, true)));
    let b = member_with(&fleet, Minter::new(&fleet, X, 300, 60).source(), &b_cert, fleet.jwk_lookup());
    let server = Server::push(71, 300_000, 72);
    let b_addr = b.listen(loopback0(), server.addr).await.unwrap();
    let fwd = member(&fleet, X).forward(b_addr).await.unwrap();
    let before = presented_leaf(b_addr).await;
    let stream = blocking_spawn(move || peer::exchange_with(fwd, 72, 300_000, 71, 2048, Duration::from_millis(10), 0));
    tokio::time::sleep(Duration::from_millis(500)).await;
    *b_cert.write().unwrap() = fleet.leaf(X, SVC, false);
    let r = tokio::time::timeout(Duration::from_secs(60), stream).await.unwrap().unwrap();
    assert!(r.ok(300_000), "the stream in flight survives a rotation: {:?}", r);
    assert_ne!(before, presented_leaf(b_addr).await, "a new connection gets the new certificate");
    let echo = Server::echo();
    let after = member(&fleet, X).forward(b.listen(loopback0(), echo.addr).await.unwrap()).await.unwrap();
    let r = blocking(move || peer::exchange(after, 73, 5000, 73)).await;
    assert!(r.ok(5000), "members still connect after the rotation: {:?}", r);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn same_key_type_rotations_keep_every_handshake_working() {
    // EC to EC, several times, with many handshakes in between.
    let fleet = Fleet::new();
    let b_cert = Arc::new(RwLock::new(fleet.leaf(X, SVC, false)));
    let b = member_with(&fleet, Minter::new(&fleet, X, 300, 60).source(), &b_cert, fleet.jwk_lookup());
    let echo = Server::echo();
    let b_addr = b.listen(loopback0(), echo.addr).await.unwrap();
    for round in 0..4u64 {
        let expected = b_cert.read().unwrap().cert[0].as_ref().to_vec();
        assert_eq!(expected, presented_leaf(b_addr).await, "round {} serves the current certificate", round);
        // A new forward is a new channel: its handshake sees this round's certificate.
        let fwd = member(&fleet, X).forward(b_addr).await.unwrap();
        for i in 0..12u64 {
            let seed = round * 100 + i;
            let r = blocking(move || peer::exchange(fwd, seed, 1000, seed)).await;
            assert!(r.ok(1000), "round {} connection {}: {:?}", round, i, r);
        }
        *b_cert.write().unwrap() = fleet.leaf(X, SVC, false);
    }
}

// ==== The sender checks the receiver ====

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn receiver_certificate_of_another_tenant_is_refused_by_sender() {
    let fleet = Fleet::new();
    // B holds tenant X's tokens but presents a certificate naming tenant Y.
    let b = member_with(&fleet, Minter::new(&fleet, X, 300, 60).source(), &Arc::new(RwLock::new(fleet.leaf(Y, SVC, true))), fleet.jwk_lookup());
    let echo = Server::echo();
    let fwd = member(&fleet, X).forward(b.listen(loopback0(), echo.addr).await.unwrap()).await.unwrap();
    let r = blocking(move || peer::exchange(fwd, 81, 1000, 81)).await;
    assert!(!r.ok(1000) && r.received == 0, "refused at the handshake: {:?}", r);
    assert_eq!(echo.accepted.load(Ordering::SeqCst), 0);
    // A certificate of another service is refused the same way.
    let c = member_with(&fleet, Minter::new(&fleet, X, 300, 60).source(), &Arc::new(RwLock::new(fleet.leaf(X, "other", false))), fleet.jwk_lookup());
    let echo2 = Server::echo();
    let fwd2 = member(&fleet, X).forward(c.listen(loopback0(), echo2.addr).await.unwrap()).await.unwrap();
    let r2 = blocking(move || peer::exchange(fwd2, 82, 1000, 82)).await;
    assert!(!r2.ok(1000) && r2.received == 0, "another service's certificate: {:?}", r2);
    assert_eq!(echo2.accepted.load(Ordering::SeqCst), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn receiver_token_of_another_tenant_is_refused_by_sender() {
    let fleet = Fleet::new();
    let data_chunks = Arc::new(AtomicUsize::new(0));
    let receiver = fake_receiver(&fleet, fleet.mint(SVC, Y, &aud(), 300), FakeMode::Count, data_chunks.clone()).await;
    let fwd = member(&fleet, X).forward(receiver).await.unwrap();
    let r = blocking(move || peer::exchange(fwd, 91, 1000, 91)).await;
    assert!(!r.ok(1000) && r.received == 0, "refused: {:?}", r);
    tokio::time::sleep(Duration::from_millis(300)).await;
    assert_eq!(data_chunks.load(Ordering::SeqCst), 0, "the sender sends no bytes to a receiver it refused");
    // The receiver's own valid member token is admitted: the same fake then works.
    let admitted_chunks = Arc::new(AtomicUsize::new(0));
    let ok_receiver = fake_receiver(&fleet, fleet.mint(SVC, X, &aud(), 300), FakeMode::Count, admitted_chunks.clone()).await;
    let ok = member(&fleet, X).forward(ok_receiver).await.unwrap();
    let held = blocking(move || {
        let mut s = std::net::TcpStream::connect(ok).unwrap();
        s.write_all(&[1, 2, 3]).unwrap();
        s
    })
    .await;
    wait_for("bytes to reach the admitted receiver", Duration::from_secs(5), || admitted_chunks.load(Ordering::SeqCst) > 0).await;
    drop(held);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn two_tenants_members_never_connect() {
    let fleet = Fleet::new();
    let x1 = member(&fleet, X);
    let x2 = member(&fleet, X);
    let y1 = member(&fleet, Y);
    let behind_x2 = Server::echo();
    let x2_port = x2.listen(loopback0(), behind_x2.addr).await.unwrap();
    let same = x1.forward(x2_port).await.unwrap();
    assert!(blocking(move || peer::exchange(same, 101, 5000, 101)).await.ok(5000), "same tenant connects");
    let cross_fwd = y1.forward(x2_port).await.unwrap();
    let cross = blocking(move || peer::exchange(cross_fwd, 102, 5000, 102)).await;
    assert!(!cross.ok(5000) && cross.received == 0, "tenant Y cannot reach tenant X: {:?}", cross);
    assert_eq!(behind_x2.accepted.load(Ordering::SeqCst), 1, "only tenant X's connection reached the peer");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn target_and_local_forward_must_be_loopback() {
    let fleet = Fleet::new();
    let a = member(&fleet, X);
    assert!(a.listen(loopback0(), "192.0.2.1:5701".parse().unwrap()).await.is_err());
    assert!(a.forward_on("127.0.0.1:1".parse().unwrap(), "0.0.0.0:0".parse().unwrap()).await.is_err());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn close_drops_every_session_with_a_reset() {
    let fleet = Fleet::new();
    let a = member(&fleet, X);
    let echo = Server::echo();
    let fwd = tunnel(&a, &member(&fleet, X), &echo).await;
    let run = blocking_spawn(move || peer::exchange_with(fwd, 111, 2_000_000, 111, 2048, Duration::from_millis(10), 0));
    tokio::time::sleep(Duration::from_millis(500)).await;
    a.close();
    let r = tokio::time::timeout(Duration::from_secs(5), run).await.unwrap().unwrap();
    assert!(!r.ok(2_000_000) && r.first_mismatch.is_none() && r.reset(), "closed with a reset: {:?}", r);
    assert!(a.forward(fwd).await.is_err(), "a closed port forwards nothing more");
}
