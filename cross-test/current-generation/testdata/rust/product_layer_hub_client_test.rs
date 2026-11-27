// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the product-layer fixture's hub build (#475). NOT
// generated: run.sh copies this file into the generated crate's tests/
// directory and runs it with the crate's `cargo test --tests`, so it drives the
// REAL generated typed S2S client of crew.CrewPeerService (tenant-owned, routed
// by its "tenant" field).
//
// What it pins: the edge table of roadmap/PRODUCTS_LAYER.md at the client,
// before anything is sent (products-layer-2026-11-08): a product's service or
// workload never calls a tenant (PERMISSION_DENIED, nothing sent), whatever
// tenant it names; the platform names the tenant it calls; a tenant's service
// reaches its own tenant and no other (#258).
//
// The client is driven over a static channel to an in-process crew peer
// server that answers every call and records the tenant its route field
// carries, so "refused before sending" (PERMISSION_DENIED, nothing recorded) is
// told apart from "sent to tenant X".

use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex};

use hub_model::s2sm::internal::grpc::clients::services::crew::peer_service::{CrewPeerServiceGrpcClient, TokenProvider};
use hub_model::types::services::crew::crew_peer_service_server::{CrewPeerService, CrewPeerServiceServer};
use hub_model::types::services::crew::{GetCrewStatusRequest, GetCrewStatusResponse};
use servicemanager::ChannelProvider;
use tonic::{Code, Request, Response, Status};

const ALPHA: &str = "aaaaaaaa";
const BETA: &str = "bbbbbbbb";

/// An unsigned JWT with a caller's claims: the client reads its own token and
/// never verifies it; the target does.
fn layer_token(sub_type: &str, sub: &str, namespace: &str, tid: Option<&str>) -> String {
    let mut claims = serde_json::json!({
        "iss": "https://kernel.example",
        "sub_type": sub_type,
        "sub": sub,
        "namespace": namespace,
    });
    if let Some(tid) = tid {
        claims["tid"] = serde_json::Value::from(tid);
    }
    let enc = |v: &serde_json::Value| key::base64_utils::encode_base64_url(v.to_string().as_bytes());
    format!("{}.{}.", enc(&serde_json::json!({"alg": "EdDSA", "typ": "JWT"})), enc(&claims))
}

fn token_provider(token: String) -> TokenProvider {
    Arc::new(move || {
        let token = token.clone();
        Box::pin(async move { Ok(token) })
            as Pin<Box<dyn Future<Output = Result<String, Box<dyn std::error::Error + Send + Sync>>> + Send>>
    })
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

/// One case: a caller (its own token's claims) naming a tenant, and the
/// tenant the call must reach (None: refused PERMISSION_DENIED, nothing sent).
struct Case {
    name: &'static str,
    sub_type: &'static str,
    sub: &'static str,
    namespace: &'static str,
    tid: Option<&'static str>,
    named: &'static str,
    reaches: Option<&'static str>,
}

/// Drives each case through a fresh generated client over a fresh peer.
async fn drive(cases: &[Case]) {
    for case in cases {
        let (cp, recorder) = recording_peer().await;
        let token = layer_token(case.sub_type, case.sub, case.namespace, case.tid);
        let client = CrewPeerServiceGrpcClient::new(cp, Some(token_provider(token)));
        let got = client
            .get_crew_status(GetCrewStatusRequest { tenant: case.named.to_string(), ..Default::default() })
            .await;
        let reached = recorder.0.lock().unwrap().clone();
        match case.reaches {
            None => {
                let err = got.expect_err(case.name);
                assert_eq!(
                    err.code(),
                    Code::PermissionDenied,
                    "{} calling tenant-owned crew: want PERMISSION_DENIED, got {err:?}",
                    case.name
                );
                assert!(reached.is_empty(), "{} calling tenant-owned crew: a refused call reached crew: {reached:?}", case.name);
            }
            Some(want) => {
                let resp = got.unwrap_or_else(|e| panic!("{} calling tenant-owned crew: want it sent to {want}, got {e:?}", case.name));
                assert!(resp.get_ref().ready, "{}", case.name);
                assert_eq!(reached, vec![want.to_string()], "{} calling tenant-owned crew", case.name);
            }
        }
    }
}

#[tokio::test]
async fn a_product_never_calls_a_tenant() {
    drive(&[
        Case {
            name: "product ledger's service naming tenant bbbbbbbb",
            sub_type: "service",
            sub: "spiffe://example.com/products/ledger/services/billing",
            namespace: "/products/ledger",
            tid: None,
            named: BETA,
            reaches: None,
        },
        Case {
            name: "product ledger's workload naming tenant bbbbbbbb",
            sub_type: "workload",
            sub: "spiffe://example.com/products/ledger/services/billing/workloads/w-1",
            namespace: "/products/ledger",
            tid: None,
            named: BETA,
            reaches: None,
        },
    ])
    .await;
}

#[tokio::test]
async fn the_platform_and_a_tenant_call_a_tenant_within_the_route_rules() {
    drive(&[
        Case {
            name: "the platform's service naming tenant bbbbbbbb",
            sub_type: "service",
            sub: "spiffe://example.com/system/services/hub",
            namespace: "/system",
            tid: None,
            named: BETA,
            reaches: Some(BETA),
        },
        Case {
            name: "tenant alpha's service, its own tenant (empty route field)",
            sub_type: "service",
            sub: "spiffe://example.com/tenants/aaaaaaaa/services/crew",
            namespace: "/tenants/alpha",
            tid: Some(ALPHA),
            named: "",
            reaches: Some(ALPHA),
        },
        Case {
            name: "tenant alpha's service naming tenant bbbbbbbb",
            sub_type: "service",
            sub: "spiffe://example.com/tenants/aaaaaaaa/services/crew",
            namespace: "/tenants/alpha",
            tid: Some(ALPHA),
            named: BETA,
            reaches: None,
        },
    ])
    .await;
}
