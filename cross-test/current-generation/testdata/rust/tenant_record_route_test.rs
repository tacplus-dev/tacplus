// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-record HOST fixture (#207). NOT generated:
// run.sh copies this file into the generated crate's tests/ directory and runs
// it with `cargo test --test tenant_record_route_test`, so it drives the REAL
// generated TenantRecordMap ValidationWrapper of the tenant-owned record store
// in-process: a fake handler behind it, standing for the store and its redo
// (a call that never reaches it leaves nothing in either), and in front of it
// the REAL generated grpcserver Authorizer, seeded with a test signing key,
// told its instance runs in /tenants/bbbbbbbb exactly as the ServiceManager
// tells it from its own identity, with the wrapper's route backstop told the
// same and the plane's own generated register_method_auth applied.
//
// What it pins, through the wrapper's own methods: the instance of tenant
// bbbbbbbb never stores an entry for another tenant, whoever the caller is, a
// /system peer included:
//   - a call routed to another tenant is refused (the route check);
//   - an entry outside the routed tenant is refused (the route pin), on the
//     single-type writes, on the key-addressed reads and deletes, and per op
//     of Apply;
//   - a call naming no tenant is malformed;
//   - another tenant's caller is refused at the door;
//   - a /system peer writing the routed tenant's entries passes.

use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use base64::Engine as _;
use key::keys;
use servicemanager::auth::Caller;
use tonic::{Code, Status};
use tstratum_model::s2sm::internal::grpc::servers::vservices::tquery::tenant_record_map::{
    register_method_auth, Authorizer, TenantRecordMapHandler, ValidationWrapper,
};
use tstratum_model::types::services::tmessaging::TMessage;
use tstratum_model::types::vservices::tquery::tenant_record_map_server::TenantRecordMap;
use tstratum_model::types::vservices::tquery::*;

const HOST_NAMESPACE: &str = "/tenants/bbbbbbbb";
const SYSTEM_PEER: &str = "spiffe://example.com/system/services/tmessaging";
const TENANT_A_PEER: &str = "spiffe://example.com/tenants/aaaaaaaa/services/tmessaging";

/// Counts every call that reached the handler: the store and its redo.
struct StoreHandler(Arc<AtomicUsize>);

impl StoreHandler {
    fn reached(&self) {
        self.0.fetch_add(1, Ordering::SeqCst);
    }
}

fn ack() -> RecordWriteResult {
    RecordWriteResult { revision: 1, durability: RecordDurability::Remote as i32, ..Default::default() }
}

fn unimpl() -> Status {
    Status::unimplemented("not driven by this test")
}

#[tonic::async_trait]
impl TenantRecordMapHandler for StoreHandler {
    async fn put_t_message(&self, _c: Option<&Caller>, _r: PutTMessageRequest) -> Result<RecordWriteResult, Status> {
        self.reached();
        Ok(ack())
    }
    async fn delete_t_message(&self, _c: Option<&Caller>, _r: DeleteTMessageRequest) -> Result<RecordWriteResult, Status> {
        self.reached();
        Ok(ack())
    }
    async fn get_t_message(&self, _c: Option<&Caller>, _r: GetTMessageRequest) -> Result<GetTMessageResponse, Status> {
        self.reached();
        Ok(GetTMessageResponse::default())
    }
    async fn apply(&self, _c: Option<&Caller>, req: RecordApplyRequest) -> Result<RecordApplyResponse, Status> {
        self.reached();
        Ok(RecordApplyResponse { results: req.ops.iter().map(|_| ack()).collect(), ..Default::default() })
    }
    async fn watch_t_message_changes(&self, _c: Option<&Caller>, _r: WatchTMessageChangesRequest) -> Result<collection::WatchSource<TMessage, TMessageWatchChange>, Status> {
        self.reached();
        Err(unimpl())
    }
    async fn watch_t_message(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<collection::WatchSource<TMessage, TMessageWatchChange>, Status> {
        self.reached();
        Err(unimpl())
    }

    async fn create_t_message_query(&self, _c: Option<&Caller>, _r: OpenTMessageQueryRequest) -> Result<QueryHandle, Status> { self.reached(); Err(unimpl()) }
    async fn get_t_message_count(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<RecordCount, Status> { self.reached(); Err(unimpl()) }
    async fn get_t_message_sample(&self, _c: Option<&Caller>, _r: SampleTMessageRequest) -> Result<SampleTMessageResponse, Status> { self.reached(); Err(unimpl()) }
    async fn list_t_message(&self, _c: Option<&Caller>, _r: PullTMessageRequest, _p: collection::PageParams) -> Result<collection::Page<TMessage>, Status> { self.reached(); Err(unimpl()) }
    async fn delete_t_message_query(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<QueryStats, Status> { self.reached(); Err(unimpl()) }
    async fn get_t_message_rows(&self, _c: Option<&Caller>, _r: GetTMessageRowsRequest) -> Result<GetTMessageRowsResponse, Status> { self.reached(); Err(unimpl()) }
    async fn update_t_message_query(&self, _c: Option<&Caller>, _r: RefineTMessageRequest) -> Result<QueryStats, Status> { self.reached(); Err(unimpl()) }
    async fn subscribe_t_message_count(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<tonic::Response<std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<RecordCount, Status>> + Send>>>, Status> { self.reached(); Err(unimpl()) }
    async fn insert_if_absent_t_message(&self, _c: Option<&Caller>, _r: InsertIfAbsentTMessageRequest) -> Result<RecordWriteResult, Status> { self.reached(); Err(unimpl()) }
    async fn replace_if_unchanged_t_message(&self, _c: Option<&Caller>, _r: ReplaceIfUnchangedTMessageRequest) -> Result<RecordWriteResult, Status> { self.reached(); Err(unimpl()) }
}

/// The ServiceManager's AuthorizerAdapter, verbatim in what it decides: the
/// real generated grpcserver Authorizer behind the wrapper's Authorizer trait.
struct RealAuthorizer(Arc<grpcserver::authorizer::Authorizer>);

fn to_status(e: grpcserver::authorizer::AuthorizerError) -> Status {
    match e.code {
        16 => Status::unauthenticated(e.message),
        _ => Status::permission_denied(e.message),
    }
}

#[tonic::async_trait]
impl Authorizer for RealAuthorizer {
    async fn authorize(&self, full_method: &str, authorization: Option<&str>) -> Result<HashMap<String, String>, Status> {
        let method = full_method.rsplit('/').next().unwrap_or(full_method);
        self.0
            .authorize_grpc(full_method, method, authorization, &HashMap::new())
            .await
            .map(|tw| grpcserver::authorizer::Authorizer::identity_claims(&tw))
            .map_err(to_status)
    }
    async fn check_at_or_below(&self, full_method: &str, authorization: Option<&str>, target_namespace: &str) -> Result<HashMap<String, String>, Status> {
        self.0
            .check_caller_at_or_below_method(full_method, authorization, target_namespace)
            .map(|tw| grpcserver::authorizer::Authorizer::identity_claims(&tw))
            .map_err(to_status)
    }
    async fn authenticate(&self, full_method: &str, authorization: Option<&str>) -> Result<HashMap<String, String>, Status> {
        self.0
            .authenticate_caller_method(full_method, authorization)
            .map(|tw| grpcserver::authorizer::Authorizer::identity_claims(&tw))
            .map_err(to_status)
    }
}

fn verifying_jwk(key: &keys::ImmutableKey) -> jwk::ImmutableJwk {
    let x = match key.public_key() {
        keys::PublicKey::Ed25519(ed) => base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(ed.as_bytes()),
        _ => panic!("expected an ed25519 public key"),
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

/// A signed service token for `subject` in `namespace`, audience tstratum.
fn service_bearer(key: &keys::ImmutableKey, subject: &str, namespace: &str) -> String {
    let exp = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64 + 3600;
    let mut tok = token::Token::new();
    tok.set_issuer("test-issuer").unwrap();
    tok.set_audience("tstratum").unwrap();
    tok.set_expiration(exp).unwrap();
    tok.set_namespace(namespace).unwrap();
    tok.set_sub_type("service").unwrap();
    tok.set_subject(subject).unwrap();
    tok.sign(key).expect("sign");
    format!("Bearer {}", tok.base64().expect("signed token has base64"))
}

/// The instance of tenant bbbbbbbb, its callers presenting `subject`/`namespace`.
struct Instance {
    wrapper: ValidationWrapper,
    bearer: String,
    reached: Arc<AtomicUsize>,
}

async fn instance(subject: &str, namespace: &str) -> Instance {
    let key = keys::generate_ed25519_key().expect("generate ed25519 key");
    let auth = Arc::new(grpcserver::authorizer::Authorizer::with_seeded_jwks(vec![verifying_jwk(&key)]));
    // As the ServiceManager does: the authorizer learns its instance's namespace
    // from the instance's own identity, the plane's per-method auth_services are
    // registered, and the wrapper's route backstop is told the same namespace.
    auth.set_host_namespace(HOST_NAMESPACE);
    register_method_auth(&auth);
    let reached = Arc::new(AtomicUsize::new(0));
    let wrapper = ValidationWrapper::new(Arc::new(StoreHandler(reached.clone())));
    wrapper.set_host_namespace(&auth.host_namespace());
    wrapper.set_authorize_func(Some(Arc::new(RealAuthorizer(auth.clone())))).await;
    Instance { wrapper, bearer: service_bearer(&key, subject, namespace), reached }
}

impl Instance {
    fn req<T>(&self, msg: T) -> tonic::Request<T> {
        let mut r = tonic::Request::new(msg);
        r.metadata_mut().insert("authorization", self.bearer.parse().unwrap());
        r
    }
    fn reached(&self) -> usize {
        self.reached.load(Ordering::SeqCst)
    }
    async fn put(&self, target: &str, ns: &str) -> Result<RecordWriteResult, Status> {
        self.wrapper.put_t_message(self.req(put_req(target, ns))).await.map(|r| r.into_inner())
    }
}

fn message(ns: &str) -> TMessage {
    TMessage {
        message_id: "m1".to_string(),
        namespace: ns.to_string(),
        owner: "spiffe://example.com/tenants/aaaaaaaa/users/u1".to_string(),
        body: "hello".to_string(),
        ..Default::default()
    }
}

fn put_req(target: &str, ns: &str) -> PutTMessageRequest {
    PutTMessageRequest { item: Some(message(ns)), target_tenant: target.to_string(), ..Default::default() }
}

fn put_op(ns: &str) -> RecordOp {
    RecordOp { op: Some(record_op::Op::PutTMessage(put_req("", ns))), ..Default::default() }
}

/// The call is refused with `code`, its message containing `substr`, and the
/// store was never asked.
fn refused<T: std::fmt::Debug>(p: &Instance, got: Result<T, Status>, code: Code, substr: &str) {
    match got {
        Err(s) if s.code() == code && s.message().contains(substr) => {}
        other => panic!("want {:?} containing {:?}, got {:?}", code, substr, other),
    }
    assert_eq!(p.reached(), 0, "a refused call must not reach the store");
}

// 1. Tenant A's call forced onto B's instance: the route check refuses it.
#[tokio::test]
async fn misrouted_write_is_refused() {
    let p = instance(SYSTEM_PEER, "/system").await;
    refused(&p, p.put("aaaaaaaa", "/tenants/aaaaaaaa").await, Code::PermissionDenied, "routed to tenant aaaaaaaa, this instance serves tenant bbbbbbbb");
}

// 2. A's entry under B's route: the route pin refuses it, a /system peer included.
#[tokio::test]
async fn foreign_entry_under_the_route_is_refused() {
    let p = instance(SYSTEM_PEER, "/system").await;
    refused(
        &p,
        p.put("bbbbbbbb", "/tenants/aaaaaaaa").await,
        Code::PermissionDenied,
        "PutTMessage: item.namespace is not in tenant bbbbbbbb, the tenant this call is routed to; a tenant's instance holds that tenant's entries only",
    );
    // A sibling whose name merely starts with the routed tid is another tenant.
    refused(&p, p.put("bbbbbbbb", "/tenants/bbbbbbbbx").await, Code::PermissionDenied, "is not in tenant bbbbbbbb");
}

// 3. A call naming no target tenant is malformed.
#[tokio::test]
async fn unrouted_call_is_malformed() {
    let p = instance(SYSTEM_PEER, "/system").await;
    refused(&p, p.put("", "/tenants/bbbbbbbb").await, Code::InvalidArgument, "target_tenant");
}

// 4. Apply: the route pin runs per op against the Apply's own route, and an
// Apply routed to another tenant is refused before any op is looked at.
#[tokio::test]
async fn apply_is_held_to_its_route() {
    let p = instance(SYSTEM_PEER, "/system").await;
    let apply = |target: &str, ops: Vec<RecordOp>| RecordApplyRequest { ops, target_tenant: target.to_string(), ..Default::default() };
    refused(
        &p,
        p.wrapper.apply(p.req(apply("bbbbbbbb", vec![put_op("/tenants/aaaaaaaa")]))).await,
        Code::PermissionDenied,
        "Apply (put_t_message): item.namespace is not in tenant bbbbbbbb, the tenant this call is routed to",
    );
    refused(
        &p,
        p.wrapper.apply(p.req(apply("aaaaaaaa", vec![put_op("/tenants/aaaaaaaa")]))).await,
        Code::PermissionDenied,
        "routed to tenant aaaaaaaa, this instance serves tenant bbbbbbbb",
    );
}

// 5. The key-addressed delete and read are held to the route the same way.
#[tokio::test]
async fn delete_and_get_are_held_to_their_route() {
    let p = instance(SYSTEM_PEER, "/system").await;
    let del = DeleteTMessageRequest {
        message_id: "m1".to_string(),
        namespace: "/tenants/aaaaaaaa".to_string(),
        target_tenant: "bbbbbbbb".to_string(),
        ..Default::default()
    };
    refused(&p, p.wrapper.delete_t_message(p.req(del)).await, Code::PermissionDenied, "DeleteTMessage: namespace is not in tenant bbbbbbbb");
    let get = GetTMessageRequest {
        message_id: "m1".to_string(),
        namespace: "/tenants/aaaaaaaa".to_string(),
        target_tenant: "bbbbbbbb".to_string(),
        ..Default::default()
    };
    refused(&p, p.wrapper.get_t_message(p.req(get)).await, Code::PermissionDenied, "GetTMessage: namespace is not in tenant bbbbbbbb");
}

// The streaming rpcs carry the route check too, before the store is asked for
// its source.
#[tokio::test]
async fn watches_are_held_to_their_route() {
    let p = instance(SYSTEM_PEER, "/system").await;
    let changes = WatchTMessageChangesRequest { namespace: "/tenants/aaaaaaaa".to_string(), target_tenant: "aaaaaaaa".to_string(), ..Default::default() };
    match p.wrapper.watch_t_message_changes(p.req(changes)).await {
        Err(s) if s.code() == Code::PermissionDenied => {}
        Err(s) => panic!("WatchTMessageChanges: want PERMISSION_DENIED, got {:?}", s),
        Ok(_) => panic!("WatchTMessageChanges routed to another tenant must be refused"),
    }
    let foreign = WatchTMessageChangesRequest { namespace: "/tenants/aaaaaaaa".to_string(), target_tenant: "bbbbbbbb".to_string(), ..Default::default() };
    match p.wrapper.watch_t_message_changes(p.req(foreign)).await {
        Err(s) if s.code() == Code::PermissionDenied && s.message().contains("is not in tenant bbbbbbbb") => {}
        Err(s) => panic!("WatchTMessageChanges of another tenant's entries: want the route pin, got {:?}", s),
        Ok(_) => panic!("WatchTMessageChanges of another tenant's entries must be refused"),
    }
    let handle = QueryHandle { query_id: "q1".to_string(), target_tenant: "aaaaaaaa".to_string(), ..Default::default() };
    match p.wrapper.watch_t_message(p.req(handle)).await {
        Err(s) if s.code() == Code::PermissionDenied => {}
        Err(s) => panic!("WatchTMessage: want PERMISSION_DENIED, got {:?}", s),
        Ok(_) => panic!("WatchTMessage routed to another tenant must be refused"),
    }
    assert_eq!(p.reached(), 0, "a refused watch must not reach the store");
}

// 6. Tenant A's own caller on B's instance: refused, whatever it routes to.
#[tokio::test]
async fn another_tenants_caller_is_refused() {
    let p = instance(TENANT_A_PEER, "/tenants/aaaaaaaa").await;
    for (target, ns) in [("aaaaaaaa", "/tenants/aaaaaaaa"), ("bbbbbbbb", "/tenants/aaaaaaaa"), ("bbbbbbbb", "/tenants/bbbbbbbb")] {
        let got = p.put(target, ns).await;
        assert!(got.is_err(), "tenant aaaaaaaa's caller writing ({}, {}) on tenant bbbbbbbb's instance must be refused, got {:?}", target, ns, got);
    }
    assert_eq!(p.reached(), 0, "another tenant's caller must not reach the store");
}

// 7. A /system peer writing the routed tenant's own entries passes.
#[tokio::test]
async fn routed_tenants_entries_pass() {
    let p = instance(SYSTEM_PEER, "/system").await;
    p.put("bbbbbbbb", "/tenants/bbbbbbbb").await.expect("an entry of the routed tenant must pass");
    assert_eq!(p.reached(), 1, "the write must reach the store");
    p.put("bbbbbbbb", "/tenants/bbbbbbbb/users/u1").await.expect("an entry below the routed tenant must pass");
    assert_eq!(p.reached(), 2, "the write must reach the store");
    let apply = RecordApplyRequest { ops: vec![put_op("/tenants/bbbbbbbb")], target_tenant: "bbbbbbbb".to_string(), ..Default::default() };
    p.wrapper.apply(p.req(apply)).await.expect("an Apply of the routed tenant's entries must pass");
    assert_eq!(p.reached(), 3, "the Apply must reach the store");
}
