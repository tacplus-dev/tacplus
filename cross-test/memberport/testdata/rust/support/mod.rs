// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! The fake fleet of the Rust member port tests (tacplus#322), the twin of the
//! fixture plumbing in MemberPortBehaviorTest.java: a kernel signing key and
//! its JWK, a CA (the fleet trust bundle) and serving certificates with SPIFFE
//! SANs; member tokens from a cached, renewing minter; hand-driven tunnel
//! streams (Raw) and a hand-built receiver for what a generated member port
//! never sends.

#![allow(dead_code)]

pub mod peer;

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, RwLock};
use std::task::{Context, Poll};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use base64::Engine as _;
use key::keys;
use memberport::{ChunkCodec, JwkLookup, MemberChunk, MemberPort, ServingCert, TokenSource};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::sign::CertifiedKey;
use tokio::sync::mpsc;
use tonic::codegen::{http, Body, BoxFuture, StdError};
use tonic::{Code, Status};

pub const TD: &str = "example.org";
pub const SVC: &str = "grid";
pub const X: &str = "/tenants/aaaaaaaa";
pub const Y: &str = "/tenants/bbbbbbbb";
pub const MB: u64 = 1024 * 1024;

pub fn aud() -> String {
    memberport::audience_for(SVC)
}

pub fn now_secs() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).expect("clock").as_secs() as i64
}

pub fn now_millis() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).expect("clock").as_millis() as i64
}

pub fn loopback0() -> SocketAddr {
    "127.0.0.1:0".parse().unwrap()
}

fn provider() -> Arc<rustls::crypto::CryptoProvider> {
    Arc::new(rustls::crypto::ring::default_provider())
}

// ---- The fake fleet ----

pub struct Fleet {
    pub kernel: keys::ImmutableKey,
    pub jwks: Arc<RwLock<HashMap<String, Arc<jwk::ImmutableJwk>>>>,
    ca: x509::ImmutableCertChain,
    pub ca_der: CertificateDer<'static>,
}

impl Fleet {
    pub fn new() -> Arc<Self> {
        let _ = rustls::crypto::ring::default_provider().install_default();
        let kernel = keys::generate_ed25519_key().expect("kernel key");
        let mut jwks = HashMap::new();
        jwks.insert(kernel.id().to_string(), Arc::new(jwk_for(&kernel)));
        let ca_key = keys::generate_ec_p256_key().expect("CA key");
        // self_sign makes no CA certificate (no basicConstraints), which Java's
        // PKIX refuses as a trust anchor: the bootstrap certificate only names
        // the issuer, and the CA certificate is the same key signing itself
        // with is_ca set.
        let bootstrap = x509::ImmutableCertChain::for_self_signing(ca_key.clone())
            .self_sign("fleet-ca", now_secs() + 86_400, x509::x509::X509KeyUsage::CertSign, &[])
            .expect("self-sign the fleet CA");
        let ca_public = match ca_key.public_key() {
            keys::PublicKey::EcP256(k) => k.to_spki_der().expect("CA SPKI"),
            _ => unreachable!("the CA key is P-256"),
        };
        let ca_public = keys::ImmutablePublicKey::from_pem(
            ca_key.id().to_string(),
            &base64::engine::general_purpose::STANDARD.encode(pem_text("PUBLIC KEY", &ca_public)),
        )
        .expect("CA public key");
        let ca_cert = x509::ImmutableCertChain::new(vec![bootstrap], Some(ca_key.clone()))
            .expect("bootstrap chain")
            .sign(&x509::x509::X509CertRequest {
                public_key: Some(ca_public),
                exp: now_secs() + 86_400,
                key_usage: x509::x509::X509KeyUsage::CertSign,
                issuer: "fleet-ca".to_string(),
                is_ca: true,
                path_len_constraint: 0,
                ..Default::default()
            })
            .expect("sign the fleet CA");
        let ca_der = der_of(&ca_cert);
        let ca = x509::ImmutableCertChain::new(vec![ca_cert], Some(ca_key)).expect("CA chain");
        Arc::new(Self { kernel, jwks: Arc::new(RwLock::new(jwks)), ca, ca_der })
    }

    pub fn mint(&self, service: &str, namespace: &str, audience: &str, ttl_secs: i64) -> String {
        mint_with(&self.kernel, service, namespace, audience, ttl_secs)
    }

    /// The fleet's verifying keys, as the member port looks them up.
    pub fn jwk_lookup(&self) -> JwkLookup {
        let jwks = self.jwks.clone();
        Arc::new(move |kid: &str| jwks.read().unwrap().get(kid).cloned())
    }

    /// A serving certificate naming `spiffe://example.org<namespace>/services/<service>`.
    pub fn leaf(&self, namespace: &str, service: &str, ed25519: bool) -> Arc<CertifiedKey> {
        let (chain, key_pem) = self.leaf_parts(namespace, service, ed25519);
        let private = rustls_pemfile::private_key(&mut key_pem.as_bytes())
            .expect("leaf key PEM")
            .expect("a leaf private key");
        Arc::new(CertifiedKey::from_der(chain, private, &provider()).expect("certified leaf"))
    }

    /// A serving certificate's chain (leaf, then the CA) and its PKCS#8 key PEM.
    pub fn leaf_parts(&self, namespace: &str, service: &str, ed25519: bool) -> (Vec<CertificateDer<'static>>, String) {
        let key = if ed25519 {
            keys::generate_ed25519_key().expect("leaf key")
        } else {
            keys::generate_ec_p256_key().expect("leaf key")
        };
        let spki = match key.public_key() {
            keys::PublicKey::Ed25519(k) => {
                let mut der = vec![0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00];
                der.extend_from_slice(k.as_bytes());
                der
            }
            keys::PublicKey::EcP256(k) => k.to_spki_der().expect("leaf SPKI"),
            _ => unreachable!("leaf keys are Ed25519 or P-256"),
        };
        let pem = pem_text("PUBLIC KEY", &spki);
        let public = keys::ImmutablePublicKey::from_pem(
            key.id().to_string(),
            &base64::engine::general_purpose::STANDARD.encode(pem),
        )
        .expect("leaf public key");
        let cert = self
            .ca
            .sign(&x509::x509::X509CertRequest {
                public_key: Some(public),
                exp: now_secs() + 86_400,
                key_usage: x509::x509::X509KeyUsage::DigitalSignature,
                ext_key_usage: vec![x509::x509::X509ExtKeyUsage::ServerAuth],
                issuer: service.to_string(),
                uri_sans: vec![format!("spiffe://{}{}/services/{}", TD, namespace, service)],
                ..Default::default()
            })
            .expect("sign the leaf");
        let key_pem = String::from_utf8(key.pem_bytes().to_vec()).expect("key PEM text");
        (vec![der_of(&cert), self.ca_der.clone()], key_pem)
    }
}

/// An Ed25519 key's PKCS#8 v1 PEM (the seed only), the form Java and Go read.
pub fn ed25519_pkcs8_v1_pem(key: &keys::ImmutableKey) -> String {
    let seed = match key.private_key() {
        keys::PrivateKey::Ed25519(k) => k.to_bytes(),
        _ => panic!("expected an Ed25519 key"),
    };
    let mut der = vec![
        0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20,
    ];
    der.extend_from_slice(&seed);
    pem_text("PRIVATE KEY", &der)
}

/// A DER certificate as PEM text.
pub fn cert_pem(der: &CertificateDer<'_>) -> String {
    pem_text("CERTIFICATE", der.as_ref())
}

fn pem_text(label: &str, der: &[u8]) -> String {
    let b64 = base64::engine::general_purpose::STANDARD.encode(der);
    let mut out = format!("-----BEGIN {}-----\n", label);
    for line in b64.as_bytes().chunks(64) {
        out.push_str(std::str::from_utf8(line).unwrap());
        out.push('\n');
    }
    out.push_str(&format!("-----END {}-----\n", label));
    out
}

fn der_of(cert: &x509::ImmutableCert) -> CertificateDer<'static> {
    let pem = cert.to_pem().expect("cert PEM");
    let der = rustls_pemfile::certs(&mut pem.as_bytes())
        .next()
        .expect("one certificate")
        .expect("certificate DER");
    der
}

pub fn jwk_for(key: &keys::ImmutableKey) -> jwk::ImmutableJwk {
    let x = match key.public_key() {
        keys::PublicKey::Ed25519(ed) => base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(ed.as_bytes()),
        _ => panic!("expected an Ed25519 kernel key"),
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

pub fn mint_with(key: &keys::ImmutableKey, service: &str, namespace: &str, audience: &str, ttl_secs: i64) -> String {
    let mut tok = token::Token::new();
    tok.set_issuer("kernel").unwrap();
    tok.set_audience(audience).unwrap();
    tok.set_sub_type("service").unwrap();
    tok.set_subject(service).unwrap();
    tok.set_namespace(namespace).unwrap();
    tok.set_expiration(now_secs() + ttl_secs).unwrap();
    tok.sign(key).expect("sign");
    tok.base64().expect("signed token").to_string()
}

/// A member's token source, like the ServiceManager's: a cached token,
/// renewed once fewer than `renew_before` seconds remain. It can be made to
/// stall (the mint hangs) or to never renew.
pub struct Minter {
    fleet: Arc<Fleet>,
    namespace: String,
    ttl: i64,
    renew_before: i64,
    pub mints: AtomicUsize,
    pub stall: AtomicBool,
    pub never_renew: AtomicBool,
    cached: tokio::sync::Mutex<Option<String>>,
}

impl Minter {
    pub fn new(fleet: &Arc<Fleet>, namespace: &str, ttl: i64, renew_before: i64) -> Arc<Self> {
        Arc::new(Self {
            fleet: fleet.clone(),
            namespace: namespace.to_string(),
            ttl,
            renew_before,
            mints: AtomicUsize::new(0),
            stall: AtomicBool::new(false),
            never_renew: AtomicBool::new(false),
            cached: tokio::sync::Mutex::new(None),
        })
    }

    pub async fn token(&self) -> String {
        let mut cached = self.cached.lock().await;
        if let Some(t) = cached.as_ref() {
            let exp = token::Token::parse(t).unwrap().expiration().unwrap();
            if self.never_renew.load(Ordering::SeqCst) || exp - now_secs() > self.renew_before {
                return t.clone();
            }
        }
        while self.stall.load(Ordering::SeqCst) {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        self.mints.fetch_add(1, Ordering::SeqCst);
        let t = self.fleet.mint(SVC, &self.namespace, &aud(), self.ttl);
        *cached = Some(t.clone());
        t
    }

    pub fn source(self: &Arc<Self>) -> TokenSource {
        let me = self.clone();
        Arc::new(move || {
            let me = me.clone();
            Box::pin(async move { Ok(me.token().await) })
        })
    }
}

/// A serving certificate the test can swap: a renewal.
pub fn serving(cert: &Arc<RwLock<Arc<CertifiedKey>>>) -> ServingCert {
    let cert = cert.clone();
    Arc::new(move || Some(cert.read().unwrap().clone()))
}

pub fn member_with(fleet: &Arc<Fleet>, tokens: TokenSource, cert: &Arc<RwLock<Arc<CertifiedKey>>>, jwks: JwkLookup) -> MemberPort {
    MemberPort::new(SVC, TD, vec![fleet.ca_der.clone()], tokens, jwks, serving(cert)).expect("member port")
}

/// A member of `namespace` with long-lived tokens and an Ed25519 certificate.
pub fn member(fleet: &Arc<Fleet>, namespace: &str) -> MemberPort {
    let cert = Arc::new(RwLock::new(fleet.leaf(namespace, SVC, true)));
    member_with(fleet, Minter::new(fleet, namespace, 300, 60).source(), &cert, fleet.jwk_lookup())
}

/// peer client → a.forward → b.listen → peer server; the address the client dials.
pub async fn tunnel(a: &MemberPort, b: &MemberPort, server: &peer::Server) -> SocketAddr {
    let b_addr = b.listen(loopback0(), server.addr).await.expect("listen");
    a.forward(b_addr).await.expect("forward")
}

/// Runs blocking peer code off the runtime's workers.
pub async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> T {
    tokio::task::spawn_blocking(f).await.expect("blocking task")
}

pub async fn wait_for(what: &str, timeout: Duration, cond: impl Fn() -> bool) {
    let deadline = Instant::now() + timeout;
    while !cond() {
        assert!(Instant::now() < deadline, "timed out waiting for {}", what);
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

/// Publishes a file to the Java side of a two-process leg (written, then renamed).
pub fn write_interop(dir: &std::path::Path, name: &str, data: &str) {
    let tmp = dir.join(format!("{}.rust.tmp", name));
    std::fs::write(&tmp, data).expect("write interop file");
    std::fs::rename(&tmp, dir.join(name)).expect("publish interop file");
}

/// Waits for the Java side of a two-process leg to publish `name`; its trimmed content.
pub async fn wait_interop(dir: &std::path::Path, name: &str, timeout: Duration) -> String {
    let path = dir.join(name);
    let deadline = Instant::now() + timeout;
    loop {
        if let Ok(s) = std::fs::read_to_string(&path) {
            return s.trim().to_string();
        }
        assert!(Instant::now() < deadline, "timed out waiting for {} from the Java side", name);
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
}

/// The status of a refused stream: the code, and no reason text.
pub fn assert_refused(code: Code, status: &Status, what: &str) {
    assert_eq!(status.code(), code, "{}: {:?}", what, status);
    assert!(status.message().is_empty(), "{}: a refusal carries no reason text: {:?}", what, status);
}

// ---- Raw member tunnel clients (for faults) ----

/// Accepts any server certificate: what a peer that is not a generated
/// member port does, skipping the member port's own checks of the receiver.
#[derive(Debug)]
struct AcceptAny(Arc<rustls::crypto::CryptoProvider>);

impl rustls::client::danger::ServerCertVerifier for AcceptAny {
    fn verify_server_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp: &[u8],
        _now: UnixTime,
    ) -> Result<rustls::client::danger::ServerCertVerified, rustls::Error> {
        Ok(rustls::client::danger::ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(message, cert, dss, &self.0.signature_verification_algorithms)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(message, cert, dss, &self.0.signature_verification_algorithms)
    }

    fn supported_verify_schemes(&self) -> Vec<rustls::SignatureScheme> {
        self.0.signature_verification_algorithms.supported_schemes()
    }
}

/// A TLS client config that trusts anything, offering `alpn`.
pub fn insecure_tls(alpn: &[u8]) -> Arc<rustls::ClientConfig> {
    let mut config = rustls::ClientConfig::builder_with_provider(provider())
        .with_protocol_versions(&[&rustls::version::TLS13])
        .unwrap()
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(AcceptAny(provider())))
        .with_no_client_auth();
    config.alpn_protocols = vec![alpn.to_vec()];
    Arc::new(config)
}

/// A TLS connection to a member port; Err when the handshake fails.
pub async fn raw_tls(
    addr: SocketAddr,
    alpn: &[u8],
) -> std::io::Result<tokio_rustls::client::TlsStream<tokio::net::TcpStream>> {
    let tcp = tokio::net::TcpStream::connect(addr).await?;
    tokio_rustls::TlsConnector::from(insecure_tls(alpn))
        .connect(ServerName::IpAddress(addr.ip().into()), tcp)
        .await
}

/// The leaf certificate a member port presents on a new handshake.
pub async fn presented_leaf(addr: SocketAddr) -> Vec<u8> {
    let tls = raw_tls(addr, b"h2").await.expect("handshake");
    tls.get_ref().1.peer_certificates().expect("peer certificates")[0].as_ref().to_vec()
}

/// A gRPC channel to a member port that skips the member port's own checks of the receiver.
pub fn raw_channel(addr: SocketAddr) -> tonic::transport::Channel {
    let tls = insecure_tls(b"h2");
    let connector = tower::service_fn(move |_: tonic::transport::Uri| {
        let tls = tls.clone();
        async move {
            let tcp = tokio::net::TcpStream::connect(addr).await?;
            let conn = tokio_rustls::TlsConnector::from(tls)
                .connect(ServerName::IpAddress(addr.ip().into()), tcp)
                .await?;
            Ok::<_, std::io::Error>(hyper_util::rt::TokioIo::new(conn))
        }
    });
    tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .unwrap()
        .connect_with_connector_lazy(connector)
}

/// One member tunnel stream, driven by hand: what a peer that is not a
/// generated member port can send.
pub struct Raw {
    tx: Option<mpsc::Sender<MemberChunk>>,
    received: mpsc::UnboundedReceiver<MemberChunk>,
    status: Arc<Mutex<Option<Status>>>,
    task: tokio::task::JoinHandle<()>,
}

impl Raw {
    pub fn open(channel: &tonic::transport::Channel, token: Option<&str>) -> Self {
        let (tx, rx) = mpsc::channel::<MemberChunk>(1024);
        let (got_tx, received) = mpsc::unbounded_channel();
        let status = Arc::new(Mutex::new(None));
        let mut request = tonic::Request::new(tokio_stream::wrappers::ReceiverStream::new(rx));
        if let Some(token) = token {
            request.metadata_mut().insert("authorization", format!("Bearer {}", token).parse().unwrap());
        }
        let channel = channel.clone();
        let st = status.clone();
        let task = tokio::spawn(async move {
            let mut grpc = tonic::client::Grpc::new(channel);
            if let Err(e) = grpc.ready().await {
                *st.lock().unwrap() = Some(Status::unavailable(e.to_string()));
                return;
            }
            let path = http::uri::PathAndQuery::from_static(memberport::OPEN_PATH);
            let mut stream = match grpc.streaming(request, path, ChunkCodec).await {
                Ok(response) => response.into_inner(),
                Err(s) => {
                    *st.lock().unwrap() = Some(s);
                    return;
                }
            };
            loop {
                match stream.message().await {
                    Ok(Some(c)) => {
                        let _ = got_tx.send(c);
                    }
                    Ok(None) => {
                        *st.lock().unwrap() = Some(Status::new(Code::Ok, ""));
                        return;
                    }
                    Err(s) => {
                        *st.lock().unwrap() = Some(s);
                        return;
                    }
                }
            }
        });
        Self { tx: Some(tx), received, status, task }
    }

    pub async fn send(&self, c: MemberChunk) -> &Self {
        if let Some(tx) = &self.tx {
            let _ = tx.send(c).await;
        }
        self
    }

    /// Cuts the stream off without last: its request ends and its response is dropped.
    pub fn cancel(&mut self) {
        self.tx = None;
        self.task.abort();
    }

    pub async fn next(&mut self) -> MemberChunk {
        match tokio::time::timeout(Duration::from_secs(10), self.received.recv()).await {
            Ok(Some(c)) => c,
            _ => panic!("no chunk within 10s; stream status {:?}", self.status.lock().unwrap()),
        }
    }

    pub async fn status(&self, timeout: Duration) -> Status {
        let deadline = Instant::now() + timeout;
        loop {
            if let Some(s) = self.status.lock().unwrap().clone() {
                return s;
            }
            assert!(Instant::now() < deadline, "the stream did not end within {:?}", timeout);
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    }
}

pub fn open_chunk() -> MemberChunk {
    MemberChunk::default()
}

pub fn join(session_id: &str) -> MemberChunk {
    MemberChunk { session_id: session_id.to_string(), ..Default::default() }
}

pub fn data(b: &[u8]) -> MemberChunk {
    MemberChunk { payload: b.to_vec(), ..Default::default() }
}

pub fn last() -> MemberChunk {
    MemberChunk { last: true, ..Default::default() }
}

/// Opens a new session and returns its id.
pub async fn open_session(r: &mut Raw) -> String {
    r.send(open_chunk()).await;
    let first = r.next().await;
    assert!(!first.session_id.is_empty(), "the server's first chunk names the session: {:?}", first);
    first.session_id
}

// ---- A hand-built receiver ----

/// What a fake receiver does with each stream after it attached it.
#[derive(Clone, Copy)]
pub enum FakeMode {
    /// Counts the data chunks.
    Count,
    /// Echoes the first data chunk, then ends the stream OK with no last.
    EchoThenEnd,
}

/// A fake receiver with a valid tenant-X certificate: it answers each stream
/// with `token` in its headers and attaches it, then behaves as `mode`.
pub async fn fake_receiver(fleet: &Arc<Fleet>, token: String, mode: FakeMode, data_chunks: Arc<AtomicUsize>) -> SocketAddr {
    let cert = fleet.leaf(X, SVC, false);
    let mut config = rustls::ServerConfig::builder_with_provider(provider())
        .with_protocol_versions(&[&rustls::version::TLS13])
        .unwrap()
        .with_no_client_auth()
        .with_cert_resolver(Arc::new(Fixed(cert)));
    config.alpn_protocols = vec![b"h2".to_vec()];
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(config));
    let listener = tokio::net::TcpListener::bind(loopback0()).await.unwrap();
    let addr = listener.local_addr().unwrap();
    let (conns_tx, conns_rx) = mpsc::channel::<Result<FakeTls, std::io::Error>>(16);
    tokio::spawn(async move {
        loop {
            let Ok((tcp, _)) = listener.accept().await else { return };
            if let Ok(tls) = acceptor.accept(tcp).await {
                let _ = conns_tx.send(Ok(FakeTls(tls))).await;
            }
        }
    });
    let svc = FakeTunnel { token, mode, data_chunks };
    tokio::spawn(
        tonic::transport::Server::builder()
            .add_service(svc)
            .serve_with_incoming(tokio_stream::wrappers::ReceiverStream::new(conns_rx)),
    );
    addr
}

#[derive(Debug)]
struct Fixed(Arc<CertifiedKey>);

impl rustls::server::ResolvesServerCert for Fixed {
    fn resolve(&self, _hello: rustls::server::ClientHello<'_>) -> Option<Arc<CertifiedKey>> {
        Some(self.0.clone())
    }
}

struct FakeTls(tokio_rustls::server::TlsStream<tokio::net::TcpStream>);

impl tonic::transport::server::Connected for FakeTls {
    type ConnectInfo = ();
    fn connect_info(&self) -> Self::ConnectInfo {}
}

impl tokio::io::AsyncRead for FakeTls {
    fn poll_read(mut self: std::pin::Pin<&mut Self>, cx: &mut Context<'_>, buf: &mut tokio::io::ReadBuf<'_>) -> Poll<std::io::Result<()>> {
        std::pin::Pin::new(&mut self.0).poll_read(cx, buf)
    }
}

impl tokio::io::AsyncWrite for FakeTls {
    fn poll_write(mut self: std::pin::Pin<&mut Self>, cx: &mut Context<'_>, buf: &[u8]) -> Poll<std::io::Result<usize>> {
        std::pin::Pin::new(&mut self.0).poll_write(cx, buf)
    }
    fn poll_flush(mut self: std::pin::Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
        std::pin::Pin::new(&mut self.0).poll_flush(cx)
    }
    fn poll_shutdown(mut self: std::pin::Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
        std::pin::Pin::new(&mut self.0).poll_shutdown(cx)
    }
}

#[derive(Clone)]
struct FakeTunnel {
    token: String,
    mode: FakeMode,
    data_chunks: Arc<AtomicUsize>,
}

impl tonic::server::NamedService for FakeTunnel {
    const NAME: &'static str = memberport::SERVICE_NAME;
}

impl<B> tonic::codegen::Service<http::Request<B>> for FakeTunnel
where
    B: Body + Send + 'static,
    B::Error: Into<StdError> + Send + 'static,
{
    type Response = http::Response<tonic::body::Body>;
    type Error = std::convert::Infallible;
    type Future = BoxFuture<Self::Response, Self::Error>;

    fn poll_ready(&mut self, _cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        Poll::Ready(Ok(()))
    }

    fn call(&mut self, req: http::Request<B>) -> Self::Future {
        let me = self.clone();
        Box::pin(async move { Ok(tonic::server::Grpc::new(ChunkCodec).streaming(me, req).await) })
    }
}

type ChunkStream = std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<MemberChunk, Status>> + Send>>;

impl tonic::server::StreamingService<MemberChunk> for FakeTunnel {
    type Response = MemberChunk;
    type ResponseStream = ChunkStream;
    type Future = BoxFuture<tonic::Response<ChunkStream>, Status>;

    fn call(&mut self, request: tonic::Request<tonic::Streaming<MemberChunk>>) -> Self::Future {
        let me = self.clone();
        Box::pin(async move {
            let token = me.token.clone();
            let mut inbound = request.into_inner();
            let (tx, rx) = mpsc::channel::<Result<MemberChunk, Status>>(16);
            tokio::spawn(async move {
                let mut first = true;
                while let Ok(Some(m)) = inbound.message().await {
                    if first {
                        first = false;
                        let _ = tx.send(Ok(join("fake-session"))).await;
                    } else if !m.payload.is_empty() {
                        me.data_chunks.fetch_add(1, Ordering::SeqCst);
                        if let FakeMode::EchoThenEnd = me.mode {
                            let _ = tx.send(Ok(data(&m.payload))).await;
                            // Ends OK, with no last.
                            return;
                        }
                    }
                }
            });
            let mut response = tonic::Response::new(Box::pin(tokio_stream::wrappers::ReceiverStream::new(rx)) as ChunkStream);
            response.metadata_mut().insert(memberport::TOKEN_HEADER, token.parse().unwrap());
            Ok(response)
        })
    }
}
