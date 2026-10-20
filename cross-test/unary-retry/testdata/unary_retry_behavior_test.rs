// Behavior of the generated Rust unary client retry path (issue #57). Included as a
// child module of retry_service.rs so the jitter check reaches outbound_backoff.
use super::*;
use crate::types::contract::unaryretry::retry_service_server::{RetryService, RetryServiceServer};
use crate::types::contract::unaryretry::{Ping, PingList};
use std::collections::HashSet;
use std::sync::atomic::{AtomicUsize, Ordering};
use tonic::transport::{Channel, Endpoint, Server};

// DownPeer fails every list_pings with UNAVAILABLE and counts the attempts that reach it.
struct DownPeer {
    attempts: AtomicUsize,
}

#[tonic::async_trait]
impl RetryService for DownPeer {
    async fn list_pings(&self, _request: tonic::Request<Ping>) -> Result<tonic::Response<PingList>, tonic::Status> {
        self.attempts.fetch_add(1, Ordering::SeqCst);
        Err(tonic::Status::unavailable("peer down"))
    }
}

fn token_ok() -> Option<TokenProvider> {
    Some(Arc::new(|| Box::pin(async { Ok("test-token".to_string()) })))
}

// serve_down_peer starts the outage peer on a local port and returns its counter
// with a lazily connected channel to it.
async fn serve_down_peer() -> (Arc<DownPeer>, Channel) {
    let peer = Arc::new(DownPeer {
        attempts: AtomicUsize::new(0),
    });
    let probe = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = probe.local_addr().unwrap();
    drop(probe);
    let server = RetryServiceServer::from_arc(peer.clone());
    tokio::spawn(Server::builder().add_service(server).serve(addr));
    tokio::time::sleep(Duration::from_millis(100)).await;
    let channel = Endpoint::from_shared(format!("http://{addr}")).unwrap().connect_lazy();
    (peer, channel)
}

// A sustained outage must not multiply load. Fifty callers each allow two retries,
// so unbounded retry sends 150 attempts. The budget admits a burst of ten retries
// and refills only on success, so 50 first attempts plus at most 10 retries reach the peer.
#[tokio::test(flavor = "multi_thread")]
async fn rust_unary_retry_budget_caps_retries_during_outage() {
    let (peer, channel) = serve_down_peer().await;
    let client = Arc::new(
        RetryServiceGrpcClient::new(ChannelProvider::from_channel("unaryretry", channel), token_ok())
            .with_retry(2, Duration::from_millis(1)),
    );

    let mut calls = Vec::new();
    for _ in 0..50 {
        let client = client.clone();
        calls.push(tokio::spawn(async move {
            let _ = client.list_pings(Ping { m: "x".to_string(), ..Default::default() }).await;
        }));
    }
    for call in calls {
        call.await.unwrap();
    }

    let got = peer.attempts.load(Ordering::SeqCst);
    assert!(
        got <= 60,
        "outage saw {got} attempts for 50 calls; want at most 60 (first attempts plus the 10-retry budget)"
    );
}

// The wait for a given attempt is jittered, so repeated draws for the same attempt differ.
#[tokio::test]
async fn rust_unary_retry_backoff_is_jittered() {
    let (_peer, channel) = serve_down_peer().await;
    let client = RetryServiceGrpcClient::new(ChannelProvider::from_channel("unaryretry", channel), None)
        .with_retry(2, Duration::from_millis(100));

    let seen: HashSet<Duration> = (0..50).map(|_| client.outbound_backoff(0)).collect();
    assert!(
        seen.len() >= 2,
        "first-retry backoff is the fixed {:?} for every caller; want jitter",
        client.outbound_backoff(0)
    );
}
