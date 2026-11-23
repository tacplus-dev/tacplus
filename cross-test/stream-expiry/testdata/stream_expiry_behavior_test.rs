// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Stream credential expiry (issue #260), Rust leg. Copied into the generated
// s2sm module by cross-test/stream-expiry/run.sh.
//
// The REAL generated pieces are served: the generated StreamerService
// ValidationWrapper, wired to the generated grpcserver Authorizer (through the
// ServiceManager's own AuthorizerAdapter) and behind the tower layers the
// generated ServiceManager installs, built from the ServiceManager's generated
// streaming-path list. Plaintext on 127.0.0.1: the TLS acceptor is not what is
// under test, and the layers sit above it.
//
// A client opens one stream of each shape with a token that expires ~3 s out
// and is never rotated, and keeps it busy. The server must end each one at the
// token's exp with UNAUTHENTICATED. A control stream opened with a long-lived
// token must stay open past that window.

use std::pin::Pin;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::StreamExt;
use key::keys;
use tokio_stream::Stream;

use crate::s2sm::internal::grpc::servers::svc::streamexpiry::streamer_service::{
    StreamerServiceHandler, ValidationWrapper,
};
use crate::types::svc::streamexpiry::streamer_service_client::StreamerServiceClient;
use crate::types::svc::streamexpiry::streamer_service_server::StreamerServiceServer;
use crate::types::svc::streamexpiry::Tick;

type TickStream = Pin<Box<dyn Stream<Item = Result<Tick, tonic::Status>> + Send>>;

/// The audience the generated Authorizer accepts (ACCEPTED_AUDIENCES).
const AUDIENCE: &str = "streamexpiry";
/// How far out the short-lived token expires.
const SHORT_TTL_SECS: i64 = 3;
/// A stream may end no earlier than this before exp (whole-second exp claim).
const EARLY_SLACK: Duration = Duration::from_secs(1);
/// ...and no later than this after exp.
const LATE_SLACK: Duration = Duration::from_secs(5);
/// Hard bound per stream so a stream the server never ends fails the test.
const HARD_BOUND: Duration = Duration::from_secs(SHORT_TTL_SECS as u64 + 8);
const TICK_EVERY: Duration = Duration::from_millis(200);
/// How long the control stream may go without a tick before it counts as
/// stalled. Generous on purpose: on a loaded runner every step runs late
/// (issue #627); a stream the server ended still fails at once.
const CONTROL_STALL: Duration = LATE_SLACK;

fn now_secs() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).expect("clock").as_secs() as i64
}

fn exp_instant(exp: i64) -> SystemTime {
    UNIX_EPOCH + Duration::from_secs(exp as u64)
}

fn jwk_for_key(key: &keys::ImmutableKey) -> jwk::ImmutableJwk {
    let x = match key.public_key() {
        keys::PublicKey::Ed25519(ed) => {
            use base64::Engine;
            base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(ed.as_bytes())
        }
        _ => panic!("expected ed25519 public key"),
    };
    jwk::ImmutableJwk::new(jwk::jwk::Jwk {
        kty: "OKP".to_string(),
        alg: Some("EdDSA".to_string()),
        kid: key.id().to_string(),
        key_use: Some("sig".to_string()),
        n: None,
        e: None,
        crv: Some("Ed25519".to_string()),
        x: Some(x),
        y: None,
        exp: None,
        created: None,
    })
    .expect("build jwk")
}

/// A signed service token (sub_type service) for AUDIENCE, as a bearer value.
fn service_bearer(key: &keys::ImmutableKey, exp: i64) -> String {
    let mut tok = token::Token::new();
    tok.set_issuer("test-issuer").expect("issuer");
    tok.set_audience(AUDIENCE).expect("audience");
    tok.set_expiration(exp).expect("expiration");
    tok.set_namespace("/tenants/acme").expect("namespace");
    tok.set_sub_type("service").expect("sub_type");
    tok.set_subject("caller-svc").expect("subject");
    tok.sign(key).expect("sign");
    format!("Bearer {}", tok.base64().expect("signed token has base64"))
}

fn tick(m: &str) -> Tick {
    Tick { m: m.to_string(), ..Default::default() }
}

/// An endless client-side tick stream.
fn endless_ticks() -> impl Stream<Item = Tick> + Send + 'static {
    futures::stream::unfold(0u64, |n| async move {
        tokio::time::sleep(TICK_EVERY).await;
        Some((tick(&format!("c{n}")), n + 1))
    })
}

/// A handler that keeps every stream open for as long as the server lets it.
struct KeepOpenHandler;

#[tonic::async_trait]
impl StreamerServiceHandler for KeepOpenHandler {
    async fn send_ticks(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<TickStream>,
    ) -> Result<tonic::Response<Tick>, tonic::Status> {
        // Keep receiving; blocked in the stream's next() between ticks.
        let mut inbound = req.into_inner();
        let mut n = 0u64;
        while let Some(item) = inbound.next().await {
            item?;
            n += 1;
        }
        Ok(tonic::Response::new(tick(&format!("received {n}"))))
    }

    async fn generate_ticks(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: Tick,
    ) -> Result<tonic::Response<TickStream>, tonic::Status> {
        let out = futures::stream::unfold(0u64, |n| async move {
            tokio::time::sleep(TICK_EVERY).await;
            Some((Ok(tick(&format!("s{n}"))), n + 1))
        });
        Ok(tonic::Response::new(Box::pin(out) as TickStream))
    }

    async fn assist_ticks(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<TickStream>,
    ) -> Result<tonic::Response<TickStream>, tonic::Status> {
        // Echo every inbound tick; the output lives as long as the input.
        let out = req.into_inner().map(|item| item.map(|t| tick(&format!("echo {}", t.m))));
        Ok(tonic::Response::new(Box::pin(out) as TickStream))
    }
}

/// Serves the generated wrapper the way the generated ServiceManager does and
/// returns the bound address.
async fn serve(key: &keys::ImmutableKey) -> std::net::SocketAddr {
    let authorizer = Arc::new(grpcserver::authorizer::Authorizer::with_seeded_jwks(vec![jwk_for_key(key)]));
    crate::s2sm::internal::grpc::servers::svc::streamexpiry::streamer_service::register_method_auth(&authorizer);

    let wrapper = ValidationWrapper::new(Arc::new(KeepOpenHandler));
    let adapter = Arc::new(super::AuthorizerAdapter { authorizer: authorizer.clone() });
    wrapper.set_authorize_func(Some(adapter)).await;
    assert!(wrapper.has_authorizer().await, "wrapper must be wired to the authorizer");

    let incoming = tonic::transport::server::TcpIncoming::bind("127.0.0.1:0".parse().unwrap())
        .expect("bind 127.0.0.1:0");
    let addr = incoming.local_addr().expect("local addr");

    // The layers the generated ServiceManager installs on its TLS server, in
    // its order, the expiry layer fed the ServiceManager's own generated list.
    assert_eq!(
        super::STREAMING_RPC_PATHS,
        &[
            "/streamexpiry.StreamerService/SendTicks",
            "/streamexpiry.StreamerService/GenerateTicks",
            "/streamexpiry.StreamerService/AssistTicks",
        ],
        "generated STREAMING_RPC_PATHS must list exactly the three streaming rpcs"
    );
    let server = tonic::transport::Server::builder().layer(grpcserver::authorizer::AuthEnforcingLayer::with_exempt_paths(
        authorizer.clone(),
        Vec::<String>::new(),
    ));
    let mut server = server.layer(grpcserver::authorizer::StreamCredentialExpiryLayer::new(
        super::STREAMING_RPC_PATHS.iter().map(|p| p.to_string()),
    ));
    let router = server.add_service(servicemanager::deadline::InboundDeadline::new(StreamerServiceServer::new(wrapper)));
    tokio::spawn(async move {
        router.serve_with_incoming(incoming).await.expect("serve");
    });
    addr
}

async fn client(addr: std::net::SocketAddr) -> StreamerServiceClient<tonic::transport::Channel> {
    let endpoint = tonic::transport::Endpoint::from_shared(format!("http://{addr}")).expect("endpoint");
    for _ in 0..50 {
        if let Ok(ch) = endpoint.connect().await {
            return StreamerServiceClient::new(ch);
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("could not connect to {addr}");
}

fn with_auth<T>(msg: T, bearer: &str) -> tonic::Request<T> {
    let mut req = tonic::Request::new(msg);
    req.metadata_mut().insert(
        "authorization",
        tonic::metadata::MetadataValue::try_from(bearer).expect("bearer metadata"),
    );
    req
}

/// How a stream ended: the status and when.
type Ended = Result<(tonic::Status, SystemTime), String>;

async fn run_send_ticks(mut c: StreamerServiceClient<tonic::transport::Channel>, bearer: String) -> Ended {
    match c.send_ticks(with_auth(endless_ticks(), &bearer)).await {
        Ok(resp) => Err(format!("SendTicks completed OK instead of being ended: {:?}", resp.into_inner())),
        Err(st) => Ok((st, SystemTime::now())),
    }
}

async fn drain(name: &str, mut s: tonic::Streaming<Tick>) -> Ended {
    loop {
        match s.message().await {
            Ok(Some(_)) => continue,
            Ok(None) => return Err(format!("{name} ended cleanly (status OK) instead of UNAUTHENTICATED")),
            Err(st) => return Ok((st, SystemTime::now())),
        }
    }
}

async fn run_generate_ticks(mut c: StreamerServiceClient<tonic::transport::Channel>, bearer: String) -> Ended {
    match c.generate_ticks(with_auth(tick("go"), &bearer)).await {
        Ok(resp) => drain("GenerateTicks", resp.into_inner()).await,
        Err(st) => Ok((st, SystemTime::now())),
    }
}

async fn run_assist_ticks(mut c: StreamerServiceClient<tonic::transport::Channel>, bearer: String) -> Ended {
    match c.assist_ticks(with_auth(endless_ticks(), &bearer)).await {
        Ok(resp) => drain("AssistTicks", resp.into_inner()).await,
        Err(st) => Ok((st, SystemTime::now())),
    }
}

fn check_ended(name: &str, exp: i64, ended: Result<Ended, tokio::time::error::Elapsed>) -> Result<(), String> {
    let ended = ended.map_err(|_| {
        format!("{name}: still open {HARD_BOUND:?} after open, well past its token's exp (server never ended it)")
    })?;
    let (status, at) = ended.map_err(|e| format!("{name}: {e}"))?;
    if status.code() != tonic::Code::Unauthenticated {
        return Err(format!("{name}: ended with {:?} ({}), want UNAUTHENTICATED", status.code(), status.message()));
    }
    let exp_at = exp_instant(exp);
    if at + EARLY_SLACK < exp_at {
        return Err(format!(
            "{name}: ended {:?} before exp, earlier than the {EARLY_SLACK:?} allowance ({})",
            exp_at.duration_since(at).unwrap_or_default(),
            status.message()
        ));
    }
    if at > exp_at + LATE_SLACK {
        return Err(format!(
            "{name}: ended {:?} after exp, later than the {LATE_SLACK:?} allowance",
            at.duration_since(exp_at).unwrap_or_default()
        ));
    }
    eprintln!(
        "{name}: ended UNAUTHENTICATED ({}) at exp{:+.2}s",
        status.message(),
        match at.duration_since(exp_at) {
            Ok(d) => d.as_secs_f64(),
            Err(e) => -e.duration().as_secs_f64(),
        }
    );
    Ok(())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_stream_expiry_ends_every_stream_shape_at_exp() {
    let key = keys::generate_ed25519_key().expect("generate ed25519 key");
    let addr = serve(&key).await;
    let c = client(addr).await;

    let exp = now_secs() + SHORT_TTL_SECS;
    let short = service_bearer(&key, exp);
    let long = service_bearer(&key, now_secs() + 3600);

    let send = tokio::spawn(tokio::time::timeout(HARD_BOUND, run_send_ticks(c.clone(), short.clone())));
    let gen = tokio::spawn(tokio::time::timeout(HARD_BOUND, run_generate_ticks(c.clone(), short.clone())));
    let assist = tokio::spawn(tokio::time::timeout(HARD_BOUND, run_assist_ticks(c.clone(), short.clone())));

    // Control: a long-lived token's stream must outlive the whole window.
    let control_until = exp_instant(exp) + LATE_SLACK + Duration::from_secs(1);
    let control = tokio::spawn({
        let mut c = c.clone();
        async move {
            let resp = c
                .generate_ticks(with_auth(tick("control"), &long))
                .await
                .map_err(|st| format!("control GenerateTicks refused at open: {st}"))?;
            let mut s = resp.into_inner();
            let mut got = 0u64;
            while SystemTime::now() < control_until {
                let waiting = SystemTime::now();
                match tokio::time::timeout(CONTROL_STALL, s.message()).await {
                    Ok(Ok(Some(_))) => got += 1,
                    Ok(Ok(None)) => return Err("control stream ended cleanly inside the window".to_string()),
                    Ok(Err(st)) => return Err(format!("control stream (long-lived token) was ended: {st}")),
                    Err(_) => {
                        return Err(format!(
                            "control stream stalled: no tick for {:?} (bound {CONTROL_STALL:?}) after {got} ticks",
                            waiting.elapsed().unwrap_or_default()
                        ))
                    }
                }
            }
            Ok::<u64, String>(got)
        }
    });

    let mut failures = Vec::new();
    for (name, h) in [("SendTicks (client-stream)", send), ("GenerateTicks (server-stream)", gen), ("AssistTicks (bidi)", assist)] {
        if let Err(e) = check_ended(name, exp, h.await.expect("stream task panicked")) {
            failures.push(e);
        }
    }
    match control.await.expect("control task panicked") {
        Ok(got) => eprintln!("control: long-lived stream still open past exp+{LATE_SLACK:?} ({got} ticks)"),
        Err(e) => failures.push(e),
    }
    assert!(failures.is_empty(), "stream credential expiry failures:\n  {}", failures.join("\n  "));
}
