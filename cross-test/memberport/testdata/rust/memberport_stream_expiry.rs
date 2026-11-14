// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! The Rust member port's leg of stream-expiry (tacplus#260, #322): a member
//! tunnel stream closes at its opening token's exp like every other stream,
//! with no exception for member traffic. The twin of
//! MemberPortStreamExpiryTest.java.

mod support;

use std::sync::atomic::Ordering;
use std::sync::{Arc, RwLock};
use std::time::{Duration, Instant};

use support::peer::{self, Server};
use support::*;
use tonic::Code;

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_expired_unrotated_stream_ends_unauthenticated_at_exp_with_a_local_reset() {
    let fleet = Fleet::new();
    let behind = Server::echo();
    let port = member(&fleet, X).listen(loopback0(), behind.addr).await.unwrap();
    let ttl_secs = 3;
    let token = fleet.mint(SVC, X, &aud(), ttl_secs);
    let exp_millis = token::Token::parse(&token).unwrap().expiration().unwrap() * 1000;
    let mut r = Raw::open(&raw_channel(port), Some(&token));
    open_session(&mut r).await;
    r.send(data(&[1, 2, 3])).await;
    assert_eq!(r.next().await.payload, vec![1, 2, 3], "the stream works until exp");
    let s = r.status(Duration::from_millis(ttl_secs as u64 * 1000 + 5_000)).await;
    let late = now_millis() - exp_millis;
    assert_refused(Code::Unauthenticated, &s, "an expired, unrotated stream");
    assert!(late > -250 && late < 1_500, "closed at exp, not before or long after: {}ms after it", late);
    wait_for("the connection behind the port to end", Duration::from_secs(5), || behind.result_count() == 1).await;
    let ended = behind.result(1).unwrap();
    assert!(ended.reset(), "the connection behind the port is reset: {:?}", ended);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_member_that_does_not_rotate_is_closed_at_expiry() {
    let fleet = Fleet::new();
    let stuck = Minter::new(&fleet, X, 3, 1);
    let a = member_with(&fleet, stuck.source(), &Arc::new(RwLock::new(fleet.leaf(X, SVC, true))), fleet.jwk_lookup());
    let echo = Server::echo();
    let fwd = tunnel(&a, &member(&fleet, X), &echo).await;
    stuck.token().await;
    stuck.never_renew.store(true, Ordering::SeqCst);
    let start = Instant::now();
    let r = blocking(move || peer::exchange_with(fwd, 41, 2_000_000, 41, 2048, Duration::from_millis(10), 0)).await;
    let elapsed = start.elapsed();
    assert!(!r.ok(2_000_000) && r.first_mismatch.is_none(), "cut at expiry, what arrived intact: {:?}", r);
    assert!(r.reset(), "the black-box client sees a reset: {:?}", r);
    assert!(elapsed < Duration::from_millis(4_500), "closed at the token's expiry, took {:?}", elapsed);
}
