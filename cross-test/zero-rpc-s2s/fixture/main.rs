// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Zero-rpc (client-only) Rust host, run against a fake kernel (#94).
//
// The REAL generated ServiceManager of cross-test/proto-zerorpc runs between two
// fakes over TLS with SPIFFE certificates:
//   - a kernel: the token minter, plus the ServiceHealthState that lists the
//     llmproxy instance. It records the path of every request it receives.
//   - an llmproxy peer (service id "upstreamllm", the package of its proto), found only through that ServiceHealthState.
//
// The host does `new()` + `run()` and then makes ONE llmproxy call through the
// generated s2s client. It asserts the call succeeds, and that the host did not
// behave as an instance of its parent service: it never published instance
// health, never asked for a serving certificate, and bound no listener.
use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use base64::Engine as _;
use rcgen::{BasicConstraints, CertificateParams, IsCa, KeyPair, SanType};
use tonic::transport::{Identity, Server, ServerTlsConfig};
use tonic::{Request, Response, Status};
use zerorpc_model::s2sm::handler::Handler;
use zerorpc_model::s2sm::ServiceManager;
use zerorpc_model::types::platform::zerorpchealth::*;
use zerorpc_model::types::platform::zerorpchealth::service_health_state_service_server::{
    ServiceHealthStateService, ServiceHealthStateServiceServer,
};
use zerorpc_model::types::services::mintsvc::mintsvc::mint_svc_server::{MintSvc, MintSvcServer};
use zerorpc_model::types::services::upstreamllm::upstreamllm::llm_proxy_server::{LlmProxy, LlmProxyServer};
use zerorpc_model::types::services::upstreamllm::upstreamllm::{CompleteRequest, CompleteResponse};
use zerorpc_model::types::sys::healthreport::healthreport::{InstanceStatus, ServiceInstanceHealth};
use zerorpc_model::types::sys::host::host::Host;
use zerorpc_model::types::sys::token::token::Token;
use zerorpc_model::types::sys::tokenrequest::tokenrequest::{
    MintServiceTokenRequest, TokenMintRequest, TokenMintResponse, X509MintRequest,
};
use zerorpc_model::types::sys::x509::x509::X509CertChain;

const TRUST_DOMAIN: &str = "test.local";
const REPLY: &str = "pong from llmproxy";

type Paths = Arc<Mutex<Vec<String>>>;

fn fail(msg: String) -> ! {
    println!("zero-rpc-s2s FAILED: {msg}");
    std::process::exit(1);
}

// ---- certificates -----------------------------------------------------------

struct Ca {
    params: CertificateParams,
    key: KeyPair,
    pem: String,
}

fn new_ca() -> Ca {
    let mut params = CertificateParams::new(Vec::<String>::new()).unwrap();
    params.is_ca = IsCa::Ca(BasicConstraints::Unconstrained);
    params.distinguished_name.push(rcgen::DnType::CommonName, "zero-rpc-s2s test CA");
    let key = KeyPair::generate().unwrap();
    let pem = params.self_signed(&key).unwrap().pem();
    Ca { params, key, pem }
}

/// A leaf whose only name is the canonical service SPIFFE ID of `service`.
fn service_identity(ca: &Ca, service: &str) -> Identity {
    let id = format!("spiffe://{TRUST_DOMAIN}/system/services/{service}");
    let mut params = CertificateParams::new(Vec::<String>::new()).unwrap();
    params.subject_alt_names = vec![SanType::URI(id.try_into().unwrap())];
    let key = KeyPair::generate().unwrap();
    let issuer = rcgen::Issuer::from_params(&ca.params, &ca.key);
    let cert = params.signed_by(&key, &issuer).unwrap();
    Identity::from_pem(cert.pem(), key.serialize_pem())
}

// ---- fake kernel ------------------------------------------------------------

struct Kernel {
    peer: SocketAddr,
}

fn token(exp_in: u64) -> Token {
    let exp = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs() + exp_in;
    let mut payload = HashMap::new();
    payload.insert("exp".to_string(), exp.to_string());
    payload.insert("instance_id".to_string(), "pigeon-instance".to_string());
    Token { payload, base64: "e30.e30.c2ln".to_string(), ..Default::default() }
}

#[tonic::async_trait]
impl MintSvc for Kernel {
    async fn token_mint(&self, _: Request<TokenMintRequest>) -> Result<Response<TokenMintResponse>, Status> {
        Err(Status::unimplemented("token_mint"))
    }
    async fn mint_service_token(
        &self,
        _: Request<MintServiceTokenRequest>,
    ) -> Result<Response<TokenMintResponse>, Status> {
        Ok(Response::new(TokenMintResponse {
            access_token: Some(token(3600)),
            refresh_token: Some(token(7200)),
            ..Default::default()
        }))
    }
    async fn x509_mint(&self, _: Request<X509MintRequest>) -> Result<Response<X509CertChain>, Status> {
        Err(Status::unimplemented("x509_mint"))
    }
}

#[tonic::async_trait]
impl ServiceHealthStateService for Kernel {
    type WatchInstancesStream = tokio_stream::wrappers::ReceiverStream<Result<InstancesEntriesResponse, Status>>;
    type WatchQueryInstancesStream = tokio_stream::wrappers::ReceiverStream<Result<InstancesQueryResponse, Status>>;

    async fn set_instances(&self, _: Request<ServiceInstanceHealth>) -> Result<Response<MapEmpty>, Status> {
        Err(Status::unimplemented("set_instances"))
    }
    async fn get_instances(&self, _: Request<InstancesGetRequest>) -> Result<Response<InstancesValueResponse>, Status> {
        Err(Status::unimplemented("get_instances"))
    }
    async fn get_all_instances(
        &self,
        _: Request<InstancesGetAllRequest>,
    ) -> Result<Response<InstancesEntriesResponse>, Status> {
        let mut entries = HashMap::new();
        entries.insert(
            "llmproxy-1".to_string(),
            ServiceInstanceHealth {
                service_id: "upstreamllm".to_string(),
                grpc_endpoint: Some(Host {
                    host: self.peer.ip().to_string(),
                    port: self.peer.port() as i32,
                    ..Default::default()
                }),
                status: InstanceStatus::Healthy as i32,
                ..Default::default()
            },
        );
        Ok(Response::new(InstancesEntriesResponse { entries, ..Default::default() }))
    }
    async fn contains_key_instances(
        &self,
        _: Request<InstancesGetRequest>,
    ) -> Result<Response<InstancesValueResponse>, Status> {
        Err(Status::unimplemented("contains_key_instances"))
    }
    async fn key_set_instances(
        &self,
        _: Request<InstancesKeysRequest>,
    ) -> Result<Response<InstancesKeysResponse>, Status> {
        Err(Status::unimplemented("key_set_instances"))
    }
    async fn size_instances(&self, _: Request<InstancesSizeRequest>) -> Result<Response<InstancesSizeResponse>, Status> {
        Err(Status::unimplemented("size_instances"))
    }
    async fn watch_instances(
        &self,
        _: Request<zerorpc_model::types::sys::trace::trace::TraceRequest>,
    ) -> Result<Response<Self::WatchInstancesStream>, Status> {
        Err(Status::unimplemented("watch_instances"))
    }
    async fn query_instances(
        &self,
        _: Request<InstancesQueryRequest>,
    ) -> Result<Response<InstancesQueryResponse>, Status> {
        Err(Status::unimplemented("query_instances"))
    }
    async fn watch_query_instances(
        &self,
        _: Request<InstancesQueryRequest>,
    ) -> Result<Response<Self::WatchQueryInstancesStream>, Status> {
        Err(Status::unimplemented("watch_query_instances"))
    }
}

// ---- fake llmproxy ----------------------------------------------------------

struct Llm;

#[tonic::async_trait]
impl LlmProxy for Llm {
    async fn generate(&self, req: Request<CompleteRequest>) -> Result<Response<CompleteResponse>, Status> {
        if req.metadata().get("authorization").is_none() {
            return Err(Status::unauthenticated("no bearer token on the s2s call"));
        }
        Ok(Response::new(CompleteResponse { text: format!("{REPLY}: {}", req.into_inner().prompt), ..Default::default() }))
    }
}

async fn listener() -> (tokio::net::TcpListener, SocketAddr) {
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    (l, addr)
}

// ---- the host ---------------------------------------------------------------

struct Zero;

#[tonic::async_trait]
impl Handler for Zero {
    async fn shutdown(&self) {}
}

#[tokio::main]
async fn main() {
    // run() holds a non-Send error across awaits, so it runs on a LocalSet.
    tokio::task::LocalSet::new().run_until(host()).await;
}

async fn host() {
    let ca = new_ca();
    let paths: Paths = Arc::new(Mutex::new(Vec::new()));

    let (llm_l, llm_addr) = listener().await;
    let llm_tls = ServerTlsConfig::new().identity(service_identity(&ca, "upstreamllm"));
    tokio::spawn(async move {
        Server::builder()
            .tls_config(llm_tls)
            .unwrap()
            .add_service(LlmProxyServer::new(Llm))
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(llm_l))
            .await
            .unwrap();
    });

    let (kernel_l, kernel_addr) = listener().await;
    let kernel_tls = ServerTlsConfig::new().identity(service_identity(&ca, "zerorpchealth"));
    let seen = paths.clone();
    let (health_reporter, health_svc) = tonic_health::server::health_reporter();
    health_reporter.set_service_status("", tonic_health::ServingStatus::Serving).await;
    tokio::spawn(async move {
        let _keep = health_reporter;
        let record = tower::ServiceBuilder::new().map_request(move |req: http::Request<tonic::body::Body>| {
            seen.lock().unwrap().push(req.uri().path().to_string());
            req
        });
        Server::builder()
            .tls_config(kernel_tls)
            .unwrap()
            .layer(record)
            .add_service(health_svc)
            .add_service(MintSvcServer::new(Kernel { peer: llm_addr }))
            .add_service(ServiceHealthStateServiceServer::new(Kernel { peer: llm_addr }))
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(kernel_l))
            .await
            .unwrap();
    });

    // The ports this host would bind as an instance: its TLS listener and the
    // plaintext probe. Reserved-then-released so the test can probe them closed.
    let (serve_l, serve_addr) = listener().await;
    drop(serve_l);

    let dir = std::env::temp_dir().join(format!("zero-rpc-s2s-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let k8s_token = dir.join("k8s-token");
    std::fs::write(&k8s_token, "projected-token").unwrap();
    let cfg_path = dir.join("config.json");
    let cfg = serde_json::json!({
        "endpoints": [{"host": kernel_addr.ip().to_string(), "port": kernel_addr.port()}],
        "listen_address": {"host": serve_addr.ip().to_string(), "port": serve_addr.port()},
        "spiffe_trust_domain": TRUST_DOMAIN,
        "trusted_cas": [{"base64": base64::engine::general_purpose::STANDARD.encode(&ca.pem)}],
        "health_probe_port": 0,
    });
    std::fs::write(&cfg_path, cfg.to_string()).unwrap();
    std::env::set_var("BOOTSTRAP_CONFIG", &cfg_path);
    std::env::set_var("KERNEL_K8S_TOKEN_FILE", &k8s_token);

    // new() must return without a serving certificate: the kernel serves no
    // TlsCertService, so waiting for one fails or hangs here.
    let sm = match tokio::time::timeout(Duration::from_secs(30), ServiceManager::new()).await {
        Ok(Ok(sm)) => Arc::new(sm),
        Ok(Err(e)) => fail(format!("ServiceManager::new(): {e}")),
        Err(_) => fail("ServiceManager::new() did not return in 30s (waiting for a serving cert?)".into()),
    };

    let run_sm = sm.clone();
    let run = tokio::task::spawn_local(async move { run_sm.run(Arc::new(Zero)).await.map_err(|e| e.to_string()) });

    // One llmproxy call through the generated s2s client. The peer is found
    // only by the health poller that run() starts; allow it a few polls.
    let deadline = tokio::time::Instant::now() + Duration::from_secs(40);
    let reply = loop {
        if run.is_finished() {
            fail(format!("run() returned early: {:?}", run.await));
        }
        let call = sm
            .s2s()
            .upstreamllm()
            .llm_proxy_client()
            .generate(CompleteRequest { prompt: "ping".to_string(), ..Default::default() })
            .await;
        match call {
            Ok(r) => break r,
            Err(e) if tokio::time::Instant::now() < deadline => {
                println!("llmproxy call not ready yet: {e}");
                tokio::time::sleep(Duration::from_millis(500)).await;
            }
            Err(e) => fail(format!("llmproxy call never succeeded: {e}")),
        }
    };
    println!("llmproxy replied: {reply:?}");
    if !format!("{reply:?}").contains(&format!("{REPLY}: ping")) {
        fail(format!("unexpected llmproxy reply: {reply:?}"));
    }

    // Not an instance of the parent service.
    let seen = paths.lock().unwrap().clone();
    println!("kernel saw: {seen:?}");
    for forbidden in ["SetInstances", "TlsCertService", "X509Mint"] {
        if seen.iter().any(|p| p.contains(forbidden)) {
            fail(format!("client-only host called {forbidden}: {seen:?}"));
        }
    }
    if !seen.iter().any(|p| p.ends_with("/GetAllInstances")) {
        fail(format!("the health poller never read ServiceHealthState: {seen:?}"));
    }
    if tokio::net::TcpStream::connect(serve_addr).await.is_ok() {
        fail(format!("client-only host is listening on {serve_addr}"));
    }
    println!("zero-rpc-s2s OK");
    std::process::exit(0);
}
