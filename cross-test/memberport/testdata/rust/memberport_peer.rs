// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! The peer itself (#919): a stream is bounded by its progress, never by its
//! total time, so a slowed peer finishes however long it takes, and a stalled
//! one fails with the bound named.

mod support;

use std::net::TcpListener;
use std::sync::atomic::Ordering;
use std::sync::{Mutex, MutexGuard};
use std::thread;
use std::time::{Duration, Instant};

use support::peer::{self, Mode, Server, STALL_MS};

/// Sets the stall bound for one test at a time, and restores it however the
/// test ends.
struct Stall(u64, #[allow(dead_code)] MutexGuard<'static, ()>);

static ONE_AT_A_TIME: Mutex<()> = Mutex::new(());

impl Stall {
    fn set(ms: u64) -> Self {
        let guard = ONE_AT_A_TIME.lock().unwrap_or_else(|e| e.into_inner());
        Self(STALL_MS.swap(ms, Ordering::SeqCst), guard)
    }
}

impl Drop for Stall {
    fn drop(&mut self) {
        STALL_MS.store(self.0, Ordering::SeqCst);
    }
}

#[test]
fn a_slowed_stream_outlasts_the_stall_bound() {
    let _stall = Stall::set(300);
    // The client sends its few bytes at once and reads slowly while the server
    // pushes slowly, so the server's push outlives its read side too.
    let server = Server::start(Mode::Push, 61, 40 * 1024, 62, 1024, Duration::from_millis(50));
    let start = Instant::now();
    let r = peer::exchange_with(server.addr, 62, 4096, 61, 4096, Duration::ZERO, 20);
    let elapsed = start.elapsed();
    assert!(r.ok(40 * 1024), "a slowed stream, {:?} long against a 300ms stall bound: {:?}", elapsed, r);
    assert!(elapsed > Duration::from_millis(900), "the stream must outlast the stall bound several times over, took {:?}", elapsed);
    let deadline = Instant::now() + Duration::from_secs(10);
    while server.result_count() == 0 {
        assert!(Instant::now() < deadline, "timed out waiting for the server's result");
        thread::sleep(Duration::from_millis(20));
    }
    let sr = server.result(1).unwrap();
    assert!(sr.ok(4096), "server: {:?}", sr);
}

#[test]
fn a_stalled_peer_is_cut_at_the_stall_bound() {
    let _stall = Stall::set(300);
    // Accepts (the backlog does), then neither reads nor writes.
    let silent = TcpListener::bind("127.0.0.1:0").expect("bind");
    let start = Instant::now();
    let r = peer::exchange_with(silent.local_addr().unwrap(), 1, 1024, 2, 1024, Duration::ZERO, 0);
    let elapsed = start.elapsed();
    assert!(r.error.as_deref().is_some_and(|e| e.contains("stall bound")), "a stalled peer: {:?}", r);
    assert!(elapsed < Duration::from_secs(5), "a stalled peer is cut at the stall bound, took {:?}", elapsed);
}
