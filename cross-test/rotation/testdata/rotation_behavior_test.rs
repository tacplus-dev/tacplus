// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Stream rotation (tacplus#322), Rust leg. Copied into the generated s2sm
// module by cross-test/rotation/run.sh; the Rust mirror of
// rotation_behavior_test.go.
//
// The real generated server (the generated TickerService ValidationWrapper
// and its generated rotating_sibling, wired to the generated grpcserver
// Authorizer through the ServiceManager's own AuthorizerAdapter, behind the
// tower layers the generated ServiceManager installs: AuthEnforcingLayer and
// #260's StreamCredentialExpiryLayer fed STREAMING_RPC_PATHS and
// ROTATING_RPC_PATHS) meets the real generated S2S client
// (s2sm/internal/grpc/clients), with tokens that live TOKEN_TTL. Plaintext on
// 127.0.0.1: the TLS acceptor is not under test, and the layers sit above it.
// The handler below and the callers in the transparency tests are plain
// handler and caller code: nothing in them mentions rotation, and each stream
// must outlive several token lifetimes with no loss, duplication or
// reordering. The fault tests then go to the wire.

use std::pin::Pin;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use futures::StreamExt;
use key::keys;
use tokio_stream::Stream;

use crate::s2sm::internal::grpc::clients::svc::rotationfixture::ticker_service::{
    TickerServiceGrpcClient, TokenProvider,
};
use crate::s2sm::internal::grpc::servers::svc::rotationfixture::ticker_service::{
    TickerServiceHandler, ValidationWrapper,
};
use crate::types::svc::rotationfixture::ticker_service_server::TickerServiceServer;
use crate::types::svc::rotationfixture::Tick;

type TickStream = Pin<Box<dyn Stream<Item = Result<Tick, tonic::Status>> + Send>>;

/// The audience the generated Authorizer accepts (ACCEPTED_AUDIENCES).
const AUDIENCE: &str = "rotationfixture";
/// Every token's lifetime. exp is whole seconds, so a token may expire up to a
/// second sooner.
const TOKEN_TTL_SECS: i64 = 3;
const TOKEN_TTL: Duration = Duration::from_secs(TOKEN_TTL_SECS as u64);
/// How long the transparency tests run a stream: several lifetimes.
const STREAM_FOR: Duration = Duration::from_secs(9);

fn now_secs() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).expect("clock").as_secs() as i64
}

// ---------------------------------------------------------------------------
// The handler: plain handler code, nothing about rotation.
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Default)]
struct HandlerEnd {
    method: &'static str,
    err: Option<tonic::Status>,
    /// The handler was dropped (cancelled) before it finished.
    cancelled: bool,
    received: Vec<i64>,
}

#[derive(Clone)]
struct Tracker {
    calls: Arc<AtomicUsize>,
    ended: Arc<Mutex<Vec<HandlerEnd>>>,
}

impl Tracker {
    fn new() -> Self {
        Self { calls: Arc::new(AtomicUsize::new(0)), ended: Arc::new(Mutex::new(Vec::new())) }
    }

    fn begin(&self, method: &'static str) -> EndGuard {
        self.calls.fetch_add(1, Ordering::SeqCst);
        EndGuard { tracker: self.clone(), end: HandlerEnd { method, ..Default::default() }, finished: false }
    }

    async fn handler_end(&self, within: Duration) -> HandlerEnd {
        let deadline = Instant::now() + within;
        loop {
            if let Some(end) = self.ended.lock().unwrap().pop() {
                return end;
            }
            if Instant::now() > deadline {
                panic!("the handler did not end within {within:?}");
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    }
}

/// Records how a handler's stream ended; dropped unfinished, it was cancelled.
struct EndGuard {
    tracker: Tracker,
    end: HandlerEnd,
    finished: bool,
}

impl EndGuard {
    fn finish(&mut self, err: Option<tonic::Status>) {
        self.end.err = err;
        self.finished = true;
    }
}

impl Drop for EndGuard {
    fn drop(&mut self) {
        let mut end = std::mem::take(&mut self.end);
        end.cancelled = !self.finished;
        self.tracker.ended.lock().unwrap().push(end);
    }
}

struct Ticker {
    tracker: Tracker,
}

#[tonic::async_trait]
impl TickerServiceHandler for Ticker {
    /// Receives ticks until the caller closes, and answers with how many
    /// arrived, in order.
    async fn send_ticks(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<TickStream>,
    ) -> Result<tonic::Response<Tick>, tonic::Status> {
        let mut guard = self.tracker.begin("SendTicks");
        let mut inbound = req.into_inner();
        while let Some(item) = inbound.next().await {
            match item {
                Ok(t) => guard.end.received.push(t.seq),
                Err(st) => {
                    guard.finish(Some(st.clone()));
                    return Err(st);
                }
            }
        }
        let mut verdict = "in-order".to_string();
        for (i, seq) in guard.end.received.iter().enumerate() {
            if *seq != i as i64 + 1 {
                verdict = format!("out of order at {i}: {seq}");
                break;
            }
        }
        let n = guard.end.received.len() as i64;
        guard.finish(None);
        Ok(tonic::Response::new(Tick { seq: n, m: verdict, ..Default::default() }))
    }

    /// Sends ticks 1..=req.seq every 50 ms (forever when req.seq is 0).
    async fn generate_ticks(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: Tick,
    ) -> Result<tonic::Response<TickStream>, tonic::Status> {
        let guard = self.tracker.begin("GenerateTicks");
        let n = req.seq;
        let out = futures::stream::unfold((1i64, guard), move |(seq, mut guard)| async move {
            if n != 0 && seq > n {
                guard.finish(None);
                return None;
            }
            tokio::time::sleep(Duration::from_millis(50)).await;
            Some((Ok(Tick { seq, m: "tick".to_string(), ..Default::default() }), (seq + 1, guard)))
        });
        Ok(tonic::Response::new(Box::pin(out) as TickStream))
    }

    /// Echoes every tick back.
    async fn assist_ticks(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<TickStream>,
    ) -> Result<tonic::Response<TickStream>, tonic::Status> {
        let guard = self.tracker.begin("AssistTicks");
        let inbound = req.into_inner();
        let out = futures::stream::unfold((inbound, guard), |(mut inbound, mut guard)| async move {
            match inbound.next().await {
                Some(Ok(t)) => {
                    guard.end.received.push(t.seq);
                    Some((Ok(Tick { seq: t.seq, m: "echo".to_string(), ..Default::default() }), (inbound, guard)))
                }
                Some(Err(st)) => {
                    guard.finish(Some(st.clone()));
                    None
                }
                None => {
                    guard.finish(None);
                    None
                }
            }
        });
        Ok(tonic::Response::new(Box::pin(out) as TickStream))
    }
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

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

struct Fixture {
    key: Arc<keys::ImmutableKey>,
    addr: std::net::SocketAddr,
    tracker: Tracker,
}

/// A signed service token for `subject`, audience rotationfixture, living TOKEN_TTL.
fn mint(key: &keys::ImmutableKey, subject: &str) -> String {
    let mut tok = token::Token::new();
    tok.set_issuer("test-issuer").expect("issuer");
    tok.set_audience(AUDIENCE).expect("audience");
    tok.set_expiration(now_secs() + TOKEN_TTL_SECS).expect("expiration");
    tok.set_namespace("/system").expect("namespace");
    tok.set_sub_type("service").expect("sub_type");
    tok.set_subject(subject).expect("subject");
    tok.sign(key).expect("sign");
    tok.base64().expect("signed token has base64").to_string()
}

/// The generated wrapper behind the generated Authorizer, wired as the
/// generated ServiceManager wires them.
async fn wrapper(key: &keys::ImmutableKey, tracker: &Tracker) -> (ValidationWrapper, Arc<grpcserver::authorizer::Authorizer>) {
    let authorizer = Arc::new(grpcserver::authorizer::Authorizer::with_seeded_jwks(vec![jwk_for_key(key)]));
    crate::s2sm::internal::grpc::servers::svc::rotationfixture::ticker_service::register_method_auth(&authorizer);
    let wrapper = ValidationWrapper::new(Arc::new(Ticker { tracker: tracker.clone() }));
    let adapter = Arc::new(super::AuthorizerAdapter { authorizer: authorizer.clone() });
    wrapper.set_authorize_func(Some(adapter)).await;
    assert!(wrapper.has_authorizer().await, "wrapper must be wired to the authorizer");
    (wrapper, authorizer)
}

/// Serves the generated TickerService and, unless `plain_only`, its sibling,
/// behind the layers the generated ServiceManager installs, in its order.
async fn serve(plain_only: bool) -> Fixture {
    let key = Arc::new(keys::generate_ed25519_key().expect("generate ed25519 key"));
    let tracker = Tracker::new();
    let (wrapper, authorizer) = wrapper(&key, &tracker).await;
    assert_eq!(
        super::ROTATING_RPC_PATHS,
        &[
            "/rotationfixture.TickerServiceRotating/SendTicks",
            "/rotationfixture.TickerServiceRotating/GenerateTicks",
            "/rotationfixture.TickerServiceRotating/AssistTicks",
        ],
        "generated ROTATING_RPC_PATHS must list the sibling's three methods"
    );
    let incoming = tonic::transport::server::TcpIncoming::bind("127.0.0.1:0".parse().unwrap()).expect("bind");
    let addr = incoming.local_addr().expect("local addr");
    let server = tonic::transport::Server::builder().layer(grpcserver::authorizer::AuthEnforcingLayer::with_exempt_paths(
        authorizer.clone(),
        Vec::<String>::new(),
    ));
    let mut server = server.layer(grpcserver::authorizer::StreamCredentialExpiryLayer::new(
        super::STREAMING_RPC_PATHS.iter().chain(super::ROTATING_RPC_PATHS.iter()).map(|p| p.to_string()),
    ));
    let plain = || servicemanager::deadline::InboundDeadline::new(TickerServiceServer::new(wrapper.clone()));
    let router = if plain_only {
        server.add_service(plain())
    } else {
        server.add_service(plain()).add_service(wrapper.rotating_sibling(plain()))
    };
    tokio::spawn(async move {
        router.serve_with_incoming(incoming).await.expect("serve");
    });
    Fixture { key, addr, tracker }
}

async fn connect(addr: std::net::SocketAddr) -> tonic::transport::Channel {
    let endpoint = tonic::transport::Endpoint::from_shared(format!("http://{addr}")).expect("endpoint");
    for _ in 0..100 {
        if let Ok(ch) = endpoint.connect().await {
            return ch;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("could not connect to {addr}");
}

/// The caller's token provider: caller-svc tokens, counted, until revoked
/// (then the mint fails) or impersonating (then another subject's).
#[derive(Clone)]
struct TokenSource {
    key: Arc<keys::ImmutableKey>,
    mints: Arc<AtomicUsize>,
    revoked: Arc<AtomicBool>,
    impersonate: Arc<Mutex<Option<String>>>,
}

impl TokenSource {
    fn provider(&self) -> TokenProvider {
        let src = self.clone();
        Arc::new(move || {
            let src = src.clone();
            Box::pin(async move {
                if src.revoked.load(Ordering::SeqCst) {
                    return Err("identity: caller revoked".into());
                }
                src.mints.fetch_add(1, Ordering::SeqCst);
                let subject = src.impersonate.lock().unwrap().clone().unwrap_or_else(|| "caller-svc".to_string());
                Ok(mint(&src.key, &subject))
            })
        })
    }

    fn mints(&self) -> usize {
        self.mints.load(Ordering::SeqCst)
    }
}

impl Fixture {
    /// The generated S2S client, over a fixed ChannelProvider.
    async fn client(&self, addr: std::net::SocketAddr) -> (TickerServiceGrpcClient, TokenSource) {
        let src = TokenSource {
            key: self.key.clone(),
            mints: Arc::new(AtomicUsize::new(0)),
            revoked: Arc::new(AtomicBool::new(false)),
            impersonate: Arc::new(Mutex::new(None)),
        };
        let cp = servicemanager::ChannelProvider::from_channel("rotationfixture", connect(addr).await);
        (TickerServiceGrpcClient::new(cp, Some(src.provider())), src)
    }
}

fn check_sequence(what: &str, got: &[i64], n: usize) {
    assert_eq!(got.len(), n, "{what}: got {} messages, want {n} (loss or duplication)", got.len());
    for (i, seq) in got.iter().enumerate() {
        assert_eq!(*seq, i as i64 + 1, "{what}: message {i} is seq {seq} (reordering, loss or duplication)");
    }
}

// ---------------------------------------------------------------------------
// Transparency: one stream, several token lifetimes, plain code on both ends.
// ---------------------------------------------------------------------------

/// The caller sends a tick every 2 ms for STREAM_FOR while the handler echoes
/// each back, both directions at once, across several token lifetimes. Every
/// tick arrives at the handler and back exactly once, in order, on one handler
/// call and one connect of the caller's stream; the tokens were renewed.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_bidi_both_ways_across_lifetimes() {
    let f = serve(false).await;
    let (c, src) = f.client(f.addr).await;
    let echoed = Arc::new(Mutex::new(Vec::<i64>::new()));
    let connects = Arc::new(AtomicUsize::new(0));
    let on_connect = {
        let connects = connects.clone();
        move |_| {
            connects.fetch_add(1, Ordering::SeqCst);
            async {}
        }
    };
    let on_message = {
        let echoed = echoed.clone();
        Arc::new(move |t: Tick| echoed.lock().unwrap().push(t.seq)) as Arc<dyn Fn(Tick) + Send + Sync>
    };
    let h = c.assist_ticks(on_connect, on_message);
    let start = Instant::now();
    let mut n = 0usize;
    while start.elapsed() < STREAM_FOR {
        n += 1;
        h.send(Tick { seq: n as i64, m: "up".to_string(), ..Default::default() }).await.expect("send");
        tokio::time::sleep(Duration::from_millis(2)).await;
    }
    let deadline = Instant::now() + Duration::from_secs(5);
    while echoed.lock().unwrap().len() < n && Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    drop(h);
    check_sequence("echoed", &echoed.lock().unwrap(), n);
    let end = f.tracker.handler_end(Duration::from_secs(5)).await;
    check_sequence("received by the handler", &end.received, n);
    // Dropping the handle ends the stream, as it always has: the handler sees
    // a clean end, or is cancelled if the caller's cancel won the race.
    if let Some(err) = &end.err {
        assert_eq!(err.code(), tonic::Code::Cancelled, "handler ended with {err:?}");
    }
    assert_eq!(f.tracker.calls.load(Ordering::SeqCst), 1, "one logical stream, one handler call");
    assert_eq!(connects.load(Ordering::SeqCst), 1, "the caller's stream must not reconnect");
    assert!(src.mints() >= 4, "{} tokens minted over {STREAM_FOR:?}: the stream did not rotate", src.mints());
}

/// 160 ticks at 50 ms, across several lifetimes, all delivered in order, then
/// a clean end.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_server_streaming_across_lifetimes() {
    let f = serve(false).await;
    let (c, src) = f.client(f.addr).await;
    const N: usize = 160;
    let mut s = c.generate_ticks(Tick { seq: N as i64, ..Default::default() }).await.expect("GenerateTicks").into_inner();
    let mut got = Vec::new();
    loop {
        match s.message().await {
            Ok(Some(t)) => got.push(t.seq),
            Ok(None) => break,
            Err(st) => panic!("Recv after {} ticks: {st:?}", got.len()),
        }
    }
    check_sequence("ticks", &got, N);
    let end = f.tracker.handler_end(Duration::from_secs(5)).await;
    assert!(end.err.is_none() && !end.cancelled, "handler ended with {end:?}, want a clean end");
    assert!(src.mints() >= 3, "{} tokens minted: the stream did not rotate", src.mints());
}

/// 160 ticks at 50 ms, across several lifetimes; the handler counts them all,
/// in order.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_client_streaming_across_lifetimes() {
    let f = serve(false).await;
    let (c, src) = f.client(f.addr).await;
    const N: i64 = 160;
    let s = c.send_ticks();
    for i in 1..=N {
        s.send(Tick { seq: i, m: "up".to_string(), ..Default::default() }).await.unwrap_or_else(|e| panic!("send {i}: {e:?}"));
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    let resp = s.finish().await.expect("finish").into_inner();
    assert_eq!((resp.seq, resp.m.as_str()), (N, "in-order"), "handler counted {resp:?}");
    assert!(src.mints() >= 3, "{} tokens minted: the stream did not rotate", src.mints());
}

// ---------------------------------------------------------------------------
// Fail closed
// ---------------------------------------------------------------------------

/// Runs an endless server stream and returns how it ended and when.
async fn ends_at(c: &TickerServiceGrpcClient, bound: Duration) -> Result<(tonic::Status, Instant), String> {
    let run = async {
        let mut s = match c.generate_ticks(Tick::default()).await {
            Ok(s) => s.into_inner(),
            Err(st) => return (st, Instant::now()),
        };
        loop {
            match s.message().await {
                Ok(Some(_)) => {}
                Ok(None) => return (tonic::Status::ok("ended cleanly"), Instant::now()),
                Err(st) => return (st, Instant::now()),
            }
        }
    };
    tokio::time::timeout(bound, run).await.map_err(|_| format!("still open at the test's bound {bound:?}"))
}

/// The caller gets its opening token and no other. The stream is not
/// rotated, and ends at the token's exp with UNAUTHENTICATED; the handler is
/// cancelled.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_mint_failure_ends_at_expiry() {
    let f = serve(false).await;
    let (c, src) = f.client(f.addr).await;
    let revoker = {
        let src = src.clone();
        tokio::spawn(async move {
            while src.mints() < 1 {
                tokio::time::sleep(Duration::from_millis(1)).await;
            }
            src.revoked.store(true, Ordering::SeqCst);
        })
    };
    let opened = Instant::now();
    let (st, ended) = ends_at(&c, TOKEN_TTL * 4).await.expect("the stream outlived its only token");
    revoker.abort();
    assert_eq!(st.code(), tonic::Code::Unauthenticated, "stream ended with {st:?}");
    let took = ended - opened;
    assert!(
        took + Duration::from_millis(1500) >= TOKEN_TTL && took <= TOKEN_TTL + Duration::from_secs(3),
        "stream ended {took:?} after open, want about one token lifetime"
    );
    let end = f.tracker.handler_end(Duration::from_secs(5)).await;
    assert!(end.cancelled, "the handler was not cancelled when its stream ended: {end:?}");
}

/// A caller revoked mid-stream, after some rotations, gets no new token and is
/// cut off within one token lifetime.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_revoked_caller_cut_off() {
    let f = serve(false).await;
    let (c, src) = f.client(f.addr).await;
    let revoked_at = Arc::new(Mutex::new(None::<Instant>));
    let revoker = {
        let (src, revoked_at) = (src.clone(), revoked_at.clone());
        tokio::spawn(async move {
            while src.mints() < 3 {
                tokio::time::sleep(Duration::from_millis(1)).await;
            }
            src.revoked.store(true, Ordering::SeqCst);
            *revoked_at.lock().unwrap() = Some(Instant::now());
        })
    };
    let (st, ended) = ends_at(&c, TOKEN_TTL * 6).await.expect("a revoked caller's stream stayed open");
    revoker.abort();
    let revoked_at = revoked_at.lock().unwrap().unwrap_or_else(|| panic!("the stream ended ({st:?}) before the revocation"));
    assert_eq!(st.code(), tonic::Code::Unauthenticated, "stream ended with {st:?}");
    let after = ended - revoked_at;
    assert!(after <= TOKEN_TTL + Duration::from_secs(1), "revoked caller cut off {after:?} after revocation");
    f.tracker.handler_end(Duration::from_secs(5)).await;
}

/// A rotation presenting another subject's token (one the method's policy
/// admits) is refused, and the caller's stream ends PERMISSION_DENIED, not
/// continued under the other name.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_impersonation_ends_the_stream() {
    let f = serve(false).await;
    let (c, src) = f.client(f.addr).await;
    let switcher = {
        let src = src.clone();
        tokio::spawn(async move {
            while src.mints() < 1 {
                tokio::time::sleep(Duration::from_millis(1)).await;
            }
            *src.impersonate.lock().unwrap() = Some("other-svc".to_string());
        })
    };
    let (st, _) = ends_at(&c, TOKEN_TTL * 4).await.expect("an impersonated rotation kept the stream open");
    switcher.abort();
    assert_eq!(st.code(), tonic::Code::PermissionDenied, "stream ended with {st:?}");
    let end = f.tracker.handler_end(Duration::from_secs(5)).await;
    assert!(end.cancelled, "the handler was not cancelled: {end:?}");
}

// ---------------------------------------------------------------------------
// The wire
// ---------------------------------------------------------------------------

const ASSIST_ROTATING: &str = "/rotationfixture.TickerServiceRotating/AssistTicks";

struct Leg {
    tx: tokio::sync::mpsc::Sender<rotation::Envelope>,
    rx: tonic::Streaming<rotation::Envelope>,
    first: rotation::Envelope,
}

/// Opens a leg of AssistTicks' sibling with `subject`'s token, presenting
/// `joining` (empty: a new session), and returns it with the server's first
/// envelope, or the status that refused it.
async fn open_leg(f: &Fixture, channel: &tonic::transport::Channel, subject: &str, joining: &str) -> Result<Leg, tonic::Status> {
    let (tx, rx) = tokio::sync::mpsc::channel(64);
    tx.send(rotation::Envelope { session_id: joining.to_string(), ..Default::default() }).await.unwrap();
    let mut request = tonic::Request::new(tokio_stream::wrappers::ReceiverStream::new(rx));
    request
        .metadata_mut()
        .insert("authorization", format!("Bearer {}", mint(&f.key, subject)).parse().unwrap());
    let mut grpc = tonic::client::Grpc::new(channel.clone());
    grpc.ready().await.expect("ready");
    let path = tonic::codegen::http::uri::PathAndQuery::from_static(ASSIST_ROTATING);
    let mut rx = grpc.streaming(request, path, rotation::EnvelopeCodec).await?.into_inner();
    let first = rx.message().await?.ok_or_else(|| tonic::Status::unknown("the leg ended before its first envelope"))?;
    Ok(Leg { tx, rx, first })
}

fn refused_without_reason(what: &str, st: &tonic::Status, want: tonic::Code) {
    assert_eq!(st.code(), want, "{what}: got {st:?}");
    assert_eq!(st.message(), "", "{what}: the refusal carries a description; it must carry none");
}

/// A rotation presenting a valid session id with another subject's token (one
/// the policy admits) is refused PERMISSION_DENIED with no description, as is
/// an unknown id; the same subject's new token is attached.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_another_subject_refused() {
    let f = serve(false).await;
    let ch = connect(f.addr).await;
    let opened = open_leg(&f, &ch, "caller-svc", "").await.expect("open");
    let id = opened.first.session_id.clone();
    assert!(
        !id.is_empty() && opened.first.payload.is_empty() && !opened.first.last && !opened.first.fin,
        "the server's first envelope is {:?}, want a session id and nothing else",
        opened.first
    );
    let st = open_leg(&f, &ch, "other-svc", &id).await.err().expect("another subject's rotation was attached");
    refused_without_reason("another subject's rotation", &st, tonic::Code::PermissionDenied);
    let st = open_leg(&f, &ch, "caller-svc", "no-such-session").await.err().expect("an unknown session was joined");
    refused_without_reason("an unknown session's rotation", &st, tonic::Code::PermissionDenied);
    // A caller the method's policy does not admit is refused by the method's
    // own wrapper: the stream's status, with no description.
    let st = match open_leg(&f, &ch, "intruder-svc", "").await {
        Err(st) => st,
        Ok(mut leg) => match leg.rx.message().await {
            Err(st) => st,
            Ok(m) => panic!("a caller the policy does not admit got {m:?}"),
        },
    };
    refused_without_reason("a caller the policy does not admit", &st, tonic::Code::PermissionDenied);
    let rotated = open_leg(&f, &ch, "caller-svc", &id).await.expect("the same subject's rotation");
    assert_eq!(rotated.first.session_id, id, "the rotation's echo names another session");
    drop(opened);
}

/// A cutover driven by hand: messages before and after a last both ways, with
/// the server's sends still in flight on the old leg; the handler sees every
/// message once, in order, on one call.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_cutover_loses_nothing() {
    let f = serve(false).await;
    let ch = connect(f.addr).await;
    let Leg { tx: old_tx, rx: mut old_rx, first } = open_leg(&f, &ch, "caller-svc", "").await.expect("open");
    let envelope = |seq: i64| rotation::Envelope {
        payload: prost::Message::encode_to_vec(&Tick { seq, ..Default::default() }),
        ..Default::default()
    };
    for i in 1..=50 {
        old_tx.send(envelope(i)).await.expect("send");
    }
    let Leg { tx: next_tx, rx: mut next_rx, first: echoed } =
        open_leg(&f, &ch, "caller-svc", &first.session_id).await.expect("rotation");
    assert_eq!(echoed.session_id, first.session_id);
    old_tx.send(rotation::Envelope { last: true, ..Default::default() }).await.expect("last");
    drop(old_tx);
    for i in 51..=100 {
        next_tx.send(envelope(i)).await.expect("send");
    }
    next_tx.send(rotation::Envelope { fin: true, ..Default::default() }).await.expect("fin");
    let mut got = Vec::new();
    loop {
        let e = old_rx
            .message()
            .await
            .unwrap_or_else(|st| panic!("the old leg ended ({st:?}) before the server's last"))
            .expect("the old leg ended before the server's last");
        if !e.payload.is_empty() {
            got.push(<Tick as prost::Message>::decode(e.payload.as_slice()).expect("tick").seq);
        }
        if e.last {
            break;
        }
    }
    match old_rx.message().await {
        Ok(None) => {}
        other => panic!("the old leg ended with {other:?} after its last, want OK"),
    }
    loop {
        match next_rx.message().await {
            Ok(Some(e)) => {
                if !e.payload.is_empty() {
                    got.push(<Tick as prost::Message>::decode(e.payload.as_slice()).expect("tick").seq);
                }
            }
            Ok(None) => break,
            Err(st) => panic!("the new leg ended with {st:?}, want OK"),
        }
    }
    drop(next_tx);
    check_sequence("echoed across the cutover", &got, 100);
    let end = f.tracker.handler_end(Duration::from_secs(5)).await;
    assert_eq!(end.method, "AssistTicks");
    assert!(end.err.is_none() && !end.cancelled, "the handler ended with {end:?}, want a clean end");
    check_sequence("received across the cutover", &end.received, 100);
    assert_eq!(f.tracker.calls.load(Ordering::SeqCst), 1, "one handler call");
}

/// A TCP proxy that can cut every connection it carries.
struct Proxy {
    addr: std::net::SocketAddr,
    conns: Arc<Mutex<Vec<tokio::task::JoinHandle<()>>>>,
}

impl Proxy {
    async fn start(target: std::net::SocketAddr) -> Self {
        let lis = tokio::net::TcpListener::bind("127.0.0.1:0").await.expect("bind proxy");
        let addr = lis.local_addr().expect("proxy addr");
        let conns = Arc::new(Mutex::new(Vec::new()));
        let held = conns.clone();
        tokio::spawn(async move {
            while let Ok((mut inbound, _)) = lis.accept().await {
                let Ok(mut outbound) = tokio::net::TcpStream::connect(target).await else { continue };
                let conn = tokio::spawn(async move {
                    let _ = tokio::io::copy_bidirectional(&mut inbound, &mut outbound).await;
                });
                held.lock().unwrap().push(conn);
            }
        });
        Self { addr, conns }
    }

    fn cut(&self) {
        for conn in self.conns.lock().unwrap().drain(..) {
            conn.abort();
        }
    }
}

/// A leg that ends without its last (here the caller's connection is cut)
/// ends the logical stream: the caller gets a status, the handler is
/// cancelled, and nothing is spliced onto another leg.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_leg_cut_without_last_ends_the_stream() {
    let f = serve(false).await;
    let proxy = Proxy::start(f.addr).await;
    let (c, _src) = f.client(proxy.addr).await;
    let mut s = c.generate_ticks(Tick::default()).await.expect("GenerateTicks").into_inner();
    for _ in 0..5 {
        s.message().await.expect("Recv").expect("a tick");
    }
    proxy.cut();
    let ended = tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            match s.message().await {
                Ok(Some(_)) => {}
                Ok(None) => return None,
                Err(st) => return Some(st),
            }
        }
    })
    .await
    .expect("the cut stream did not end");
    match ended {
        Some(st) => assert_ne!(st.code(), tonic::Code::Ok, "the cut stream ended OK"),
        None => panic!("the cut stream ended cleanly, want a failure status"),
    }
    let end = f.tracker.handler_end(Duration::from_secs(5)).await;
    assert!(end.cancelled, "the handler was not cancelled by the cut: {end:?}");
}

/// Against a server that serves only the plain service (no sibling), the
/// generated client's stream goes to the plain method, which works, and ends
/// at its token's exp with UNAUTHENTICATED (#260).
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_rotation_falls_back_to_plain_method() {
    let f = serve(true).await;
    let (c, _src) = f.client(f.addr).await;
    let opened = Instant::now();
    let mut s = c.generate_ticks(Tick::default()).await.expect("GenerateTicks").into_inner();
    let mut ticks = 0;
    let st = loop {
        match tokio::time::timeout(TOKEN_TTL * 3, s.message()).await.expect("the plain stream outlived its token") {
            Ok(Some(_)) => ticks += 1,
            Ok(None) => panic!("the plain stream ended cleanly, want UNAUTHENTICATED at its token's exp"),
            Err(st) => break st,
        }
    };
    assert!(ticks > 0, "no tick arrived on the plain method: {st:?}");
    assert_eq!(st.code(), tonic::Code::Unauthenticated, "the plain stream ended with {st:?}");
    assert!(opened.elapsed() <= TOKEN_TTL + Duration::from_secs(3), "the plain stream lasted {:?}", opened.elapsed());
}
