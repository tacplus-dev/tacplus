// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the quorum-write fixture. NOT generated: run.sh copies this
// file into the generated crate's tests/ directory and runs it with
// `cargo test --test store_roll_test`.
//
// What it pins: a Rust service declaring a keyed (spec.service_store) HOSTS the
// synthesized CredPairStoreService and runs its disk-store roll scheduler, as Go
// and Java do. Until 2026-10-15 the shared own-package filter dropped the store
// service for Rust, so the Handler had no cred_pair_store_grpc_impl getter, the
// ServiceManager served no CredPairStoreService and had no roll scheduler, and all
// of that compiled. This drives rows through the generated server wrapper into a
// Handler's store, then runs the generated roll scheduler the ServiceManager
// spawns and reads the baseline it wrote: only this node's rows (the
// cred_pair_local_rows narrowing), decoded by the generated helpers.

use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use quorumvault_model::s2sm::diskstore_stores;
use quorumvault_model::s2sm::handler::Handler;
use quorumvault_model::s2sm::internal::grpc::servers::services::quorumvault::cred_pair_store_service::{
    Authorizer, CredPairStoreServiceHandler, ValidationWrapper,
};
use quorumvault_model::s2sm::internal::grpc::servers::services::quorumvault::vault_service::{
    VaultServiceHandler, VaultServiceHandlerStub,
};
use quorumvault_model::types::services::quorumvault::cred_pair_store_service_server::CredPairStoreService;
use quorumvault_model::types::services::quorumvault::CredPair;

/// The service's own store: rows by client_id.
#[derive(Default)]
struct MemStore(Mutex<BTreeMap<String, CredPair>>);

#[async_trait::async_trait]
impl CredPairStoreServiceHandler for MemStore {
    async fn put(&self, _caller: Option<&servicemanager::auth::Caller>, req: CredPair) -> Result<(), tonic::Status> {
        self.0.lock().unwrap().insert(req.client_id.clone(), req);
        Ok(())
    }
    async fn get(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: quorumvault_model::types::services::quorumvault::CredPairStoreKeyRequest,
    ) -> Result<CredPair, tonic::Status> {
        self.0.lock().unwrap().get(&req.client_id).cloned().ok_or_else(|| tonic::Status::not_found(req.client_id))
    }
    async fn delete(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: quorumvault_model::types::services::quorumvault::CredPairStoreKeyRequest,
    ) -> Result<(), tonic::Status> {
        self.0.lock().unwrap().remove(&req.client_id);
        Ok(())
    }
    async fn list(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
    ) -> Result<quorumvault_model::types::services::quorumvault::CredPairStoreListResponse, tonic::Status> {
        Ok(quorumvault_model::types::services::quorumvault::CredPairStoreListResponse {
            items: self.0.lock().unwrap().values().cloned().collect(),
            ..Default::default()
        })
    }
}

/// This node owns every row but "b".
struct NotB;
impl diskstore::LocalRows for NotB {
    fn is_local_row(&self, key: &str) -> bool {
        key != "b"
    }
}

struct TestHandler {
    store: Arc<MemStore>,
}

#[async_trait::async_trait]
impl Handler for TestHandler {
    fn vault_grpc_impl(&self) -> Arc<dyn VaultServiceHandler> {
        Arc::new(VaultServiceHandlerStub)
    }
    fn cred_pair_store_grpc_impl(&self) -> Arc<dyn CredPairStoreServiceHandler> {
        self.store.clone()
    }
    fn cred_pair_local_rows(&self) -> Option<Arc<dyn diskstore::LocalRows>> {
        Some(Arc::new(NotB))
    }
    async fn shutdown(&self) {}
}

/// Admits every call: the ServiceManager wires the real authorizer; this test is
/// about the store service's binding, not its auth decision.
struct AllowAll;
#[async_trait::async_trait]
impl Authorizer for AllowAll {
    async fn authorize(&self, _m: &str, _a: Option<&str>) -> Result<HashMap<String, String>, tonic::Status> {
        Ok(HashMap::new())
    }
    async fn check_at_or_below(&self, _m: &str, _a: Option<&str>, _ns: &str) -> Result<HashMap<String, String>, tonic::Status> {
        Ok(HashMap::new())
    }
    async fn authenticate(&self, _m: &str, _a: Option<&str>) -> Result<HashMap<String, String>, tonic::Status> {
        Ok(HashMap::new())
    }
}

#[tokio::test]
async fn hosted_store_service_feeds_the_roll_scheduler() {
    let handler = Arc::new(TestHandler { store: Arc::new(MemStore::default()) });

    // The store service, as the ServiceManager serves it: the generated wrapper
    // over the Handler's getter.
    let wrapper = ValidationWrapper::new(handler.cred_pair_store_grpc_impl());
    wrapper.set_authorize_func(Some(Arc::new(AllowAll))).await;
    for id in ["a", "b", "c"] {
        let row = CredPair { client_id: id.to_string(), client_secret: format!("s-{id}"), ..Default::default() };
        CredPairStoreService::put(&wrapper, tonic::Request::new(row)).await.expect("put through the store service");
    }
    let listed = CredPairStoreService::list(&wrapper, tonic::Request::new(Default::default()))
        .await
        .expect("list through the store service")
        .into_inner();
    assert_eq!(listed.items.len(), 3, "the store service must serve the handler's rows");

    // Fail closed without an authorizer: hosting is the wired wrapper, not a bypass.
    let unwired = ValidationWrapper::new(handler.cred_pair_store_grpc_impl());
    let err = CredPairStoreService::list(&unwired, tonic::Request::new(Default::default())).await.unwrap_err();
    assert_eq!(err.code(), tonic::Code::Unauthenticated);

    // The roll scheduler the ServiceManager spawns for cred_pair.
    let base = std::env::temp_dir().join(format!("store-roll-test-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&base);
    let log = Arc::new(
        diskstore_stores::open_cred_pair_log(&base, diskstore::ROOT_TENANT, 1, diskstore::Codec::None).expect("open log"),
    );
    let task = tokio::spawn(quorumvault_model::s2sm::run_cred_pair_roll_scheduler(
        handler.clone(),
        log.clone(),
        Duration::from_secs(1),
        "node-0/cred_pair".to_string(),
    ));

    let mut keys: Vec<String> = Vec::new();
    for _ in 0..100 {
        tokio::time::sleep(Duration::from_millis(100)).await;
        let records = diskstore::dedup_file(log.active_path()).expect("read the rolled baseline");
        if !records.is_empty() {
            for rec in &records {
                let row = diskstore_stores::decode_cred_pair(rec).expect("decode").expect("not a tombstone");
                assert_eq!(row.client_secret, format!("s-{}", row.client_id));
            }
            keys = records.into_iter().map(|r| r.key).collect();
            break;
        }
    }
    task.abort();
    let _ = std::fs::remove_dir_all(&base);
    keys.sort();
    assert_eq!(keys, vec!["a", "c"], "the roll baseline must be the store's rows, narrowed to this node's");
}
