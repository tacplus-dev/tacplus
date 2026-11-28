// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the product-layer fixture's billing build (#475). NOT
// generated: run.sh copies this file into the generated crate's tests/
// directory and runs it with the crate's `cargo test --tests`, so it drives the
// REAL generated typed S2S clients of meter.MeterService (product ledger's) and
// hub.HubService (the platform's).
//
// What it pins: the edge table of roadmap/PRODUCTS_LAYER.md at the client,
// before anything is sent (products-layer-2026-11-08). Into product ledger:
// its own services and a tenant's pass; another product's and the platform's
// are refused PERMISSION_DENIED and never sent. Into the platform: every layer
// passes.
//
// Each client is driven over a static channel to an in-process server of its
// target that answers every call and counts the calls reaching it, so "refused
// before sending" (PERMISSION_DENIED, nothing counted) is told apart from
// "sent" (answered, counted once).

use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use billing_model::s2sm::internal::grpc::clients::services::hub::service::HubServiceGrpcClient;
use billing_model::s2sm::internal::grpc::clients::services::meter::service::{MeterServiceGrpcClient, TokenProvider};
use billing_model::types::services::hub::hub_service_server::{HubService, HubServiceServer};
use billing_model::types::services::hub::{GetHubStatusRequest, GetHubStatusResponse, GetUsageRequest, GetUsageResponse};
use billing_model::types::services::meter::meter_service_server::{MeterService, MeterServiceServer};
use billing_model::types::services::meter::{
    CreateReadingRequest, CreateReadingResponse, GetMeterReadingRequest, GetMeterReadingResponse,
};
use servicemanager::ChannelProvider;
use tonic::{Code, Request, Response, Status};

/// A caller in a layer: the claims of its own service (or workload) token.
struct LayerCaller {
    name: &'static str,
    sub_type: &'static str,
    sub: &'static str,
    namespace: &'static str,
    tid: Option<&'static str>,
}

const LEDGER_SERVICE: LayerCaller = LayerCaller {
    name: "product ledger's service",
    sub_type: "service",
    sub: "spiffe://example.com/products/ledger/services/billing",
    namespace: "/products/ledger",
    tid: None,
};
const LEDGER_WORKLOAD: LayerCaller = LayerCaller {
    name: "product ledger's workload",
    sub_type: "workload",
    sub: "spiffe://example.com/products/ledger/services/billing/workloads/w-1",
    namespace: "/products/ledger",
    tid: None,
};
const BOOKS_SERVICE: LayerCaller = LayerCaller {
    name: "product books' service",
    sub_type: "service",
    sub: "spiffe://example.com/products/books/services/audit",
    namespace: "/products/books",
    tid: None,
};
const SYSTEM_SERVICE: LayerCaller = LayerCaller {
    name: "the platform's service",
    sub_type: "service",
    sub: "spiffe://example.com/system/services/hub",
    namespace: "/system",
    tid: None,
};
const TENANT_SERVICE: LayerCaller = LayerCaller {
    name: "tenant alpha's service",
    sub_type: "service",
    sub: "spiffe://example.com/tenants/aaaaaaaa/services/crew",
    namespace: "/tenants/alpha",
    tid: Some("aaaaaaaa"),
};

/// An unsigned JWT with the caller's claims: the client reads its own token
/// and never verifies it; the target does.
fn layer_token(caller: &LayerCaller) -> String {
    let mut claims = serde_json::json!({
        "iss": "https://kernel.example",
        "sub_type": caller.sub_type,
        "sub": caller.sub,
        "namespace": caller.namespace,
    });
    if let Some(tid) = caller.tid {
        claims["tid"] = serde_json::Value::from(tid);
    }
    let enc = |v: &serde_json::Value| key::base64_utils::encode_base64_url(v.to_string().as_bytes());
    format!("{}.{}.", enc(&serde_json::json!({"alg": "EdDSA", "typ": "JWT"})), enc(&claims))
}

fn token_provider(caller: &LayerCaller) -> TokenProvider {
    let token = layer_token(caller);
    Arc::new(move || {
        let token = token.clone();
        Box::pin(async move { Ok(token) })
            as Pin<Box<dyn Future<Output = Result<String, Box<dyn std::error::Error + Send + Sync>>> + Send>>
    })
}

/// Product ledger's meter: answers every call and counts it.
#[derive(Clone, Default)]
struct Meter(Arc<AtomicUsize>);

#[tonic::async_trait]
impl MeterService for Meter {
    async fn get_meter_reading(
        &self,
        _request: Request<GetMeterReadingRequest>,
    ) -> Result<Response<GetMeterReadingResponse>, Status> {
        self.0.fetch_add(1, Ordering::SeqCst);
        Ok(Response::new(GetMeterReadingResponse { units: 1, ..Default::default() }))
    }

    async fn create_reading(&self, _request: Request<CreateReadingRequest>) -> Result<Response<CreateReadingResponse>, Status> {
        Err(Status::unimplemented("not driven by this test"))
    }
}

/// The platform's hub: answers every call and counts it.
#[derive(Clone, Default)]
struct Hub(Arc<AtomicUsize>);

#[tonic::async_trait]
impl HubService for Hub {
    async fn get_hub_status(&self, _request: Request<GetHubStatusRequest>) -> Result<Response<GetHubStatusResponse>, Status> {
        self.0.fetch_add(1, Ordering::SeqCst);
        Ok(Response::new(GetHubStatusResponse { ready: true, ..Default::default() }))
    }

    async fn get_usage(&self, _request: Request<GetUsageRequest>) -> Result<Response<GetUsageResponse>, Status> {
        Err(Status::unimplemented("not driven by this test"))
    }
}

/// A static channel to `router`'s in-process server, named `service`.
async fn serve(service: &str, router: tonic::transport::server::Router) -> ChannelProvider {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.expect("bind");
    let addr = listener.local_addr().expect("local addr");
    tokio::spawn(router.serve_with_incoming(tonic::transport::server::TcpIncoming::from(listener)));
    let channel = tonic::transport::Endpoint::from_shared(format!("http://{addr}")).expect("endpoint").connect_lazy();
    ChannelProvider::from_channel(service.to_string(), channel)
}

#[tokio::test]
async fn a_product_never_calls_another_product_or_is_called_by_the_platform() {
    let meter = Meter::default();
    let cp = serve("meter", tonic::transport::Server::builder().add_service(MeterServiceServer::new(meter.clone()))).await;

    for (caller, refused) in [
        (&LEDGER_SERVICE, false),
        (&LEDGER_WORKLOAD, false),
        (&TENANT_SERVICE, false),
        (&BOOKS_SERVICE, true),
        (&SYSTEM_SERVICE, true),
    ] {
        let before = meter.0.load(Ordering::SeqCst);
        let client = MeterServiceGrpcClient::new(cp.clone(), Some(token_provider(caller)));
        let got = client.get_meter_reading(GetMeterReadingRequest::default()).await;
        let sent = meter.0.load(Ordering::SeqCst) - before;
        if refused {
            let err = got.expect_err(caller.name);
            assert_eq!(
                err.code(),
                Code::PermissionDenied,
                "{} calling product ledger's meter: want PERMISSION_DENIED, got {err:?}",
                caller.name
            );
            assert!(err.message().contains("products-layer-2026-11-08"), "{}: {}", caller.name, err.message());
            assert_eq!(sent, 0, "{} calling product ledger's meter: a refused call reached meter", caller.name);
        } else {
            let resp = got.unwrap_or_else(|e| panic!("{} calling product ledger's meter: want it sent, got {e:?}", caller.name));
            assert_eq!(resp.get_ref().units, 1, "{}", caller.name);
            assert_eq!(sent, 1, "{} calling product ledger's meter: want one call sent", caller.name);
        }
    }
}

#[tokio::test]
async fn every_layer_calls_the_platform() {
    let hub = Hub::default();
    let cp = serve("hub", tonic::transport::Server::builder().add_service(HubServiceServer::new(hub.clone()))).await;

    for caller in [&LEDGER_SERVICE, &LEDGER_WORKLOAD, &BOOKS_SERVICE, &SYSTEM_SERVICE, &TENANT_SERVICE] {
        let before = hub.0.load(Ordering::SeqCst);
        let client = HubServiceGrpcClient::new(cp.clone(), Some(token_provider(caller)));
        let resp = client
            .get_hub_status(GetHubStatusRequest::default())
            .await
            .unwrap_or_else(|e| panic!("{} calling the platform's hub: want it sent, got {e:?}", caller.name));
        assert!(resp.get_ref().ready, "{}", caller.name);
        assert_eq!(hub.0.load(Ordering::SeqCst) - before, 1, "{} calling the platform's hub: want one call sent", caller.name);
    }
}
