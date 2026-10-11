// Copyright (c) 2026 Second Star Cavalier LLC
//
// Runtime coverage for W2S REQUEST (ingress) validation in the generated Rust
// REST dispatch module. A real tonic backend serves the generated
// CatalogService; the generated `rest_dispatch::dispatch` / `dispatch_stream`
// are driven directly (no hand-rolled dispatch logic) with both an invalid and
// a valid body. The invalid body must be rejected BEFORE the backend is
// reached; the valid body must reach it. The backend hit counters are what
// make the rejection meaningful: without them a dispatcher that rejected
// everything would look identical to a correct one.

use std::net::SocketAddr;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use tonic::transport::{Channel, Endpoint, Server};

use crate::s2sm::rest_dispatch::{self, RestDispatcher};
use crate::types::services::catalog::catalog_service_server::{
    CatalogService, CatalogServiceServer,
};
use crate::types::services::catalog::{GetItemRequest, GetItemResponse};
use crate::types::sys::mcp::mcp::{McpRequest, McpResponse};

const UNARY_PATH: &str = "/catalog.CatalogService/GetItem";
const STREAM_PATH: &str = "/catalog.CatalogService/WatchItems";
const INVALID_BODY: &[u8] = br#"{"owner":"not-an-email"}"#;
const VALID_BODY: &[u8] = br#"{"owner":"owner@example.com"}"#;

#[derive(Clone, Default)]
struct RecordingBackend {
    unary_hits: Arc<AtomicUsize>,
    stream_hits: Arc<AtomicUsize>,
}

#[tonic::async_trait]
impl CatalogService for RecordingBackend {
    async fn get_item(
        &self,
        request: tonic::Request<GetItemRequest>,
    ) -> Result<tonic::Response<GetItemResponse>, tonic::Status> {
        self.unary_hits.fetch_add(1, Ordering::SeqCst);
        let owner = request.into_inner().owner;
        Ok(tonic::Response::new(GetItemResponse {
            owner,
            ..Default::default()
        }))
    }

    type WatchItemsStream =
        futures::stream::Iter<std::vec::IntoIter<Result<GetItemResponse, tonic::Status>>>;

    async fn watch_items(
        &self,
        request: tonic::Request<GetItemRequest>,
    ) -> Result<tonic::Response<Self::WatchItemsStream>, tonic::Status> {
        self.stream_hits.fetch_add(1, Ordering::SeqCst);
        let owner = request.into_inner().owner;
        let items = vec![Ok(GetItemResponse {
            owner,
            ..Default::default()
        })];
        Ok(tonic::Response::new(futures::stream::iter(items)))
    }

    // Generated for every c2s service with an MCP-exposed rpc; the fixture
    // never opens an MCP session.
    type BrokerCatalogServiceMcpSessionStream =
        futures::stream::Empty<Result<McpResponse, tonic::Status>>;

    async fn broker_catalog_service_mcp_session(
        &self,
        _request: tonic::Request<tonic::Streaming<McpRequest>>,
    ) -> Result<tonic::Response<Self::BrokerCatalogServiceMcpSessionStream>, tonic::Status> {
        Err(tonic::Status::unimplemented("no MCP session in this fixture"))
    }
}

struct FixtureDispatcher {
    channel: Channel,
}

#[tonic::async_trait]
impl RestDispatcher for FixtureDispatcher {
    async fn channel(&self, _service_id: &str) -> Result<Channel, tonic::Status> {
        Ok(self.channel.clone())
    }

    async fn auth_header(&self, _service_id: &str) -> Option<String> {
        None
    }
}

/// Start the real generated CatalogService server on a loopback port and return
/// a dispatcher wired to it plus the backend's hit counters.
async fn start_backend() -> (FixtureDispatcher, RecordingBackend) {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind loopback port");
    let addr: SocketAddr = listener.local_addr().expect("resolve loopback port");
    drop(listener);

    let backend = RecordingBackend::default();
    let serving = backend.clone();
    tokio::spawn(async move {
        Server::builder()
            .add_service(CatalogServiceServer::new(serving))
            .serve(addr)
            .await
            .expect("serve generated CatalogService");
    });

    let endpoint = Endpoint::from_shared(format!("http://{}", addr)).expect("build endpoint");
    let mut channel = None;
    for _ in 0..100 {
        match endpoint.connect().await {
            Ok(ch) => {
                channel = Some(ch);
                break;
            }
            Err(_) => tokio::time::sleep(Duration::from_millis(50)).await,
        }
    }
    let channel = channel.expect("backend never accepted a connection");
    (FixtureDispatcher { channel }, backend)
}

async fn body_text(response: axum::response::Response) -> String {
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .expect("read response body");
    String::from_utf8(bytes.to_vec()).expect("response body is utf-8")
}

#[tokio::test]
async fn rust_w2s_unary_dispatch_validates_request_before_forwarding() {
    let (dispatcher, backend) = start_backend().await;

    let rejected = rest_dispatch::dispatch(&dispatcher, UNARY_PATH, INVALID_BODY, "req-invalid")
        .await
        .expect_err("invalid request body must be rejected, not forwarded");
    assert_eq!(
        rejected.0,
        axum::http::StatusCode::BAD_REQUEST,
        "invalid request rejected with wrong status: {:?}",
        rejected
    );
    assert!(
        rejected.1.contains("request validation failed"),
        "invalid request rejected for the wrong reason: {}",
        rejected.1
    );
    assert_eq!(
        backend.unary_hits.load(Ordering::SeqCst),
        0,
        "invalid request reached the backend service"
    );

    // Positive control: the same seam must still forward a valid request.
    let ok = rest_dispatch::dispatch(&dispatcher, UNARY_PATH, VALID_BODY, "req-valid")
        .await
        .expect("valid request must be forwarded");
    assert!(
        ok.contains("owner@example.com"),
        "valid response did not carry the request owner: {}",
        ok
    );
    assert_eq!(
        backend.unary_hits.load(Ordering::SeqCst),
        1,
        "valid request did not reach the backend service"
    );
}

#[tokio::test]
async fn rust_w2s_stream_dispatch_validates_request_before_forwarding() {
    let (dispatcher, backend) = start_backend().await;

    let rejected =
        rest_dispatch::dispatch_stream(&dispatcher, STREAM_PATH, INVALID_BODY, "sse-invalid").await;
    assert_eq!(
        rejected.status(),
        axum::http::StatusCode::BAD_REQUEST,
        "invalid SSE request was not rejected"
    );
    let rejected_body = body_text(rejected).await;
    assert!(
        rejected_body.contains("request validation failed"),
        "invalid SSE request rejected for the wrong reason: {}",
        rejected_body
    );
    assert_eq!(
        backend.stream_hits.load(Ordering::SeqCst),
        0,
        "invalid SSE request reached the backend service"
    );

    // Positive control: the same seam must still open a valid stream.
    let ok = rest_dispatch::dispatch_stream(&dispatcher, STREAM_PATH, VALID_BODY, "sse-valid").await;
    assert_eq!(
        ok.status(),
        axum::http::StatusCode::OK,
        "valid SSE request was not accepted"
    );
    let ok_body = body_text(ok).await;
    assert!(
        ok_body.contains("owner@example.com"),
        "valid SSE stream did not carry the request owner: {}",
        ok_body
    );
    assert_eq!(
        backend.stream_hits.load(Ordering::SeqCst),
        1,
        "valid SSE request did not reach the backend service"
    );
}
