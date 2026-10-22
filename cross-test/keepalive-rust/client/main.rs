// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// keepalive-rust driver (issue #29): the GENERATED RUST C2S CLIENT
// (c2sm/internal/grpc/clients/contract/contractclient/contract_api.rs) driving
// the generated Rust ContractApi server, over a channel that pings every
// keepaliveconst.TimeSecs while idle.
//
// The channel is dialled through a local proxy that counts TCP connections. A
// GOAWAY (too_many_pings) or a missed PING ACK tears the connection down, and
// tonic redials it on the next call, so the count rises above 1 and the run
// fails. A healthy server holds the one connection for the whole idle hold.
//
// Reads TACPLUS_SERVER_ADDR (host:port of the fixture server) and optionally:
//   TACPLUS_KEEPALIVE_HOLD_SECS       idle hold between the two calls (default 600)
//   TACPLUS_KEEPALIVE_CUT_AFTER_SECS  control run only: the proxy severs every
//                                     connection after this many seconds, so the
//                                     assertion must fail

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use contractclient_model::c2sm::internal::grpc::clients::grpc_clients::TokenProvider;
use contractclient_model::c2sm::ServiceManager;
use contractclient_model::types::contract::contractclient::{
    ContractMode, ContractRequest,
};
use tokio::io::copy_bidirectional;
use tokio::net::{TcpListener, TcpStream};

const FIXTURE_TOKEN: &str = "fixture-token";

// Mirror keepaliveconst.TimeSecs and keepaliveconst.TimeoutSecs, the values the
// generated Rust clients build their channels with (see the endpoint and
// client_im templates).
const KEEPALIVE_TIME_SECS: u64 = 30;
const KEEPALIVE_TIMEOUT_SECS: u64 = 10;
const DEFAULT_HOLD_SECS: u64 = 600;

fn fail(message: String) -> ! {
    eprintln!("keepalive-rust client FAILED: {}", message);
    std::process::exit(1);
}

fn valid_request() -> ContractRequest {
    ContractRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        item_id: "item-123".to_string(),
        mode: ContractMode::Read as i32,
        ..Default::default()
    }
}

// Accepts each client connection, counts it and relays bytes to the fixture.
async fn proxy(listener: TcpListener, upstream: String, opened: Arc<AtomicUsize>, cut_after: Option<Duration>) {
    loop {
        let Ok((mut downstream, _)) = listener.accept().await else { return };
        opened.fetch_add(1, Ordering::SeqCst);
        let upstream = upstream.clone();
        tokio::spawn(async move {
            let Ok(mut server) = TcpStream::connect(&upstream).await else { return };
            let relay = copy_bidirectional(&mut downstream, &mut server);
            match cut_after {
                Some(after) => {
                    let _ = tokio::time::timeout(after, relay).await;
                }
                None => {
                    let _ = relay.await;
                }
            }
        });
    }
}

fn env_secs(name: &str) -> Option<u64> {
    std::env::var(name).ok().map(|v| {
        v.parse().unwrap_or_else(|_| fail(format!("{} must be whole seconds, got {}", name, v)))
    })
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let fixture = std::env::var("TACPLUS_SERVER_ADDR")
        .unwrap_or_else(|_| fail("TACPLUS_SERVER_ADDR is required".to_string()));
    let hold = Duration::from_secs(env_secs("TACPLUS_KEEPALIVE_HOLD_SECS").unwrap_or(DEFAULT_HOLD_SECS));
    let cut_after = env_secs("TACPLUS_KEEPALIVE_CUT_AFTER_SECS").map(Duration::from_secs);

    let listener = TcpListener::bind("127.0.0.1:0").await?;
    let proxy_addr = listener.local_addr()?;
    let opened = Arc::new(AtomicUsize::new(0));
    tokio::spawn(proxy(listener, fixture, opened.clone(), cut_after));

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", proxy_addr))?
        .connect_timeout(Duration::from_secs(5))
        .http2_keep_alive_interval(Duration::from_secs(KEEPALIVE_TIME_SECS))
        .keep_alive_while_idle(true)
        .keep_alive_timeout(Duration::from_secs(KEEPALIVE_TIMEOUT_SECS))
        .connect()
        .await?;
    let token_provider: TokenProvider =
        Arc::new(|| Box::pin(async { Ok(FIXTURE_TOKEN.to_string()) }));
    let sm = ServiceManager::new(channel, Some(token_provider));
    let api = sm.grpc().contractclient().contract_api_client();

    api.get_public_contract(valid_request())
        .await
        .unwrap_or_else(|e| fail(format!("GetPublicContract before the idle hold: {}", e)));
    println!("  connections after first call: {}", opened.load(Ordering::SeqCst));

    println!(
        "  idling {}s on one channel (keepalive {}s / timeout {}s)",
        hold.as_secs(),
        KEEPALIVE_TIME_SECS,
        KEEPALIVE_TIMEOUT_SECS
    );
    tokio::time::sleep(hold).await;

    // tonic redials a torn-down connection lazily, on the next call, so the
    // count is read after the second call: a redial during the hold shows up here.
    api.get_public_contract(valid_request())
        .await
        .unwrap_or_else(|e| fail(format!("GetPublicContract after the idle hold: {}", e)));

    let total = opened.load(Ordering::SeqCst);
    println!("  connections opened over the run: {}", total);
    if total != 1 {
        fail(format!(
            "channel was torn down and redialled {} time(s) during a {}s idle hold (want 0)",
            total - 1,
            hold.as_secs()
        ));
    }
    println!("keepalive-rust: one connection held for {}s of idle pings", hold.as_secs());
    Ok(())
}
