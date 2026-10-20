// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// server-runtime RUST fixture server: hosts the REAL generated ContractApi
// ValidationWrapper (s2sm/internal/grpc/servers/contract/contractclient/
// contract_api.rs) on a real TCP port so another language's generated client
// can drive it over the wire.
//
// As with the java fixture, this file supplies only the generated
// ContractApiHandler trait impl and an Authorizer -- and, as in the java
// fixture, that Authorizer is the REAL generated one
// (chassis/grpcserver/src/authorizer.rs). Validation, PAGE token encoding,
// egress sanitization, auth gating and dispatch all live in the generated
// wrapper.
//
// Startup contract: prints a single JSON line to stdout
//
//     {"grpc":"127.0.0.1:N"}
//
// then serves until SIGINT/SIGTERM.

use std::pin::Pin;
use std::sync::Arc;

use contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::{
    Authorizer, ContractApiHandler, ValidationWrapper,
};
use contractclient_model::types::contract::contractclient::contract_api_server::ContractApiServer;
use contractclient_model::types::contract::contractclient::{
    ContractEvent, ContractPageRequest, ContractRequest, ContractResponse, ContractWatchChange,
    ContractWatchRequest,
};
use grpcserver::authorizer::Authorizer as GrpcAuthorizer;
use tokio::net::TcpListener;
use tokio_stream::wrappers::TcpListenerStream;
use tokio_stream::Stream;

const BEARER: &str = "Bearer fixture-token";

type EventStream = Pin<Box<dyn Stream<Item = Result<ContractEvent, tonic::Status>> + Send>>;

fn valid_response() -> ContractResponse {
    ContractResponse { result_id: "result-123".to_string(), accepted: true, ..Default::default() }
}

fn event(id: &str) -> ContractEvent {
    ContractEvent {
        event_id: id.to_string(),
        tenant_namespace: "tenant.alpha".to_string(),
        payload: "ok".to_string(),
        ..Default::default()
    }
}

struct Handler;

#[tonic::async_trait]
impl ContractApiHandler for Handler {
    async fn get_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        // Never reachable: the generated wrapper authorizes GetContract before
        // dispatch and the fixture wires no verifying key, so the real
        // generated authorizer rejects the opaque bearer. If a client ever
        // sees this marker, auth was bypassed.
        Ok(ContractResponse {
            result_id: "HANDLER-REACHED".to_string(),
            accepted: true,
            ..Default::default()
        })
    }

    async fn get_public_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        Ok(valid_response())
    }

    async fn list_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        let stream = tokio_stream::iter(vec![Ok(event("event-1"))]);
        Ok(tonic::Response::new(Box::pin(stream) as EventStream))
    }

    async fn watch(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractWatchRequest,
    ) -> Result<collection::WatchSource<ContractEvent, ContractWatchChange>, tonic::Status> {
        Err(tonic::Status::unimplemented("watch is out of scope for the server-runtime gate"))
    }

    async fn list_events_paged(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractPageRequest,
        page: collection::PageParams,
    ) -> Result<collection::Page<ContractEvent>, tonic::Status> {
        // TWO keyset pages, matching the go runtime-smoke fixture: the first
        // call returns a resume cursor so the client's auto-pager is forced
        // through the token-CONTINUATION branch on the wire.
        match page.after {
            None => Ok(collection::Page {
                items: vec![event("event-1")],
                next_after: Some(collection::Cursor { keys: vec!["event-1".to_string()] }),
                total: 2,
            }),
            Some(cursor) => {
                if cursor.keys != vec!["event-1".to_string()] {
                    return Err(tonic::Status::invalid_argument(format!(
                        "unexpected resume cursor {:?}",
                        cursor.keys
                    )));
                }
                Ok(collection::Page { items: vec![event("event-2")], next_after: None, total: 2 })
            }
        }
    }

    async fn send_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<ContractResponse>, tonic::Status> {
        Ok(tonic::Response::new(valid_response()))
    }

    async fn assist_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        let stream = tokio_stream::iter(vec![Ok(event("event-reply"))]);
        Ok(tonic::Response::new(Box::pin(stream) as EventStream))
    }
}

// FixtureAuthorizer bridges the generated wrapper's Authorizer trait onto the
// REAL generated grpcserver Authorizer, the same way the generated
// ServiceManager's AuthorizerAdapter does, and mirrors the two seams the java
// fixture wires:
//
// - `authorize` (auth_entitled_users -- GetContract) delegates to the real
//   generated authorizer, constructed over an explicitly empty key set. This is
//   the rust twin of the java fixture's `Authorizer.create()`: real JWT
//   verification, no verifying keys wired, so the fail-closed leg is rejected
//   by generated auth code rather than by a fixture stand-in.
// - `check_at_or_below` (level-or-below, with a target namespace) is a fixture
//   check on the opaque bearer, mirroring the java fixture's `setAuthenticateFunc`.
//   That token is not a JWT and could never verify, so both fixtures
//   short-circuit this seam identically.
// - `authenticate` (auth_any_user with no target namespace -- ListEvents /
//   ListEventsPaged) takes the same opaque-bearer short-circuit with an empty
//   target namespace, as the java fixture's Authenticator does for that call.
//
// Each method maps the verified token's claims to the trait's claims map the
// same way the generated ServiceManager's AuthorizerAdapter does.
struct FixtureAuthorizer {
    authorizer: GrpcAuthorizer,
}

impl FixtureAuthorizer {
    // Empty seed == the java fixture's `Authorizer.create()`: real verification
    // against an explicitly empty key set, never a bypass. No background JWK
    // watch, so the fixture stays offline.
    fn new() -> Self {
        Self { authorizer: GrpcAuthorizer::with_seeded_jwks(Vec::new()) }
    }
}

#[tonic::async_trait]
impl Authorizer for FixtureAuthorizer {
    async fn authorize(
        &self,
        full_method: &str,
        authorization: Option<&str>,
    ) -> Result<std::collections::HashMap<String, String>, tonic::Status> {
        // Same shape as the generated AuthorizerAdapter: derive the bare method
        // name from the full gRPC path and map the authorizer's code back onto
        // a tonic Status.
        let method = full_method.rsplit('/').next().unwrap_or(full_method);
        self.authorizer
            .authorize_grpc(full_method, method, authorization, &std::collections::HashMap::new())
            .await
            .map(|tw| GrpcAuthorizer::identity_claims(&tw))
            .map_err(|e| match e.code {
                16 => tonic::Status::unauthenticated(e.message),
                _ => tonic::Status::permission_denied(e.message),
            })
    }

    async fn check_at_or_below(
        &self,
        authorization: Option<&str>,
        target_namespace: &str,
    ) -> Result<std::collections::HashMap<String, String>, tonic::Status> {
        if authorization != Some(BEARER) {
            return Err(tonic::Status::unauthenticated("unexpected authorization header"));
        }
        if !target_namespace.is_empty() && target_namespace != "/" {
            return Err(tonic::Status::permission_denied(format!(
                "unexpected target namespace {}",
                target_namespace
            )));
        }
        Ok(std::collections::HashMap::new())
    }

    async fn authenticate(
        &self,
        authorization: Option<&str>,
    ) -> Result<std::collections::HashMap<String, String>, tonic::Status> {
        self.check_at_or_below(authorization, "").await
    }
}

// The gate stops fixtures with SIGTERM; SIGINT is honored for interactive use.
async fn shutdown_signal() {
    use tokio::signal::unix::{signal, SignalKind};
    let mut term = signal(SignalKind::terminate()).expect("install SIGTERM handler");
    let mut interrupt = signal(SignalKind::interrupt()).expect("install SIGINT handler");
    tokio::select! {
        _ = term.recv() => {}
        _ = interrupt.recv() => {}
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let wrapper = ValidationWrapper::new(Arc::new(Handler));
    wrapper.set_authorize_func(Some(Arc::new(FixtureAuthorizer::new()))).await;

    let listener = TcpListener::bind("127.0.0.1:0").await?;
    let addr = listener.local_addr()?;
    println!("{{\"grpc\":\"{}\"}}", addr);
    use std::io::Write;
    std::io::stdout().flush()?;

    tonic::transport::Server::builder()
        .add_service(ContractApiServer::new(wrapper))
        .serve_with_incoming_shutdown(TcpListenerStream::new(listener), shutdown_signal())
        .await?;
    Ok(())
}
