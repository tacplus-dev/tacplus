// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the record-egress HOST fixture (#163). NOT generated:
// run.sh copies this file into the generated crate's tests/ directory and runs
// it with `cargo test --test record_write_guard_test`, so it drives the REAL
// generated EgressRecordMap ValidationWrapper in-process: a fake handler behind
// it, a fake authorizer in front of it, and the record write surface's guards
// between. Twin of testdata/go/record_write_guard_test.go, case for case.
//
// What it pins, through the wrapper's own methods:
//   - durability: a write below VaultRow's REMOTE floor is refused, a write
//     naming none reaches the handler at REMOTE, and a write the store acks
//     below that is not answered;
//   - tenant: a /tenants caller is pinned to its own tenant (stamped when
//     unset, refused when foreign), a /system peer is not;
//   - Apply: ops out of canonical lock order or naming one entry twice are
//     refused, each op is authorized as its single-type rpc, the handler sees
//     the highest floor, and a short answer is not returned.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use base64::Engine as _;
use egressstratum_model::s2sm::internal::grpc::servers::vservices::egressquery::egress_record_map::{
    Authorizer, EgressRecordMapHandler, ValidationWrapper,
};
use egressstratum_model::types::services::egressvault::{VaultAuditRow, VaultRow};
use egressstratum_model::types::vservices::egressquery::egress_record_map_server::EgressRecordMap;
use egressstratum_model::types::vservices::egressquery::*;
use servicemanager::auth::Caller;
use tonic::{Code, Status};

const REMOTE: i32 = RecordDurability::Remote as i32;
const LOCAL: i32 = RecordDurability::Local as i32;
const MEMORY: i32 = RecordDurability::Memory as i32;

/// What the handler saw, and how it answers.
#[derive(Default)]
struct Seen {
    put: Option<PutVaultRowRequest>,
    del: Option<DeleteVaultRowRequest>,
    apply: Option<RecordApplyRequest>,
    ack: i32,
    /// Results Apply answers with; None answers one per op.
    results: Option<usize>,
}

/// Implements only the write rpcs the test drives; everything else is
/// unimplemented.
struct WriteGuardHandler(Arc<Mutex<Seen>>);

fn unimpl() -> Status {
    Status::unimplemented("not driven by this test")
}

#[tonic::async_trait]
impl EgressRecordMapHandler for WriteGuardHandler {
    async fn put_vault_row(&self, _caller: Option<&Caller>, req: PutVaultRowRequest) -> Result<RecordWriteResult, Status> {
        let mut s = self.0.lock().unwrap();
        s.put = Some(req);
        Ok(RecordWriteResult { revision: 1, durability: s.ack, ..Default::default() })
    }
    async fn delete_vault_row(&self, _caller: Option<&Caller>, req: DeleteVaultRowRequest) -> Result<RecordWriteResult, Status> {
        let mut s = self.0.lock().unwrap();
        s.del = Some(req);
        Ok(RecordWriteResult { revision: 2, durability: s.ack, ..Default::default() })
    }
    async fn apply(&self, _caller: Option<&Caller>, req: RecordApplyRequest) -> Result<RecordApplyResponse, Status> {
        let mut s = self.0.lock().unwrap();
        let n = s.results.unwrap_or(req.ops.len());
        let ack = s.ack;
        s.apply = Some(req);
        Ok(RecordApplyResponse {
            results: (0..n).map(|i| RecordWriteResult { revision: i as i64 + 1, durability: ack, ..Default::default() }).collect(),
            ..Default::default()
        })
    }

    async fn create_vault_audit_row_query(&self, _c: Option<&Caller>, _r: OpenVaultAuditRowQueryRequest) -> Result<QueryHandle, Status> { Err(unimpl()) }
    async fn get_vault_audit_row_count(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<RecordCount, Status> { Err(unimpl()) }
    async fn get_vault_audit_row_sample(&self, _c: Option<&Caller>, _r: SampleVaultAuditRowRequest) -> Result<SampleVaultAuditRowResponse, Status> { Err(unimpl()) }
    async fn list_vault_audit_row(&self, _c: Option<&Caller>, _r: PullVaultAuditRowRequest, _p: collection::PageParams) -> Result<collection::Page<VaultAuditRow>, Status> { Err(unimpl()) }
    async fn delete_vault_audit_row_query(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<QueryStats, Status> { Err(unimpl()) }
    async fn get_vault_audit_row_rows(&self, _c: Option<&Caller>, _r: GetVaultAuditRowRowsRequest) -> Result<GetVaultAuditRowRowsResponse, Status> { Err(unimpl()) }
    async fn watch_vault_audit_row_changes(&self, _c: Option<&Caller>, _r: WatchVaultAuditRowChangesRequest) -> Result<collection::WatchSource<VaultAuditRow, VaultAuditRowWatchChange>, Status> { Err(unimpl()) }
    async fn get_vault_audit_row(&self, _c: Option<&Caller>, _r: GetVaultAuditRowRequest) -> Result<GetVaultAuditRowResponse, Status> { Err(unimpl()) }
    async fn insert_if_absent_vault_audit_row(&self, _c: Option<&Caller>, _r: InsertIfAbsentVaultAuditRowRequest) -> Result<RecordWriteResult, Status> { Err(unimpl()) }
    async fn create_vault_row_query(&self, _c: Option<&Caller>, _r: OpenVaultRowQueryRequest) -> Result<QueryHandle, Status> { Err(unimpl()) }
    async fn get_vault_row_count(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<RecordCount, Status> { Err(unimpl()) }
    async fn get_vault_row_sample(&self, _c: Option<&Caller>, _r: SampleVaultRowRequest) -> Result<SampleVaultRowResponse, Status> { Err(unimpl()) }
    async fn list_vault_row(&self, _c: Option<&Caller>, _r: PullVaultRowRequest, _p: collection::PageParams) -> Result<collection::Page<VaultRow>, Status> { Err(unimpl()) }
    async fn delete_vault_row_query(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<QueryStats, Status> { Err(unimpl()) }
    async fn get_vault_row_rows(&self, _c: Option<&Caller>, _r: GetVaultRowRowsRequest) -> Result<GetVaultRowRowsResponse, Status> { Err(unimpl()) }
    async fn update_vault_row_query(&self, _c: Option<&Caller>, _r: RefineVaultRowRequest) -> Result<QueryStats, Status> { Err(unimpl()) }
    async fn watch_vault_row(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<collection::WatchSource<VaultRow, VaultRowWatchChange>, Status> { Err(unimpl()) }
    async fn subscribe_vault_row_count(&self, _c: Option<&Caller>, _r: QueryHandle) -> Result<tonic::Response<std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<RecordCount, Status>> + Send>>>, Status> { Err(unimpl()) }
    async fn watch_vault_row_changes(&self, _c: Option<&Caller>, _r: WatchVaultRowChangesRequest) -> Result<collection::WatchSource<VaultRow, VaultRowWatchChange>, Status> { Err(unimpl()) }
    async fn get_vault_row(&self, _c: Option<&Caller>, _r: GetVaultRowRequest) -> Result<GetVaultRowResponse, Status> { Err(unimpl()) }
    async fn insert_if_absent_vault_row(&self, _c: Option<&Caller>, _r: InsertIfAbsentVaultRowRequest) -> Result<RecordWriteResult, Status> { Err(unimpl()) }
    async fn replace_if_unchanged_vault_row(&self, _c: Option<&Caller>, _r: ReplaceIfUnchangedVaultRowRequest) -> Result<RecordWriteResult, Status> { Err(unimpl()) }
}

/// Admits every caller and refuses every full method ending in one of deny.
struct DenyAuthorizer(Vec<&'static str>);

#[tonic::async_trait]
impl Authorizer for DenyAuthorizer {
    async fn authorize(&self, full_method: &str, _authorization: Option<&str>) -> Result<HashMap<String, String>, Status> {
        for d in &self.0 {
            if full_method.ends_with(&format!("/{}", d)) {
                return Err(Status::permission_denied(format!("denied: {}", d)));
            }
        }
        Ok(HashMap::new())
    }
    async fn check_at_or_below(&self, _full_method: &str, _authorization: Option<&str>, _target_namespace: &str) -> Result<HashMap<String, String>, Status> {
        Ok(HashMap::new())
    }
    async fn authenticate(&self, _full_method: &str, _authorization: Option<&str>) -> Result<HashMap<String, String>, Status> {
        Ok(HashMap::new())
    }
}

/// A wrapper whose callers present a token in namespace ns. The fake authorizer
/// stands in for the verification; the wrapper re-reads the caller's claims
/// from the same header, exactly as it does behind the real one.
struct Server {
    wrapper: ValidationWrapper,
    bearer: String,
    seen: Arc<Mutex<Seen>>,
}

async fn write_guard_server(seen: &Arc<Mutex<Seen>>, ns: &str, deny: Vec<&'static str>) -> Server {
    let wrapper = ValidationWrapper::new(Arc::new(WriteGuardHandler(seen.clone())));
    wrapper.set_authorize_func(Some(Arc::new(DenyAuthorizer(deny)))).await;
    let enc = |v: serde_json::Value| base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(v.to_string());
    let bearer = format!(
        "Bearer {}.{}.sig",
        enc(serde_json::json!({"typ": "JWT", "alg": "EdDSA"})),
        enc(serde_json::json!({"iss": "test", "sub": format!("spiffe://example.com{}/services/egressvault", ns), "namespace": ns})),
    );
    Server { wrapper, bearer, seen: seen.clone() }
}

impl Server {
    fn req<T>(&self, msg: T) -> tonic::Request<T> {
        let mut r = tonic::Request::new(msg);
        r.metadata_mut().insert("authorization", self.bearer.parse().unwrap());
        r
    }
    async fn put(&self, msg: PutVaultRowRequest) -> Result<RecordWriteResult, Status> {
        self.wrapper.put_vault_row(self.req(msg)).await.map(|r| r.into_inner())
    }
    async fn delete(&self, msg: DeleteVaultRowRequest) -> Result<RecordWriteResult, Status> {
        self.wrapper.delete_vault_row(self.req(msg)).await.map(|r| r.into_inner())
    }
    async fn apply(&self, ops: Vec<RecordOp>, durability: i32) -> Result<RecordApplyResponse, Status> {
        self.wrapper.apply(self.req(RecordApplyRequest { ops, durability, ..Default::default() })).await.map(|r| r.into_inner())
    }
}

fn vault_row(id: &str, ns: &str) -> Option<VaultRow> {
    Some(VaultRow { row_id: id.to_string(), namespace: ns.to_string(), owner: "o".to_string(), ..Default::default() })
}

fn put_req(id: &str, ns: &str) -> PutVaultRowRequest {
    PutVaultRowRequest { item: vault_row(id, ns), ..Default::default() }
}

fn put_op(id: &str) -> RecordOp {
    RecordOp { op: Some(record_op::Op::PutVaultRow(put_req(id, "/tenants/acme"))), ..Default::default() }
}

fn audit_op(seq: i64) -> RecordOp {
    RecordOp {
        op: Some(record_op::Op::InsertIfAbsentVaultAuditRow(InsertIfAbsentVaultAuditRowRequest {
            item: Some(VaultAuditRow { seq, namespace: "/tenants/acme".to_string(), owner: "o".to_string(), ..Default::default() }),
            ..Default::default()
        })),
        ..Default::default()
    }
}

fn want_code<T: std::fmt::Debug>(got: Result<T, Status>, code: Code, substr: &str) {
    match got {
        Err(s) if s.code() == code && s.message().contains(substr) => {}
        other => panic!("want {:?} containing {:?}, got {:?}", code, substr, other),
    }
}

fn seen(ack: i32) -> Arc<Mutex<Seen>> {
    Arc::new(Mutex::new(Seen { ack, ..Default::default() }))
}

#[tokio::test]
async fn record_write_guard_durability_floor() {
    let s = seen(REMOTE);
    let p = write_guard_server(&s, "/system", vec![]).await;

    let mut below = put_req("r1", "/tenants/acme");
    below.durability = MEMORY;
    want_code(p.put(below).await, Code::InvalidArgument, "below the record type's declared floor RECORD_DURABILITY_REMOTE");
    assert!(s.lock().unwrap().put.is_none(), "a write below the floor must not reach the handler");

    p.put(put_req("r1", "/tenants/acme")).await.expect("a write naming no durability must be accepted");
    assert_eq!(s.lock().unwrap().put.as_ref().unwrap().durability, REMOTE, "the handler must receive the floor");

    s.lock().unwrap().ack = LOCAL;
    want_code(p.put(put_req("r1", "/tenants/acme")).await, Code::Internal, "acked the write below the durability the request required");
    s.lock().unwrap().ack = REMOTE;
    p.put(put_req("r1", "/tenants/acme")).await.expect("a write acked at the floor is answered");
}

#[tokio::test]
async fn record_write_guard_tenant_pin() {
    let s = seen(REMOTE);
    let tenant = write_guard_server(&s, "/tenants/acme", vec![]).await;

    // A key-addressed write names its tenant beside the key; unset is stamped.
    // (A row's own tenant field is required by VaultRow, so validation refuses
    // an unset one before any pin runs.)
    tenant
        .delete(DeleteVaultRowRequest { row_id: "r1".to_string(), ..Default::default() })
        .await
        .expect("a tenant caller's unset tenant must be stamped, not refused");
    assert_eq!(tenant.seen.lock().unwrap().del.as_ref().unwrap().namespace, "/tenants/acme");
    want_code(
        tenant
            .delete(DeleteVaultRowRequest { row_id: "r1".to_string(), namespace: "/tenants/beta".to_string(), ..Default::default() })
            .await,
        Code::InvalidArgument,
        "DeleteVaultRow: namespace names a tenant the caller neither owns nor is linked to",
    );
    tenant.put(put_req("r1", "/tenants/acme")).await.expect("a tenant caller writing its own tenant's row must pass");
    want_code(
        tenant.put(put_req("r1", "/tenants/beta")).await,
        Code::InvalidArgument,
        "PutVaultRow: item.namespace names a tenant the caller neither owns nor is linked to",
    );

    let system = write_guard_server(&s, "/system", vec![]).await;
    system.put(put_req("r1", "/tenants/beta")).await.expect("a /system peer keeps the fleet-wide reach");
    assert_eq!(
        s.lock().unwrap().put.as_ref().unwrap().item.as_ref().unwrap().namespace,
        "/tenants/beta",
        "a /system peer's tenant must pass untouched"
    );
}

#[tokio::test]
async fn record_apply_guard() {
    let s = seen(REMOTE);
    let p = write_guard_server(&s, "/system", vec![]).await;

    // egressvault.VaultAuditRow sorts before egressvault.VaultRow.
    want_code(p.apply(vec![put_op("a"), audit_op(1)], 0).await, Code::InvalidArgument, "out of canonical lock order");
    want_code(p.apply(vec![put_op("a"), put_op("a")], 0).await, Code::InvalidArgument, "ops[1] is out of canonical lock order");
    // Integer keys order numerically: 9 before 10.
    p.apply(vec![audit_op(9), audit_op(10)], 0).await.expect("integer keys order numerically");
    want_code(p.apply(vec![RecordOp::default()], 0).await, Code::InvalidArgument, "ops[0] names no operation");

    p.apply(vec![audit_op(1), put_op("a"), put_op("b")], 0).await.expect("ops in canonical order must be accepted");
    assert_eq!(
        s.lock().unwrap().apply.as_ref().unwrap().durability,
        REMOTE,
        "an Apply is acked at the highest floor its ops involve (REMOTE)"
    );
    want_code(
        p.apply(vec![audit_op(1)], MEMORY).await,
        Code::InvalidArgument,
        "the request asks for a durability below egressvault.VaultAuditRow's declared floor",
    );

    s.lock().unwrap().results = Some(1);
    want_code(p.apply(vec![put_op("a"), put_op("b")], 0).await, Code::Internal, "answered 1 results for 2 ops");
    {
        let mut g = s.lock().unwrap();
        g.results = None;
        g.ack = LOCAL;
    }
    want_code(p.apply(vec![put_op("a")], 0).await, Code::Internal, "acked ops[0] below the durability the request required");

    // Each op is authorized as its single-type rpc: Apply itself is allowed,
    // PutVaultRow is not, so an Apply carrying a Put is refused with the
    // authorizer's own answer and never reaches the handler.
    {
        let mut g = s.lock().unwrap();
        g.ack = REMOTE;
        g.apply = None;
    }
    let denied = write_guard_server(&s, "/system", vec!["PutVaultRow"]).await;
    want_code(denied.apply(vec![put_op("a")], 0).await, Code::PermissionDenied, "denied: PutVaultRow");
    assert!(
        s.lock().unwrap().apply.is_none(),
        "an Apply carrying an op its caller may not make must not reach the handler"
    );
    denied.apply(vec![audit_op(1)], 0).await.expect("an Apply whose ops the caller may all make must pass");

    // The tenant pin runs per op.
    let tenant = write_guard_server(&s, "/tenants/acme", vec![]).await;
    let foreign = RecordOp { op: Some(record_op::Op::PutVaultRow(put_req("a", "/tenants/beta"))), ..Default::default() };
    want_code(
        tenant.apply(vec![foreign], 0).await,
        Code::InvalidArgument,
        "Apply (put_vault_row): item.namespace names a tenant the caller neither owns nor is linked to",
    );
}
