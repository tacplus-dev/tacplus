// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-route fixture's harbor build (#258). NOT
// generated: run.sh copies this file into the generated crate's tests/
// directory and runs it with `cargo test --test tenant_route_client_test`, so
// it drives the REAL generated typed CrewPeerService S2S client of harbor.
//
// S2S never crosses tenants (tacplus-dev/roadmap
// decisions/s2s-never-crosses-tenants-2026-10-31.md): a tenant's service routes
// a call to a tenant-owned service to its OWN tid, its service token's tid
// claim. An empty route field is stamped with it; a call naming another
// tenant is refused, PERMISSION_DENIED, before anything is dialled. A /system
// service still names the target tenant.
//
// The client is driven against two channel providers, so "refused before
// sending" is told apart from "sent to tenant X":
//   - a tenant-routed pool with no instances: a call let through asks it for
//     its tenant's channel and fails UNAVAILABLE naming that tenant;
//   - a static channel to an in-process crew peer server that records every
//     request reaching it and the tenant its route field carries.

use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

use harbor_model::s2sm::internal::grpc::clients::services::crew::peer_service::CrewPeerServiceGrpcClient;
use harbor_model::s2sm::TokenProvider;
use harbor_model::types::services::crew::crew_peer_service_server::{CrewPeerService, CrewPeerServiceServer};
use harbor_model::types::services::crew::{GetCrewStatusRequest, GetCrewStatusResponse};
use key::keys;
use servicemanager::{ChannelProvider, ServiceConnection, SpiffeServerCertVerifier};
use tonic::{Code, Request, Response, Status};

const ALPHA: &str = "aaaaaaaa";
const BETA: &str = "bbbbbbbb";

fn now_plus_hour() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs() as i64 + 3600
}

/// A signed service token in `namespace`, with `tid` as its tid claim.
fn service_token(subject: &str, namespace: &str, tid: Option<&str>) -> String {
    let key = keys::generate_ed25519_key().expect("generate ed25519 key");
    let mut tok = token::Token::new();
    tok.set_issuer("test-issuer").unwrap();
    tok.set_audience("crew").unwrap();
    tok.set_expiration(now_plus_hour()).unwrap();
    tok.set_namespace(namespace).unwrap();
    tok.set_sub_type("service").unwrap();
    tok.set_subject(subject).unwrap();
    if let Some(tid) = tid {
        tok.set_tid(tid).unwrap();
    }
    tok.sign(&key).expect("sign");
    tok.base64().expect("signed token has base64").to_string()
}

/// Tenant alpha's harbor: tid aaaaaaaa, namespace /tenants/alpha (kernel#23).
fn alpha_service() -> String {
    service_token("spiffe://example.com/tenants/aaaaaaaa/services/harbor", "/tenants/alpha", Some(ALPHA))
}

/// The /system harbor: in no tenant.
fn system_service() -> String {
    service_token("spiffe://example.com/system/services/harbor", "/system", None)
}

fn token_provider(token: String) -> TokenProvider {
    Arc::new(move || {
        let token = token.clone();
        Box::pin(async move { Ok(token) })
            as Pin<Box<dyn Future<Output = Result<String, Box<dyn std::error::Error + Send + Sync>>> + Send>>
    })
}

/// A tenant-routed pool of crew with no instance of any tenant: nothing can
/// be dialled, and a call let through names the tenant it asked for.
fn empty_tenant_pool() -> ChannelProvider {
    let signing_key = keys::generate_ec_p256_key().expect("generate p256 key");
    let pem = x509::ImmutableCertChain::for_self_signing(signing_key)
        .self_sign("test-root", now_plus_hour(), x509::x509::X509KeyUsage::CertSign, &[])
        .expect("self_sign test root")
        .to_pem()
        .expect("to_pem");
    let (roots, _) = servicemanager::spiffe::build_root_store(&pem);
    let verifier = SpiffeServerCertVerifier::new("example.com", roots).expect("verifier");
    ChannelProvider::new(Arc::new(ServiceConnection::new_tenant_routed("crew", verifier)))
}

/// The crew peer: records the route field of every request that reaches it.
#[derive(Clone, Default)]
struct Recorder(Arc<Mutex<Vec<String>>>);

#[tonic::async_trait]
impl CrewPeerService for Recorder {
    async fn get_crew_status(&self, request: Request<GetCrewStatusRequest>) -> Result<Response<GetCrewStatusResponse>, Status> {
        self.0.lock().unwrap().push(request.get_ref().tenant.clone());
        Ok(Response::new(GetCrewStatusResponse { ready: true, ..Default::default() }))
    }
}

/// A static channel to an in-process crew peer server, and what reached it.
async fn recording_peer() -> (ChannelProvider, Recorder) {
    let recorder = Recorder::default();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.expect("bind");
    let addr = listener.local_addr().expect("local addr");
    let server = tonic::transport::Server::builder().add_service(CrewPeerServiceServer::new(recorder.clone()));
    tokio::spawn(server.serve_with_incoming(tonic::transport::server::TcpIncoming::from(listener)));
    let channel = tonic::transport::Endpoint::from_shared(format!("http://{addr}")).expect("endpoint").connect_lazy();
    (ChannelProvider::from_channel("crew", channel), recorder)
}

fn ask(tenant: &str) -> GetCrewStatusRequest {
    GetCrewStatusRequest { tenant: tenant.to_string(), ..Default::default() }
}

/// The tenant a call let through asked the empty tenant pool for, read off
/// its UNAVAILABLE; None when it was refused before asking.
async fn asked_tenant(token: &str, tenant: &str) -> Result<String, Status> {
    let client = CrewPeerServiceGrpcClient::new(empty_tenant_pool(), Some(token_provider(token.to_string())));
    let err = client.get_crew_status(ask(tenant)).await.expect_err("no crew instance exists");
    if err.code() != Code::Unavailable {
        return Err(err);
    }
    let named = err.message().rsplit("for tenant ").next().unwrap_or_default().to_string();
    Ok(named)
}

#[tokio::test]
async fn a_tenants_service_never_routes_to_another_tenant() {
    let alpha = alpha_service();

    // Routed elsewhere: refused before the pool is asked for any channel.
    let err = asked_tenant(&alpha, BETA).await.expect_err("tenant alpha's service routed to tenant beta");
    assert_eq!(err.code(), Code::PermissionDenied, "{err:?}");
    assert!(err.message().contains("S2S never crosses tenants"), "{}", err.message());

    // Empty: stamped with its own tid, so its own tenant is asked for.
    assert_eq!(asked_tenant(&alpha, "").await.expect("stamped"), ALPHA);
    // Its own tid: asked for as named.
    assert_eq!(asked_tenant(&alpha, ALPHA).await.expect("own tenant"), ALPHA);

    // Against a live peer: the refused call never reaches it, the stamped
    // one arrives carrying tenant alpha's tid in its route field.
    let (cp, recorder) = recording_peer().await;
    let client = CrewPeerServiceGrpcClient::new(cp, Some(token_provider(alpha)));
    let err = client.get_crew_status(ask(BETA)).await.expect_err("routed to tenant beta");
    assert_eq!(err.code(), Code::PermissionDenied, "{err:?}");
    assert!(recorder.0.lock().unwrap().is_empty(), "a refused call reached crew");
    let resp = client.get_crew_status(ask("")).await.expect("an empty route field is stamped");
    assert!(resp.get_ref().ready);
    assert_eq!(*recorder.0.lock().unwrap(), vec![ALPHA.to_string()]);
}

#[tokio::test]
async fn a_tenants_service_without_a_tid_claim_is_refused() {
    let tidless = service_token("spiffe://example.com/tenants/aaaaaaaa/services/harbor", "/tenants/alpha", None);
    for tenant in ["", ALPHA, BETA] {
        let err = asked_tenant(&tidless, tenant).await.expect_err("no tid claim, no tenant");
        assert_eq!(err.code(), Code::PermissionDenied, "{tenant:?}: {err:?}");
    }
}

#[tokio::test]
async fn a_system_service_names_the_target_tenant() {
    let system = system_service();
    assert_eq!(asked_tenant(&system, BETA).await.expect("/system names tenant beta"), BETA);
    assert_eq!(asked_tenant(&system, ALPHA).await.expect("/system names tenant alpha"), ALPHA);
    // It names one: an empty route field is malformed, never defaulted.
    let err = asked_tenant(&system, "").await.expect_err("/system must name the tenant");
    assert_eq!(err.code(), Code::InvalidArgument, "{err:?}");

    let (cp, recorder) = recording_peer().await;
    let client = CrewPeerServiceGrpcClient::new(cp, Some(token_provider(system)));
    client.get_crew_status(ask(BETA)).await.expect("/system reaches tenant beta");
    assert_eq!(*recorder.0.lock().unwrap(), vec![BETA.to_string()]);
}
